package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testLedger(t *testing.T) (*ledger, string, uint64) {
	t.Helper()
	l, err := openLedger(filepath.Join(t.TempDir(), "quota.db"), 1000, 300)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.db.Close() })
	id := uuid.NewString()
	user, err := l.userID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return l, id, user
}

func TestLedgerLostAuthorizationStaysCharged(t *testing.T) {
	l, id, user := testLedger(t)
	ctx := t.Context()
	e, err := l.ensure(ctx, user, "2026-10-20", 123)
	if err != nil {
		t.Fatal(err)
	}
	e, err = l.checkpoint(ctx, e.ID, usage{}, true)
	if err != nil || e.Granted != 300 {
		t.Fatal(e, err)
	}
	if _, err = l.checkpoint(ctx, e.ID, usage{RX: 100, TX: 50}, false); err != nil {
		t.Fatal(err)
	}
	if _, err = l.checkpoint(ctx, e.ID, usage{RX: 99, TX: 50}, false); err == nil {
		t.Fatal("accepted backwards counters")
	}
	if _, err = l.db.Exec("UPDATE epochs SET status='closed' WHERE id=?", e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = l.checkpoint(ctx, e.ID, usage{RX: 100, TX: 50}, true); err == nil {
		t.Fatal("refilled a closed epoch")
	}
	next, err := l.ensure(ctx, user, e.Day, 234)
	if err != nil || next.ID == e.ID {
		t.Fatal(next, err)
	}
	for i := 0; i < 4; i++ {
		next, err = l.checkpoint(ctx, next.ID, usage{RX: next.Granted}, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	if next.Granted != 700 {
		t.Fatalf("lost authorization refunded: %+v", next)
	}
	if _, err = l.checkpoint(ctx, next.ID, usage{RX: 701}, false); err == nil {
		t.Fatal("accepted excess use")
	}
	r, err := l.report(id, e.Day)
	if err != nil || r.Held != 1000 || r.Confirmed != 850 || r.Uncertain != 150 || !r.Exhausted {
		t.Fatal(r, err)
	}
	tomorrow, err := l.ensure(ctx, user, "2026-10-21", 345)
	if err != nil {
		t.Fatal(err)
	}
	tomorrow, err = l.checkpoint(ctx, tomorrow.ID, usage{}, true)
	if err != nil || tomorrow.Granted != 300 {
		t.Fatal(tomorrow, err)
	}
}

func TestLedgerGrantCommitIsIdempotent(t *testing.T) {
	l, _, user := testLedger(t)
	e, err := l.ensure(t.Context(), user, "2026-10-20", 123)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			if _, err := l.checkpoint(t.Context(), e.ID, usage{}, true); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	for i := 0; i < 3; i++ {
		e, err = l.checkpoint(t.Context(), e.ID, usage{RX: 250}, true)
		if err != nil || e.Granted != 550 {
			t.Fatal("replayed authorization", e, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := l.checkpoint(ctx, e.ID, usage{RX: 550}, true); err == nil {
		t.Fatal("ignored canceled context")
	}
}

func TestLedgerCloseSnapshotAndLatestActive(t *testing.T) {
	l, id, user := testLedger(t)
	e, err := l.ensure(t.Context(), user, "2026-10-20", 123)
	if err != nil {
		t.Fatal(err)
	}
	e, err = l.checkpoint(t.Context(), e.ID, usage{}, true)
	if err != nil {
		t.Fatal(err)
	}
	next, err := l.ensure(t.Context(), user, "2026-10-21", 234)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := l.latestActive(t.Context(), user)
	if err != nil || latest.ID != next.ID {
		t.Fatal(latest, err)
	}
	for i := 0; i < 2; i++ {
		if err := l.closeSnapshot(t.Context(), e.ID, user, grant{300}, usage{RX: 100, TX: 50}); err != nil {
			t.Fatal("close must survive a retry before pointer publication", err)
		}
	}
	r, err := l.report(id, e.Day)
	if err != nil || r.Confirmed != 150 || r.Uncertain != 150 {
		t.Fatal(r, err)
	}
}

func TestLedgerSchemaAndUnusableRemainder(t *testing.T) {
	l, id, user := testLedger(t)
	var obsolete int
	if err := l.db.QueryRow(`SELECT
(SELECT count(*) FROM pragma_table_info('metadata') WHERE name IN ('boot_id','last_day')) +
(SELECT count(*) FROM pragma_table_info('epochs') WHERE name='version')`).Scan(&obsolete); err != nil || obsolete != 0 {
		t.Fatal(obsolete, err)
	}
	e, err := l.ensure(t.Context(), user, "2026-10-20", 123)
	if err != nil {
		t.Fatal(err)
	}
	for e.Granted < 1000 {
		e, err = l.checkpoint(t.Context(), e.ID, usage{RX: e.Granted}, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = l.checkpoint(t.Context(), e.ID, usage{RX: 980}, false); err != nil {
		t.Fatal(err)
	}
	r, err := l.report(id, e.Day)
	if err != nil || !r.Exhausted || r.Confirmed != 980 {
		t.Fatal(r, err)
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
