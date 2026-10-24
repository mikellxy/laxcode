//go:build linux && integration

package main

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cilium/ebpf"
	"github.com/google/uuid"
)

func TestKernelLedgerRecovery(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root required")
	}
	c := defaultConfig()
	c.CgroupParent = "/sys/fs/cgroup/laxcode-recovery-test-" + strconv.Itoa(os.Getpid()) + ".slice"
	c.PinPath = "/sys/fs/bpf/laxcode-recovery-test-" + strconv.Itoa(os.Getpid())
	c.DailyLimit = 1000
	c.GrantChunk = 300
	c.LowWatermark = 100
	c.ControlPeers = []controlPeer{{"127.0.0.1", 18080, "tcp"}}
	c.StateDir = t.TempDir()
	if err := os.Mkdir(c.CgroupParent, 0755); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(c.CgroupParent)
	defer os.RemoveAll(c.PinPath)
	path := filepath.Join(c.StateDir, "quota.db")
	l, err := openLedger(path, c.DailyLimit, c.GrantChunk)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { l.db.Close() }()
	k, err := openKernel(c, l.id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if k != nil {
			k.close()
		}
	}()
	if err = l.adopt("boot-one"); err != nil {
		t.Fatal(err)
	}
	m := &manager{cfg: c, k: k, l: l}
	id := uuid.NewString()
	e, err := m.ensureUser(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = k.enable(c.LowWatermark); err != nil {
		t.Fatal(err)
	}
	// Simulate packets, then a crash after durable grant commit and before Map update.
	u := usage{RX: 250}
	if err = k.objects.Maps["usage"].Update(e.ID, u, ebpf.UpdateExist); err != nil {
		t.Fatal(err)
	}
	e, err = l.checkpoint(e.ID, u, true)
	if err != nil {
		t.Fatal(err)
	}
	if e.Granted != 550 {
		t.Fatal(e)
	}
	k.close()
	k, err = openKernel(c, l.id)
	if err != nil {
		t.Fatal(err)
	}
	m.k = k
	if err = m.refresh(false); err != nil {
		t.Fatal(err)
	}
	got, g, err := k.snapshot(e.ID)
	if err != nil || g.Bytes != 550 || got.RX != 250 {
		t.Fatal(got, g, err)
	}
	if err = m.refresh(false); err != nil {
		t.Fatal(err)
	}
	day, _ := dayBounds(time.Now())
	before, err := l.report(id, day)
	if err != nil {
		t.Fatal(err)
	}
	if before.Held != 550 {
		t.Fatal("grant replayed", before)
	}

	// Test-owned parent has no processes; remove only its pins to simulate reboot.
	k.close()
	k = nil
	if err = os.RemoveAll(c.PinPath); err != nil {
		t.Fatal(err)
	}
	k, err = openKernel(c, l.id)
	if err != nil {
		t.Fatal(err)
	}
	if !k.fresh {
		t.Fatal("expected new Maps")
	}
	// Crash after new pins are complete but before the ledger adopts this boot.
	k.close()
	k, err = openKernel(c, l.id)
	if err != nil {
		t.Fatal(err)
	}
	if k.fresh {
		t.Fatal("expected existing Maps on retry")
	}
	if err = l.adopt("boot-two"); err != nil {
		t.Fatal(err)
	}
	m.k = k
	next, err := m.ensureUser(id)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == e.ID {
		t.Fatal("reused lost epoch")
	}
	report, err := l.report(id, day)
	if err != nil {
		t.Fatal(err)
	}
	if report.Uncertain != 300 || report.Held != 850 || report.Confirmed != 250 {
		t.Fatal(report)
	}
	if err = k.enable(c.LowWatermark); err != nil {
		t.Fatal(err)
	}

	// Identity mismatch closes traffic and never resets counters or ledger.
	wrong := c
	wrong.WorkplacesRoot = "/root/another-workplace"
	if bad, err := openKernel(wrong, l.id); err == nil {
		bad.close()
		t.Fatal("accepted changed identity config")
	}
	var meta kernelMetadata
	if err = k.objects.Maps["metadata"].Lookup(uint32(0), &meta); err != nil || meta.Ready != 0 {
		t.Fatal(meta, err)
	}
	k.close()
	k, err = openKernel(c, l.id)
	if err != nil {
		t.Fatal("correct config must permit verified recovery", err)
	}
	m.k = k
	if err = m.refresh(false); err != nil {
		t.Fatal(err)
	}
	m.wall = time.Now().Add(-10 * time.Second)
	m.boot, _ = bootNS()
	if err = m.checkClock(); err == nil {
		t.Fatal("wall clock step was hidden by monotonic Time.Sub")
	}

	// A partial pin set must return an error and close the guard without panicking.
	if err = k.enable(c.LowWatermark); err != nil {
		t.Fatal(err)
	}
	pin := filepath.Join(c.PinPath, "grants")
	if err = os.Remove(pin); err != nil {
		t.Fatal(err)
	}
	if bad, err := openKernel(c, l.id); err == nil {
		bad.close()
		t.Fatal("accepted incomplete pinned maps")
	}
	if err = k.objects.Maps["metadata"].Lookup(uint32(0), &meta); err != nil || meta.Ready != 0 {
		t.Fatal(meta, err)
	}
	if err = k.objects.Maps["grants"].Pin(pin); err != nil {
		t.Fatal(err)
	}
	t.Run("maintenance reset", func(t *testing.T) {
		python, err := exec.LookPath("python3")
		if err != nil {
			t.Skip("python3 required for maintenance script test")
		}
		// Match a stopped manager: release all BPF handles and close SQLite first.
		k.close()
		k = nil
		l.db.Close()
		data, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(python, "-c", `import json,sys
sys.path.insert(0, '../deploy')
from reset_quota import reset_kernel
reset_kernel(json.loads(sys.argv[1]))`, string(data))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("maintenance script: %s: %v", out, err)
		}
		l, err = openLedger(path, c.DailyLimit, c.GrantChunk)
		if err != nil {
			t.Fatal(err)
		}
		k, err = openKernel(c, l.id)
		if err != nil {
			t.Fatal("reset left stale attachments", err)
		}
		if !k.fresh {
			t.Fatal("reset did not clear Maps")
		}
		// The OS boot has NOT changed; the script cleared the ledger marker.
		if err = l.adopt("boot-two"); err != nil {
			t.Fatal(err)
		}
		m.k, m.l = k, l
		if _, err = m.ensureUser(id); err != nil {
			t.Fatal(err)
		}
		r, err := l.report(id, day)
		if err != nil || r.Held != 1000 || r.Confirmed != 250 || r.Uncertain != 600 {
			t.Fatal("maintenance refunded an old grant", r, err)
		}
	})
}

func TestKernelRejectsLegacyMetadata(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root required")
	}
	c := defaultConfig()
	c.CgroupParent = "/sys/fs/cgroup/laxcode-legacy-test-" + strconv.Itoa(os.Getpid()) + ".slice"
	c.PinPath = "/sys/fs/bpf/laxcode-legacy-test-" + strconv.Itoa(os.Getpid())
	if err := os.MkdirAll(c.PinPath, 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(c.PinPath)
	defer os.Remove(c.CgroupParent)
	m, err := ebpf.NewMap(&ebpf.MapSpec{Type: ebpf.Hash, KeySize: 4, ValueSize: 104, MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	var legacy [104]byte
	binary.LittleEndian.PutUint32(legacy[:4], 1)
	binary.LittleEndian.PutUint32(legacy[4:8], 1)
	legacy[8] = 42
	if err = m.Update(uint32(0), legacy, ebpf.UpdateAny); err != nil {
		t.Fatal(err)
	}
	if err = m.Pin(filepath.Join(c.PinPath, "metadata")); err != nil {
		t.Fatal(err)
	}
	if k, err := openKernel(c, uuid.New()); err == nil {
		k.close()
		t.Fatal("accepted legacy ABI")
	}
	var got [104]byte
	if err = m.Lookup(uint32(0), &got); err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(legacy[4:8], 0)
	if got != legacy {
		t.Fatal("legacy ready was not cleared or other fields were overwritten")
	}
}
