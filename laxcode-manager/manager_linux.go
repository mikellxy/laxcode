package main

import (
	"context"
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
	cfg       config
	k         *kernel
	l         *ledger
	d         *dockerClient
	bindings  map[string]binding
	ready     bool
	fatal     error
	lastError string
	lastScan  time.Time
	wall      time.Time
	boot      uint64
	wake      chan struct{}
}

func (m *manager) healthy() bool {
	return m.ready && m.fatal == nil && time.Since(m.lastScan) < 10*time.Second
}
func (m *manager) fail(err error) {
	m.fatal = err
	m.lastError = err.Error()
	m.ready = false
	m.k.disable()
	log.Printf("guard disabled: %v", err)
}
func (m *manager) checkClock() error {
	now := time.Now()
	boot, err := bootNS()
	if err != nil {
		m.lastError = err.Error()
		return err
	}
	if !m.wall.IsZero() {
		// Time.Sub would use Go's monotonic clock and hide a wall-clock step.
		drift := time.Duration(now.UnixNano()-m.wall.UnixNano()) - time.Duration(boot-m.boot)
		if drift > 5*time.Second || drift < -5*time.Second {
			return errors.New("wall clock jumped; restart after checking time")
		}
	}
	m.wall, m.boot = now, boot
	return nil
}
func (m *manager) syncEpoch(e epoch, refill bool) (epoch, error) {
	if err := m.k.initialize(e); err != nil {
		return e, err
	}
	u, g, err := m.k.snapshot(e.ID)
	if err != nil {
		return e, err
	}
	if g.Bytes > e.Granted || g.Version > e.Version || (g.Version == e.Version && g.Bytes != e.Granted) ||
		u.RX > g.Bytes || u.TX > g.Bytes-u.RX {
		return e, errors.New("kernel authorization exceeds durable ledger")
	}
	remaining := g.Bytes - u.RX - u.TX
	e, err = m.l.checkpoint(e.ID, u, refill && remaining <= m.cfg.LowWatermark)
	if err != nil {
		return e, err
	}
	if g.Bytes != e.Granted || g.Version != e.Version {
		err = m.k.objects.Maps["grants"].Update(e.ID, grant{e.Granted, e.Version}, ebpf.UpdateExist)
	}
	return e, err
}
func (m *manager) ensureUser(id string) (epoch, error) {
	now := time.Now()
	day, next := dayBounds(now)
	boot, err := bootNS()
	if err != nil {
		return epoch{}, err
	}
	// Allow the previous day's bucket to bridge the midnight rollover.
	e, err := m.l.ensure(id, day, boot+uint64(next.Sub(now)+30*time.Second))
	if err != nil {
		return e, err
	}
	if boot >= e.Deadline {
		return e, errors.New("current day's monotonic deadline expired")
	}
	e, err = m.syncEpoch(e, true)
	if err != nil {
		return e, err
	}
	err = m.k.objects.Maps["user_bucket"].Update(e.User, e.ID, ebpf.UpdateAny)
	return e, err
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
	if m.fatal != nil {
		return m.fatal
	}
	if err := m.checkClock(); err != nil {
		m.fail(err)
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	found, err := m.d.discover(ctx, m.cfg)
	if err != nil {
		m.lastError = err.Error()
		m.ready = false
		m.bindings = map[string]binding{}
		if e := m.clearOwners(); e != nil {
			m.fail(e)
		}
		return err
	}
	wanted := map[uint64]uint64{}
	for _, b := range found {
		e, err := m.ensureUser(b.User)
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
	m.bindings = found
	m.lastError = ""
	m.ready = true
	m.lastScan = time.Now()
	return nil
}
func (m *manager) refresh(refill bool) error {
	if m.fatal != nil {
		return m.fatal
	}
	if err := m.checkClock(); err != nil {
		return err
	}
	all, err := m.l.active()
	if err != nil {
		return err
	}
	now, err := bootNS()
	if err != nil {
		return err
	}
	for _, e := range all {
		_, err = m.syncEpoch(e, refill && now < e.Deadline)
		if err != nil {
			return err
		}
		if now > e.Deadline+uint64(time.Minute) {
			if err = m.l.closeEpoch(e.ID); err != nil {
				return err
			}
			var current uint64
			if m.k.objects.Maps["user_bucket"].Lookup(e.User, &current) == nil && current == e.ID {
				if err = m.k.objects.Maps["user_bucket"].Delete(e.User); err != nil {
					return err
				}
			}
			for _, name := range []string{"usage", "grants", "bucket_meta"} {
				if err = m.k.objects.Maps[name].Delete(e.ID); err != nil {
					return err
				}
			}
		}
	}
	// A supervisor can stay alive across midnight without a Docker event.
	if refill {
		seen := map[string]bool{}
		for _, b := range m.bindings {
			if !seen[b.User] {
				if _, err = m.ensureUser(b.User); err != nil {
					return err
				}
				seen[b.User] = true
			}
		}
	}
	return nil
}
func (m *manager) run(ctx context.Context) {
	go m.d.events(ctx, m.wake)
	reader, err := ringbuf.NewReader(m.k.objects.Maps["events"])
	if err != nil {
		m.mu.Lock()
		m.fail(err)
		m.mu.Unlock()
		return
	}
	defer reader.Close()
	go func() { <-ctx.Done(); reader.Close() }()
	refill := make(chan struct{}, 1)
	go func() {
		for {
			if _, err := reader.Read(); err != nil {
				return
			}
			select {
			case refill <- struct{}{}:
			default:
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
			m.mu.Lock()
			err = m.reconcile(ctx)
			m.mu.Unlock()
		case <-scan.C:
			m.mu.Lock()
			err = m.reconcile(ctx)
			m.mu.Unlock()
		case <-tick.C:
			m.mu.Lock()
			err = m.refresh(m.healthy())
			if err != nil && m.fatal == nil {
				m.fail(err)
			}
			m.mu.Unlock()
		case <-refill:
			m.mu.Lock()
			err = m.refresh(m.healthy())
			if err != nil && m.fatal == nil {
				m.fail(err)
			}
			m.mu.Unlock()
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
		if !m.healthy() {
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
		m.mu.Lock()
		defer m.mu.Unlock()
		if err := m.reconcile(r.Context()); err != nil || !m.healthy() {
			writeJSON(w, 503, map[string]string{"code": "NETWORK_QUOTA_UNAVAILABLE"})
			return
		}
		b, ok := m.bindings[r.PathValue("id")]
		if !ok {
			writeJSON(w, 503, map[string]string{"code": "NETWORK_QUOTA_UNBOUND"})
			return
		}
		day, _ := dayBounds(time.Now())
		report, err := m.l.report(b.User, day)
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
		if day == "" {
			day, _ = dayBounds(time.Now())
		}
		if _, err := time.ParseInLocation(time.DateOnly, day, shanghai); err != nil {
			writeJSON(w, 400, map[string]string{"code": "INVALID_DAY"})
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.fatal == nil {
			if err := m.refresh(false); err != nil {
				m.fail(err)
			}
		}
		report, err := m.l.report(id, day)
		if err != nil {
			writeJSON(w, 503, map[string]string{"code": "NETWORK_QUOTA_UNAVAILABLE"})
			return
		}
		report.Stale = !m.healthy()
		writeJSON(w, 200, report)
	})
	return mux
}
