//go:build linux && integration

package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/google/uuid"
)

// A real reboot has no surviving links; unpinning can take an RCU grace period.
func waitForTestDetach(t *testing.T, parent string) {
	t.Helper()
	f, err := os.Open(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		count := 0
		for _, attach := range []ebpf.AttachType{ebpf.AttachCGroupInetIngress, ebpf.AttachCGroupInetEgress} {
			result, err := link.QueryPrograms(link.QueryOptions{Target: int(f.Fd()), Attach: attach})
			if err != nil {
				t.Fatal(err)
			}
			count += len(result.Programs)
		}
		if count == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("test links did not detach")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

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
	m := &manager{cfg: c, k: k, l: l}
	defer m.cleanupWG.Wait()
	id := uuid.NewString()
	e, err := m.ensureUser(context.Background(), id)
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
	e, err = l.checkpoint(context.Background(), e.ID, u, true)
	if err != nil {
		t.Fatal(err)
	}
	if e.Granted != 550 {
		t.Fatal(e)
	}
	m.cleanupWG.Wait()
	k.close()
	k, err = openKernel(c, l.id)
	if err != nil {
		t.Fatal(err)
	}
	m.k = k
	if err = m.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, g, err := k.snapshot(e.ID)
	if err != nil || g.Bytes != 550 || got.RX != 250 {
		t.Fatal(got, g, err)
	}
	if err = m.refresh(context.Background()); err != nil {
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
	m.cleanupWG.Wait()
	k.close()
	k = nil
	if err = os.RemoveAll(c.PinPath); err != nil {
		t.Fatal(err)
	}
	waitForTestDetach(t, c.CgroupParent)
	k, err = openKernel(c, l.id)
	if err != nil {
		t.Fatal(err)
	}
	if !k.fresh {
		t.Fatal("expected new Maps")
	}
	// Crash after new pins are complete but before any user has been synchronized.
	m.cleanupWG.Wait()
	k.close()
	k, err = openKernel(c, l.id)
	if err != nil {
		t.Fatal(err)
	}
	if k.fresh {
		t.Fatal("expected existing Maps on retry")
	}
	m.k = k
	next, err := m.ensureUser(context.Background(), id)
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

	m.cleanupWG.Wait()
	for name, change := range map[string]func(*kernelMetadata){
		"ABI 2":   func(meta *kernelMetadata) { meta.ABI = 2 },
		"DB UUID": func(meta *kernelMetadata) { meta.DB[0] ^= 1 },
		"parent":  func(meta *kernelMetadata) { meta.ParentID++ },
		"config":  func(meta *kernelMetadata) { meta.ConfigHash[0] ^= 1 },
		"ready":   func(meta *kernelMetadata) { meta.Ready = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := k.meta
			change(&invalid)
			if err := k.objects.Maps["metadata"].Update(uint32(0), invalid, ebpf.UpdateExist); err != nil {
				t.Fatal(err)
			}
			if bad, err := openKernel(c, l.id); err == nil {
				bad.close()
				t.Fatal("accepted invalid pinned identity")
			}
			var rejected kernelMetadata
			if err := k.objects.Maps["metadata"].Lookup(uint32(0), &rejected); err != nil || rejected.Ready != 0 {
				t.Fatal("invalid identity did not disable guard", rejected, err)
			}
			if err := k.objects.Maps["metadata"].Update(uint32(0), k.meta, ebpf.UpdateExist); err != nil {
				t.Fatal(err)
			}
		})
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
	m.cleanupWG.Wait()
	k.close()
	k, err = openKernel(c, l.id)
	if err != nil {
		t.Fatal("correct config must permit verified recovery", err)
	}
	m.k = k
	if err = m.refresh(context.Background()); err != nil {
		t.Fatal(err)
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
		m.cleanupWG.Wait()
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
		waitForTestDetach(t, c.CgroupParent)
		k, err = openKernel(c, l.id)
		if err != nil {
			t.Fatal("reset left stale attachments", err)
		}
		if !k.fresh {
			t.Fatal("reset did not clear Maps")
		}
		// Missing maps trigger recovery even when the OS boot has not changed.
		m.k, m.l = k, l
		if _, err = m.ensureUser(context.Background(), id); err != nil {
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
