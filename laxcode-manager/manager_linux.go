package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/ringbuf"
)

type manager struct {
	mu        sync.Mutex
	scanMu    sync.Mutex
	userLocks map[uint64]*userEpochLock
	cleanupWG sync.WaitGroup
	cfg       config
	k         *kernel
	l         *ledger
	d         *dockerClient
	bindings  map[string]binding
	ready     bool
	fatal     error
	lastError string
	lastScan  time.Time
	wake      chan struct{}
}

func (m *manager) healthy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ready && m.fatal == nil && time.Since(m.lastScan) < 10*time.Second
}
func (m *manager) failure() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fatal
}
func (m *manager) fail(err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fatal != nil {
		return
	}
	m.fatal = err
	m.lastError = err.Error()
	m.ready = false
	m.k.disable()
	log.Printf("guard disabled: %v", err)
}
func (m *manager) ensureUser(ctx context.Context, id string) (epoch, error) {
	user, err := m.l.userID(ctx, id)
	if err != nil {
		return epoch{}, err
	}
	return m.SyncEpochToEbpf(ctx, user)
}
func (m *manager) clearOwners() error {
	it := m.k.objects.Maps["cgroup_owner"].Iterate()
	var id, user uint64
	var ids []uint64
	for it.Next(&id, &user) {
		ids = append(ids, id)
	}
	if err := it.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := m.k.objects.Maps["cgroup_owner"].Delete(id); err != nil {
			return err
		}
	}
	return nil
}
func (m *manager) reconcile(ctx context.Context) error {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()
	if err := m.failure(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	found, err := m.d.discover(ctx, m.cfg)
	if err != nil {
		m.mu.Lock()
		m.lastError = err.Error()
		m.ready = false
		m.bindings = map[string]binding{}
		m.mu.Unlock()
		if e := m.clearOwners(); e != nil {
			m.fail(e)
		}
		return err
	}
	wanted := map[uint64]uint64{}
	for _, b := range found {
		e, err := m.ensureUser(ctx, b.User)
		if err != nil {
			m.fail(err)
			return err
		}
		if previous, ok := wanted[b.Cgroup]; ok && previous != e.User {
			err = errors.New("cgroup assigned to two users")
			m.fail(err)
			return err
		}
		wanted[b.Cgroup] = e.User
		// Zero grants only workload-local loopback, never supervisor/public traffic.
		if _, exists := wanted[b.WorkloadCgroup]; exists || b.WorkloadCgroup == 0 {
			err = errors.New("workload cgroup identity conflict")
			m.fail(err)
			return err
		}
		wanted[b.WorkloadCgroup] = 0
	}
	// Remove stale mappings before publishing new ones.
	it := m.k.objects.Maps["cgroup_owner"].Iterate()
	var cg, user uint64
	var remove []uint64
	for it.Next(&cg, &user) {
		if owner, exists := wanted[cg]; !exists || owner != user {
			remove = append(remove, cg)
		}
	}
	if err = it.Err(); err != nil {
		m.fail(err)
		return err
	}
	for _, cg = range remove {
		if err = m.k.objects.Maps["cgroup_owner"].Delete(cg); err != nil {
			m.fail(err)
			return err
		}
	}
	for cg, user = range wanted {
		if err = m.k.objects.Maps["cgroup_owner"].Update(cg, user, ebpf.UpdateAny); err != nil {
			m.fail(err)
			return err
		}
	}
	m.mu.Lock()
	m.bindings = found
	if m.fatal == nil {
		m.lastError = ""
		m.ready = true
	}
	m.lastScan = time.Now()
	m.mu.Unlock()
	return nil
}
func (m *manager) refresh(ctx context.Context) error {
	if err := m.failure(); err != nil {
		return err
	}
	all, err := m.l.active(ctx)
	if err != nil {
		return err
	}
	seen := map[uint64]bool{}
	for _, e := range all {
		if seen[e.User] {
			continue
		}
		seen[e.User] = true
		if _, err = m.SyncEpochToEbpf(ctx, e.User); err != nil {
			return err
		}
	}
	return nil
}
func (m *manager) run(ctx context.Context) {
	go m.d.events(ctx, m.wake)
	reader, err := ringbuf.NewReader(m.k.objects.Maps["events"])
	if err != nil {
		m.fail(err)
		return
	}
	defer reader.Close()
	go func() { <-ctx.Done(); reader.Close() }()
	refill := make(chan uint64, 256)
	go func() {
		for {
			record, err := reader.Read()
			if err != nil {
				return
			}
			if len(record.RawSample) != 8 {
				continue
			}
			var event quotaEvent
			if _, err := binary.Decode(record.RawSample, binary.LittleEndian, &event); err != nil {
				continue
			}
			select {
			case refill <- event.User:
			default: // The periodic sync covers a full notification queue.
			}
		}
	}()
	scan := time.NewTicker(5 * time.Second)
	defer scan.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	lastLogged := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
			err = m.reconcile(ctx)
		case <-scan.C:
			err = m.reconcile(ctx)
		case <-tick.C:
			err = m.refresh(ctx)
			if err != nil {
				m.fail(err)
			}
		case user := <-refill:
			_, err = m.SyncEpochToEbpf(ctx, user)
			if err != nil {
				m.fail(err)
			}
		}
		if err != nil && err.Error() != lastLogged {
			log.Printf("reconcile/refill: %v", err)
			lastLogged = err.Error()
		} else if err == nil {
			lastLogged = ""
		}
	}
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (m *manager) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"alive": true}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		status := 200
		if !m.ready || m.fatal != nil || time.Since(m.lastScan) >= 10*time.Second {
			status = 503
		}
		counts := map[string]uint64{}
		for i, name := range []string{"parse_drop", "guard_disabled", "address_drop", "unbound_drop", "expired_drop", "event_lost", "quota_drop"} {
			var cpus []uint64
			if err := m.k.objects.Maps["diagnostics"].Lookup(uint32(i), &cpus); err == nil {
				for _, n := range cpus {
					counts[name] += n
				}
			}
		}
		writeJSON(w, status, map[string]any{"ready": status == 200, "cgroup_parent": m.cfg.CgroupParent,
			"sandbox_namespace": m.cfg.Namespace, "control_peers": m.cfg.ControlPeers,
			"workplaces_root": m.cfg.WorkplacesRoot,
			"abi":             abiVersion, "bindings": len(m.bindings),
			"last_reconcile_at": m.lastScan, "error": m.lastError, "diagnostics": counts})
	})
	mux.HandleFunc("POST /v1/sandboxes/{id}/ensure", func(w http.ResponseWriter, r *http.Request) {
		if !validUUID(r.PathValue("id")) {
			writeJSON(w, 400, map[string]string{"code": "INVALID_SANDBOX_ID"})
			return
		}
		if err := m.reconcile(r.Context()); err != nil || !m.healthy() {
			writeJSON(w, 503, map[string]string{"code": "NETWORK_QUOTA_UNAVAILABLE"})
			return
		}
		m.mu.Lock()
		b, ok := m.bindings[r.PathValue("id")]
		m.mu.Unlock()
		if !ok {
			writeJSON(w, 503, map[string]string{"code": "NETWORK_QUOTA_UNBOUND"})
			return
		}
		e, err := m.ensureUser(r.Context(), b.User)
		if err != nil {
			m.fail(err)
			writeJSON(w, 503, map[string]string{"code": "NETWORK_QUOTA_UNAVAILABLE"})
			return
		}
		report, err := m.l.report(b.User, e.Day)
		if err != nil {
			m.fail(err)
			writeJSON(w, 503, map[string]string{"code": "NETWORK_QUOTA_UNAVAILABLE"})
			return
		}
		status := 200
		code := "READY"
		if report.Exhausted {
			status = 429
			code = "NETWORK_QUOTA_EXHAUSTED"
		}
		writeJSON(w, status, map[string]any{"user_id": b.User, "sandbox_id": b.Sandbox, "code": code, "reset_at": report.ResetAt})
	})
	mux.HandleFunc("GET /v1/users/{id}/network-usage", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validUUID(id) {
			writeJSON(w, 400, map[string]string{"code": "INVALID_USER_ID"})
			return
		}
		day := r.URL.Query().Get("day")
		if _, err := time.ParseInLocation(time.DateOnly, day, shanghai); day != "" && err != nil {
			writeJSON(w, 400, map[string]string{"code": "INVALID_DAY"})
			return
		}
		e, syncErr := m.ensureUser(r.Context(), id)
		if syncErr != nil {
			m.fail(syncErr)
		}
		if day == "" {
			day = e.Day
			if day == "" {
				day, _ = dayBounds(time.Now())
			}
		}
		report, err := m.l.report(id, day)
		if err != nil {
			writeJSON(w, 503, map[string]string{"code": "NETWORK_QUOTA_UNAVAILABLE"})
			return
		}
		report.Stale = syncErr != nil || !m.healthy()
		writeJSON(w, 200, report)
	})
	return mux
}
