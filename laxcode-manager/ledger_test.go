package main

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLedgerNeverRefundsLostKernelAuthorization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.db")
	l, err := openLedger(path, 1000, 300)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if err = l.adopt("one", "boot", true); err != nil {
		t.Fatal(err)
	}
	e, err := l.ensure(id, "2026-10-20", "one", 123)
	if err != nil {
		t.Fatal(err)
	}
	e, err = l.checkpoint(e.ID, "one", usage{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if e.Granted != 300 {
		t.Fatal(e)
	}
	e, err = l.checkpoint(e.ID, "one", usage{RX: 100, TX: 50}, false)
	if err != nil {
		t.Fatal(err)
	}
	l.db.Close()
	l, err = openLedger(path, 1000, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer l.db.Close()
	if err = l.adopt("one", "boot", false); err != nil {
		t.Fatal(err)
	}
	if _, err = l.checkpoint(e.ID, "one", usage{RX: 99, TX: 50}, true); err == nil {
		t.Fatal("accepted backwards counters")
	}
	if err = l.adopt("two", "boot2", true); err != nil {
		t.Fatal(err)
	}
	e, err = l.ensure(id, "2026-10-20", "two", 234)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		e, err = l.checkpoint(e.ID, "two", usage{RX: e.Granted}, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	if e.Granted != 700 {
		t.Fatalf("lost grant refunded: %+v", e)
	}
	if _, err = l.checkpoint(e.ID, "two", usage{RX: 701}, false); err == nil {
		t.Fatal("accepted excess use")
	}
	r, err := l.report(id, "2026-10-20")
	if err != nil {
		t.Fatal(err)
	}
	if r.Held != 1000 || r.Uncertain != 150 || r.Confirmed != 850 || !r.Exhausted {
		t.Fatal(r)
	}
	next, err := l.ensure(id, "2026-10-21", "two", 345)
	if err != nil {
		t.Fatal(err)
	}
	next, err = l.checkpoint(next.ID, "two", usage{}, true)
	if err != nil || next.Granted != 300 {
		t.Fatal(next, err)
	}
	if _, err = l.ensure(id, "2026-10-20", "two", 345); err == nil {
		t.Fatal("accepted backwards day")
	}
}
func TestConcurrentAuthorizationAndIdentity(t *testing.T) {
	l, err := openLedger(filepath.Join(t.TempDir(), "quota.db"), 1000, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer l.db.Close()
	if err = l.adopt("one", "boot", true); err != nil {
		t.Fatal(err)
	}
	e, err := l.ensure(uuid.NewString(), "2026-10-20", "one", 123)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			if _, err := l.checkpoint(e.ID, "one", usage{}, true); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	all, err := l.active()
	if err != nil || len(all) != 1 || all[0].Granted != 300 {
		t.Fatal(all, err)
	}
	if err = l.adopt("other", "boot", false); err == nil {
		t.Fatal("accepted mismatched map-set")
	}
}
func TestShanghaiDayAndControlValidation(t *testing.T) {
	day, next := dayBounds(time.Date(2026, 10, 20, 16, 0, 0, 0, time.UTC))
	if day != "2026-10-21" || next.UTC().Hour() != 16 {
		t.Fatal(day, next)
	}
	for _, ip := range []string{"169.254.169.254", "100.96.0.96", "::ffff:100.100.100.200", "fe80::1"} {
		if _, err := (controlPeer{ip, 80, "tcp"}).key(); err == nil {
			t.Fatal(ip)
		}
	}
}

func TestUnusablePacketRemainderIsExhaustedWithoutFakingUsage(t *testing.T) {
	l, err := openLedger(filepath.Join(t.TempDir(), "quota.db"), 1000, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer l.db.Close()
	id := uuid.NewString()
	if err = l.adopt("one", "boot", true); err != nil {
		t.Fatal(err)
	}
	e, err := l.ensure(id, "2026-10-20", "one", 123)
	if err != nil {
		t.Fatal(err)
	}
	for e.Granted < 1000 {
		e, err = l.checkpoint(e.ID, "one", usage{RX: e.Granted}, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = l.checkpoint(e.ID, "one", usage{RX: 980}, false); err != nil {
		t.Fatal(err)
	}
	r, err := l.report(id, "2026-10-20")
	if err != nil || !r.Exhausted || r.Confirmed != 980 {
		t.Fatal(r, err)
	}
}
