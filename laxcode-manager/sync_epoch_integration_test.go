//go:build linux && integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/rlimit"
	"github.com/google/uuid"
)

func (k *kernel) snapshot(id uint64) (usage, grant, error) {
	var u usage
	var g grant
	err := k.objects.Maps["usage"].LookupWithFlags(id, &u, ebpf.LookupLock)
	if err != nil {
		return u, g, err
	}
	err = k.objects.Maps["grants"].Lookup(id, &g)
	return u, g, err
}

func testSyncManager(t *testing.T) (*manager, string, uint64) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("root required")
	}
	if err := rlimit.RemoveMemlock(); err != nil {
		t.Fatal(err)
	}
	l, id, user := testLedger(t)
	spec, err := loadQuota()
	if err != nil {
		t.Fatal(err)
	}
	objects, err := ebpf.NewCollection(spec)
	if err != nil {
		t.Fatalf("load quota: %+v", err)
	}
	cfg := defaultConfig()
	cfg.DailyLimit, cfg.GrantChunk, cfg.LowWatermark = 1000, 300, 100
	m := &manager{cfg: cfg, l: l, k: &kernel{objects: objects}}
	t.Cleanup(func() { m.cleanupWG.Wait(); objects.Close() })
	return m, id, user
}

func seedEpoch(t *testing.T, m *manager, user uint64, day string) epoch {
	t.Helper()
	boot, err := bootNS()
	if err != nil {
		t.Fatal(err)
	}
	e, err := m.l.ensure(t.Context(), user, day, boot+uint64(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	e, err = m.l.checkpoint(t.Context(), e.ID, usage{}, true)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func putTestBucket(t *testing.T, m *manager, e epoch, u usage, publish bool) {
	t.Helper()
	for name, value := range map[string]any{
		"bucket_meta": bucketMeta{e.User, e.Deadline}, "grants": grant{e.Granted}, "usage": u,
	} {
		if err := m.k.objects.Maps[name].Update(e.ID, value, ebpf.UpdateAny); err != nil {
			t.Fatal(err)
		}
	}
	if publish {
		if err := m.k.objects.Maps["user_bucket"].Update(e.User, e.ID, ebpf.UpdateAny); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSyncEpochRecoveryCombinations(t *testing.T) {
	for mask := 0; mask < 8; mask++ {
		for _, published := range []bool{false, true} {
			t.Run(fmt.Sprintf("entries=%03b/published=%v", mask, published), func(t *testing.T) {
				m, id, user := testSyncManager(t)
				day, _ := dayBounds(time.Now())
				e := seedEpoch(t, m, user, day)
				e, err := m.l.checkpoint(t.Context(), e.ID, usage{RX: 100, TX: 50}, false)
				if err != nil {
					t.Fatal(err)
				}
				values := []any{bucketMeta{user, e.Deadline}, grant{300}, usage{RX: 100, TX: 50, DropPackets: 7, LastEventNS: 42}}
				if mask != 0 && mask != 7 {
					// Partial entries must be discarded without validating their counters.
					values = []any{bucketMeta{999, 1}, grant{9999}, usage{RX: 9999, TX: 9999}}
				}
				for i, name := range []string{"bucket_meta", "grants", "usage"} {
					if mask&(1<<i) != 0 {
						if err := m.k.objects.Maps[name].Update(e.ID, values[i], ebpf.UpdateAny); err != nil {
							t.Fatal(err)
						}
					}
				}
				if published {
					if err := m.k.objects.Maps["user_bucket"].Update(user, e.ID, ebpf.UpdateAny); err != nil {
						t.Fatal(err)
					}
				}
				got, err := m.SyncEpochToEbpf(t.Context(), user)
				if err != nil {
					t.Fatal(err)
				}
				m.cleanupWG.Wait()
				if (got.ID == e.ID) != (mask != 0) || got.Granted != 300 {
					t.Fatalf("incorrect recovery epoch: before=%+v after=%+v", e, got)
				}
				u, g, err := m.k.snapshot(got.ID)
				if err != nil || g.Bytes != got.Granted || u.RX != got.RX || u.TX != got.TX {
					t.Fatal(u, g, got, err)
				}
				if mask == 7 && (u.DropPackets != 7 || u.LastEventNS != 42) {
					t.Fatal("overwrote complete live usage", u)
				}
				r, err := m.l.report(id, day)
				if err != nil {
					t.Fatal(err)
				}
				if mask == 0 {
					if r.Held != 600 || r.Uncertain != 150 || u.RX+u.TX != 0 {
						t.Fatal("lost maps refunded old authorization", r, u)
					}
				} else if r.Held != 300 || r.Uncertain != 0 || u.RX+u.TX != 150 {
					t.Fatal("interrupted initialization lost committed authorization", r, u)
				}
			})
		}
	}
}

func TestSyncEpochUnpublishedCommit(t *testing.T) {
	m, _, user := testSyncManager(t)
	// Fill the user pointer map so only the final publish step fails.
	for i := uint64(1); i <= 4096; i++ {
		if err := m.k.objects.Maps["user_bucket"].Update(user+i, i, ebpf.UpdateAny); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.SyncEpochToEbpf(t.Context(), user); err == nil {
		t.Fatal("expected final pointer publication to fail")
	}
	before, err := m.l.latestActive(t.Context(), user)
	if err != nil || before.Granted != 300 {
		t.Fatal(before, err)
	}
	if err := m.k.objects.Maps["user_bucket"].Delete(user + 1); err != nil {
		t.Fatal(err)
	}
	after, err := m.SyncEpochToEbpf(t.Context(), user)
	if err != nil || before.ID != after.ID || after.Granted != 300 {
		t.Fatal("unpublished committed grant was retired or replayed", before, after, err)
	}
}

func TestSyncEpochRollover(t *testing.T) {
	for _, staged := range []bool{false, true} {
		t.Run(fmt.Sprint("staged=", staged), func(t *testing.T) {
			m, id, user := testSyncManager(t)
			day, _ := dayBounds(time.Now())
			yesterday, _ := dayBounds(time.Now().AddDate(0, 0, -1))
			old := seedEpoch(t, m, user, yesterday)
			putTestBucket(t, m, old, usage{RX: 100, TX: 50}, true)
			var stagedID uint64
			if staged {
				e := seedEpoch(t, m, user, day)
				stagedID = e.ID
				if err := m.k.objects.Maps["grants"].Update(e.ID, grant{300}, ebpf.UpdateAny); err != nil {
					t.Fatal(err)
				}
				if err := m.l.closeSnapshot(t.Context(), old.ID, user, grant{300}, usage{RX: 100, TX: 50}); err != nil {
					t.Fatal(err)
				}
			}
			e, err := m.SyncEpochToEbpf(t.Context(), user)
			if err != nil || e.ID == old.ID || e.Day != day || e.Granted != 300 || e.RX+e.TX != 0 || (staged && e.ID != stagedID) {
				t.Fatal(e, err)
			}
			m.cleanupWG.Wait()
			r, err := m.l.report(id, yesterday)
			if err != nil || r.Confirmed != 150 || r.Held != 300 || r.Uncertain != 150 {
				t.Fatal(r, err)
			}
			bm, g, u, err := m.k.bucket(old.ID)
			if err != nil || bm != nil || g != nil || u != nil {
				t.Fatal("old map entries leaked", bm, g, u, err)
			}
		})
	}
}

func TestSyncEpochConcurrentAndTargetedUsage(t *testing.T) {
	m, id, user := testSyncManager(t)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Go(func() {
			if e, err := m.SyncEpochToEbpf(t.Context(), user); err != nil || e.Granted != 300 {
				t.Error(e, err)
			}
		})
	}
	wg.Wait()
	m.cleanupWG.Wait()
	e, err := m.l.latestActive(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}
	u := usage{RX: 250, DropPackets: 9, LastEventNS: 123}
	if err := m.k.objects.Maps["usage"].Update(e.ID, u, ebpf.UpdateLock); err != nil {
		t.Fatal(err)
	}
	// Holding a different user's lock must not block a target-only usage query.
	other, err := m.l.userID(t.Context(), uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	lock := m.userLock(other)
	lock.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		response := httptest.NewRecorder()
		m.handler().ServeHTTP(response, httptest.NewRequest("GET", "/v1/users/"+id+"/network-usage", nil))
		var report usageResponse
		if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil || response.Code != 200 || report.Confirmed != 250 || report.Held != 550 {
			t.Errorf("usage response: %d %s %v", response.Code, response.Body.String(), err)
		}
	}()
	select {
	case <-done:
		lock.Unlock()
	case <-time.After(3 * time.Second):
		lock.Unlock()
		<-done
		t.Fatal("another user's lock blocked usage query")
	}
	got, g, err := m.k.snapshot(e.ID)
	if err != nil || got != u || g.Bytes != 550 {
		t.Fatal("refill overwrote live usage", got, g, err)
	}
}

func TestSyncEpochCleanupRetriesAndKeepsCurrent(t *testing.T) {
	m, _, user := testSyncManager(t)
	day, _ := dayBounds(time.Now())
	yesterday, _ := dayBounds(time.Now().AddDate(0, 0, -1))
	old := seedEpoch(t, m, user, yesterday)
	if _, err := m.l.db.Exec(fmt.Sprintf(`CREATE TRIGGER fail_cleanup BEFORE UPDATE OF status ON epochs
WHEN OLD.id=%d BEGIN SELECT RAISE(ABORT, 'cleanup interrupted'); END`, old.ID)); err != nil {
		t.Fatal(err)
	}
	current, err := m.SyncEpochToEbpf(t.Context(), user)
	if err != nil {
		t.Fatal(err)
	}
	m.cleanupWG.Wait()
	if m.failure() != nil {
		t.Fatal("best-effort cleanup disabled guard", m.failure())
	}
	if _, err := m.l.db.Exec("DROP TRIGGER fail_cleanup"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SyncEpochToEbpf(t.Context(), user); err != nil {
		t.Fatal(err)
	}
	m.cleanupWG.Wait()
	var status string
	if err := m.l.db.QueryRow("SELECT status FROM epochs WHERE id=?", old.ID).Scan(&status); err != nil || status != "closed" {
		t.Fatal("historical active not retired", status, err)
	}
	// A delayed cleanup must read the replacement pointer, rather than a captured ID.
	if _, err := m.l.db.Exec("UPDATE epochs SET status='closed' WHERE id=?", current.ID); err != nil {
		t.Fatal(err)
	}
	next := seedEpoch(t, m, user, day)
	putTestBucket(t, m, next, usage{}, true)
	lock := m.userLock(user)
	lock.Lock()
	err = m.cleanupHistory(t.Context(), user, day)
	lock.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.l.db.QueryRow("SELECT status FROM epochs WHERE id=?", next.ID).Scan(&status); err != nil || status != "active" {
		t.Fatal("cleanup retired current epoch", status, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := m.SyncEpochToEbpf(ctx, user); !errors.Is(err, context.Canceled) {
		t.Fatal("ignored cancellation", err)
	}
}
