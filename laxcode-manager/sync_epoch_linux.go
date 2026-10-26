package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/cilium/ebpf"
)

type userEpochLock struct {
	sync.Mutex
	cleanedEpoch   uint64
	cleanupPending bool
}

func (m *manager) userLock(user uint64) *userEpochLock {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.userLocks == nil {
		m.userLocks = make(map[uint64]*userEpochLock)
	}
	if m.userLocks[user] == nil {
		m.userLocks[user] = new(userEpochLock)
	}
	return m.userLocks[user]
}

// SyncEpochToEbpf owns the complete synchronization sequence in docs/epoch_opt_v2.md.
func (m *manager) SyncEpochToEbpf(ctx context.Context, userID uint64) (epoch, error) {
	// 1. Only the user lock spans DB and kernel operations.
	lock := m.userLock(userID)
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return epoch{}, err
	}
	if err := m.failure(); err != nil {
		return epoch{}, err
	}

	// 2. Capture the accounting date once, after acquiring the user lock.
	now := time.Now()
	day, next := dayBounds(now)
	boot, err := bootNS()
	if err != nil {
		return epoch{}, err
	}

	// 3. An unpublished initialization is reachable through the active DB epoch.
	var bucketID uint64
	if err = m.k.objects.Maps["user_bucket"].Lookup(userID, &bucketID); errors.Is(err, ebpf.ErrKeyNotExist) {
		previous, lookupErr := m.l.latestActive(ctx, userID)
		if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
			return epoch{}, lookupErr
		}
		bucketID = previous.ID
	} else if err != nil {
		return epoch{}, err
	}
	bm, g, u, err := m.k.bucket(bucketID)
	if err != nil {
		return epoch{}, err
	}
	var dayBucketMeta *bucketMeta
	var dayGrants *grant
	var dayUsage *usage
	switch {
	case (bm != nil || g != nil || u != nil) && (bm == nil || g == nil || u == nil):
		// 4. Partial insertion is recoverable from DB; do not retire its epoch.
		if err = m.k.deleteBucket(bucketID); err != nil {
			return epoch{}, err
		}
	case bm == nil && g == nil && u == nil:
		// 5. Lost kernel state keeps the old authorization charged to this day.
		if _, err = m.l.db.ExecContext(ctx, "UPDATE epochs SET status='closed' WHERE user_id=? AND day=? AND status='active'", userID, day); err != nil {
			return epoch{}, err
		}
	default:
		// 6. A complete bucket keeps its live kernel counters.
		if bm.User != userID {
			return epoch{}, errors.New("bucket belongs to another user")
		}
		dayBucketMeta, dayGrants, dayUsage = bm, g, u
	}

	// 7. Get or create today's active epoch, keeping the 30-second deadline grace period.
	activeEpoch, err := m.l.ensure(ctx, userID, day, boot+uint64(next.Sub(now)+30*time.Second))
	if err != nil {
		return epoch{}, err
	}

	// 8. Snapshot, close, then switch: a small amount of rollover traffic may be uncounted.
	if dayBucketMeta != nil && bucketID != activeEpoch.ID {
		if err = m.l.closeSnapshot(ctx, bucketID, userID, *dayGrants, *dayUsage); err != nil {
			return epoch{}, err
		}
		dayBucketMeta, dayGrants, dayUsage = nil, nil, nil
	}

	// 9. All four recovery paths share this initialization; publish metadata in step 11.
	if dayBucketMeta == nil {
		dayGrants = &grant{Bytes: activeEpoch.Granted}
		dayUsage = &usage{RX: activeEpoch.RX, TX: activeEpoch.TX}
		if err = m.k.objects.Maps["grants"].Update(activeEpoch.ID, dayGrants, ebpf.UpdateAny); err != nil {
			return epoch{}, err
		}
		if err = m.k.objects.Maps["usage"].Update(activeEpoch.ID, dayUsage, ebpf.UpdateAny); err != nil {
			return epoch{}, err
		}
	}

	// 10. Commit usage and cumulative authorization before publishing grants. Never overwrite live usage.
	if dayGrants.Bytes > activeEpoch.Granted || dayUsage.RX > dayGrants.Bytes || dayUsage.TX > dayGrants.Bytes-dayUsage.RX {
		return epoch{}, errors.New("kernel authorization exceeds durable ledger")
	}
	remaining := dayGrants.Bytes - dayUsage.RX - dayUsage.TX
	activeEpoch, err = m.l.checkpoint(ctx, activeEpoch.ID, *dayUsage, remaining <= m.cfg.LowWatermark)
	if err != nil {
		return epoch{}, err
	}
	if dayGrants.Bytes != activeEpoch.Granted {
		if err = m.k.objects.Maps["grants"].Update(activeEpoch.ID, grant{Bytes: activeEpoch.Granted}, ebpf.UpdateExist); err != nil {
			return epoch{}, err
		}
	}

	// 11. A bucket becomes eligible only once all three entries exist.
	if err = m.k.objects.Maps["bucket_meta"].Update(activeEpoch.ID, bucketMeta{userID, activeEpoch.Deadline}, ebpf.UpdateAny); err != nil {
		return epoch{}, err
	}
	// 12. Publish the user pointer last, keeping an interrupted new bucket discoverable in DB.
	if err = m.k.objects.Maps["user_bucket"].Update(userID, activeEpoch.ID, ebpf.UpdateAny); err != nil {
		return epoch{}, err
	}

	// 13. Cleanup is best effort and must re-read the pointer under the same user lock.
	if lock.cleanedEpoch != activeEpoch.ID && !lock.cleanupPending {
		lock.cleanupPending = true
		m.cleanupWG.Go(func() {
			lock.Lock()
			defer lock.Unlock()
			lock.cleanupPending = false
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := m.cleanupHistory(cleanupCtx, userID, day); err != nil {
				log.Printf("epoch cleanup user=%d: %v", userID, err)
			} else {
				lock.cleanedEpoch = activeEpoch.ID
			}
		})
	}
	return activeEpoch, nil
}

func (m *manager) cleanupHistory(ctx context.Context, user uint64, day string) error {
	var current uint64
	if err := m.k.objects.Maps["user_bucket"].Lookup(user, &current); err != nil {
		return err
	}
	if _, err := m.l.db.ExecContext(ctx, "UPDATE epochs SET status='closed' WHERE user_id=? AND day<=? AND id!=? AND status='active'", user, day, current); err != nil {
		return err
	}
	ids, err := m.l.historicalEpochs(ctx, user, day, current)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := m.k.deleteBucket(id); err != nil {
			return err
		}
	}
	return nil
}
