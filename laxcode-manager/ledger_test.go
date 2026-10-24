package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLedgerAdoptIsAtomicAndResumable(t *testing.T) {
	for _, marker := range []string{"old-boot", ""} {
		t.Run("previous-marker="+marker, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "quota.db")
			l, err := openLedger(path, 1000, 300)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { l.db.Close() }()
			id := uuid.NewString()
			e, err := l.ensure(id, "2026-10-20", 123)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = l.checkpoint(e.ID, usage{}, true); err != nil {
				t.Fatal(err)
			}
			if _, err = l.checkpoint(e.ID, usage{RX: 100}, false); err != nil {
				t.Fatal(err)
			}
			if _, err = l.db.Exec("UPDATE metadata SET boot_id=? WHERE id=1", marker); err != nil {
				t.Fatal(err)
			}
			// Fail between retiring epochs and recording recovery completion.
			if _, err = l.db.Exec(`CREATE TRIGGER fail_adopt BEFORE UPDATE OF boot_id ON metadata
BEGIN SELECT RAISE(ABORT, 'simulated interruption'); END`); err != nil {
				t.Fatal(err)
			}
			if err = l.adopt("current-boot"); err == nil {
				t.Fatal("expected transaction failure")
			}
			active, err := l.active()
			if err != nil || len(active) != 1 || active[0].ID != e.ID {
				t.Fatal("partial recovery committed", active, err)
			}
			if _, err = l.db.Exec("DROP TRIGGER fail_adopt"); err != nil {
				t.Fatal(err)
			}
			if err = l.adopt("current-boot"); err != nil {
				t.Fatal(err)
			}
			if _, err = l.checkpoint(e.ID, usage{RX: 100}, true); err == nil {
				t.Fatal("accepted a retired epoch's late checkpoint")
			}
			next, err := l.ensure(id, "2026-10-20", 234)
			if err != nil || next.ID == e.ID {
				t.Fatal(next, err)
			}
			l.db.Close()
			l, err = openLedger(path, 1000, 300)
			if err != nil {
				t.Fatal(err)
			}
			if err = l.adopt("current-boot"); err != nil {
				t.Fatal(err)
			}
			active, err = l.active()
			if err != nil || len(active) != 1 || active[0].ID != next.ID {
				t.Fatal("repeated adoption retired the new epoch", active, err)
			}
			r, err := l.report(id, "2026-10-20")
			if err != nil || r.Held != 300 || r.Confirmed != 100 || r.Uncertain != 200 || r.Allocatable != 700 {
				t.Fatal(r, err)
			}
		})
	}
}

func TestLedgerMigratesV1WithoutChangingBalances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema, err := os.ReadFile("testdata/ledger_v1.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	dbID, user := uuid.NewString(), uuid.NewString()
	if _, err = db.Exec("INSERT INTO metadata VALUES(1,?,'old-map','boot','2026-10-20')", dbID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO users VALUES(1,?)", user); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO user_days VALUES(1,'2026-10-20',1000);
INSERT INTO epochs VALUES(7,1,'2026-10-20','old-map','active',300,100,50,1,123,'saved');`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	l, err := openLedger(path, 1000, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer l.db.Close()
	if l.id.String() != dbID {
		t.Fatal("migration replaced DB identity")
	}
	if err = l.adopt("boot"); err != nil {
		t.Fatal(err)
	}
	active, err := l.active()
	if err != nil || len(active) != 1 || active[0].ID != 7 || active[0].Deadline != 123 || active[0].Version != 1 {
		t.Fatal(active, err)
	}
	r, err := l.report(user, "2026-10-20")
	if err != nil || r.Held != 300 || r.Confirmed != 150 || r.Uncertain != 0 {
		t.Fatal(r, err)
	}
	var version, obsolete int
	if err = l.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatal(version, err)
	}
	if err = l.db.QueryRow(`SELECT
(SELECT count(*) FROM pragma_table_info('metadata') WHERE name='map_set') +
(SELECT count(*) FROM pragma_table_info('epochs') WHERE name='map_set')`).Scan(&obsolete); err != nil || obsolete != 0 {
		t.Fatal(obsolete, err)
	}
	if err = l.adopt("new-boot"); err != nil {
		t.Fatal(err)
	}
	next, err := l.ensure(user, "2026-10-20", 234)
	if err != nil || next.ID <= 7 {
		t.Fatal("migration reset epoch IDs", next, err)
	}
}

func TestLedgerNeverRefundsLostKernelAuthorization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.db")
	l, err := openLedger(path, 1000, 300)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if err = l.adopt("boot"); err != nil {
		t.Fatal(err)
	}
	e, err := l.ensure(id, "2026-10-20", 123)
	if err != nil {
		t.Fatal(err)
	}
	e, err = l.checkpoint(e.ID, usage{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if e.Granted != 300 {
		t.Fatal(e)
	}
	e, err = l.checkpoint(e.ID, usage{RX: 100, TX: 50}, false)
	if err != nil {
		t.Fatal(err)
	}
	l.db.Close()
	l, err = openLedger(path, 1000, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer l.db.Close()
	if err = l.adopt("boot"); err != nil {
		t.Fatal(err)
	}
	if _, err = l.checkpoint(e.ID, usage{RX: 99, TX: 50}, true); err == nil {
		t.Fatal("accepted backwards counters")
	}
	if err = l.adopt("boot2"); err != nil {
		t.Fatal(err)
	}
	e, err = l.ensure(id, "2026-10-20", 234)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		e, err = l.checkpoint(e.ID, usage{RX: e.Granted}, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	if e.Granted != 700 {
		t.Fatalf("lost grant refunded: %+v", e)
	}
	if _, err = l.checkpoint(e.ID, usage{RX: 701}, false); err == nil {
		t.Fatal("accepted excess use")
	}
	r, err := l.report(id, "2026-10-20")
	if err != nil {
		t.Fatal(err)
	}
	if r.Held != 1000 || r.Uncertain != 150 || r.Confirmed != 850 || !r.Exhausted {
		t.Fatal(r)
	}
	next, err := l.ensure(id, "2026-10-21", 345)
	if err != nil {
		t.Fatal(err)
	}
	next, err = l.checkpoint(next.ID, usage{}, true)
	if err != nil || next.Granted != 300 {
		t.Fatal(next, err)
	}
	if _, err = l.ensure(id, "2026-10-20", 345); err == nil {
		t.Fatal("accepted backwards day")
	}
}
func TestConcurrentAuthorization(t *testing.T) {
	l, err := openLedger(filepath.Join(t.TempDir(), "quota.db"), 1000, 300)
	if err != nil {
		t.Fatal(err)
	}
	defer l.db.Close()
	if err = l.adopt("boot"); err != nil {
		t.Fatal(err)
	}
	e, err := l.ensure(uuid.NewString(), "2026-10-20", 123)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			if _, err := l.checkpoint(e.ID, usage{}, true); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	all, err := l.active()
	if err != nil || len(all) != 1 || all[0].Granted != 300 {
		t.Fatal(all, err)
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
	if err = l.adopt("boot"); err != nil {
		t.Fatal(err)
	}
	e, err := l.ensure(id, "2026-10-20", 123)
	if err != nil {
		t.Fatal(err)
	}
	for e.Granted < 1000 {
		e, err = l.checkpoint(e.ID, usage{RX: e.Granted}, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = l.checkpoint(e.ID, usage{RX: 980}, false); err != nil {
		t.Fatal(err)
	}
	r, err := l.report(id, "2026-10-20")
	if err != nil || !r.Exhausted || r.Confirmed != 980 {
		t.Fatal(r, err)
	}
}
