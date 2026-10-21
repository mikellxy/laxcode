//go:build linux && integration

package main

import (
	"os"
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
	if err := os.Mkdir(c.CgroupParent, 0755); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(c.CgroupParent)
	defer os.RemoveAll(c.PinPath)
	path := filepath.Join(t.TempDir(), "quota.db")
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
	if err = l.adopt(uuid.UUID(k.meta.Instance).String(), uuid.UUID(k.meta.Boot).String(), k.fresh); err != nil {
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
	e, err = l.checkpoint(e.ID, m.mapSet(), u, true)
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
		t.Fatal("expected new map-set")
	}
	if err = l.adopt(uuid.UUID(k.meta.Instance).String(), uuid.UUID(k.meta.Boot).String(), true); err != nil {
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
}
