package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type dockerClient struct {
	client  *http.Client
	version string
}
type container struct {
	ID     string `json:"Id"`
	Labels map[string]string
	Config struct{ Labels map[string]string }
	Mounts []struct{ Type, Source, Destination string }
	State  struct {
		Running bool
		Pid     int
	}
	HostConfig struct {
		NetworkMode  string
		CgroupParent string
	}
}
type containerSummary struct {
	ID     string `json:"Id"`
	Labels map[string]string
}
type binding struct {
	Sandbox, User, Container string
	Cgroup, WorkloadCgroup   uint64
}

func newDocker(socket string) *dockerClient {
	t := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
	}, ResponseHeaderTimeout: 3 * time.Second}
	return &dockerClient{client: &http.Client{Transport: t}}
}
func (d *dockerClient) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://docker"+path, nil)
	if err != nil {
		return err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Docker %s: HTTP %d", strings.Split(path, "?")[0], resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}
func (d *dockerClient) negotiate(ctx context.Context) error {
	var v struct {
		APIVersion string `json:"ApiVersion"`
	}
	if err := d.get(ctx, "/version", &v); err != nil {
		return err
	}
	parts := strings.Split(v.APIVersion, ".")
	if len(parts) != 2 || parts[0] != "1" {
		return errors.New("unsupported Docker API")
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil || minor < 41 {
		return errors.New("Docker API >= 1.41 required")
	}
	d.version = "/v1." + strconv.Itoa(minor)
	return nil
}
func (d *dockerClient) inspect(ctx context.Context, id string) (container, error) {
	var c container
	if len(id) != 64 {
		return c, errors.New("Docker full container ID required")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return c, err
	}
	err := d.get(ctx, d.version+"/containers/"+id+"/json", &c)
	if err == nil && c.ID != id {
		err = errors.New("Docker container identity changed")
	}
	return c, err
}
func userName(id string) string {
	h := sha256.Sum256([]byte(id))
	return "u-" + hex.EncodeToString(h[:])[:17]
}
func validUUID(s string) bool {
	u, e := uuid.Parse(s)
	return e == nil && u != uuid.Nil && u.String() == s
}
func identity(labels map[string]string, namespace string) (sandbox, role string, err error) {
	sandbox, role = labels["openshell.ai/sandbox-id"], labels["openshell.ai/isolation-role"]
	if labels["openshell.ai/managed-by"] != "openshell" || labels["openshell.ai/sandbox-namespace"] != namespace ||
		!validUUID(sandbox) {
		err = errors.New("invalid OpenShell identity")
		return
	}
	if role != "sandbox" && role != "supervisor" {
		err = errors.New("invalid OpenShell role")
	}
	return
}

// OpenShell 0.1.4 keeps resource labels and user env outside Docker metadata.
// Use the host-controlled bind source created by proxy, never workload content.
func workloadUser(c container, root string) (string, bool, error) {
	source := ""
	for _, mount := range c.Mounts {
		if mount.Destination != "/workplace" {
			continue
		}
		if source != "" || mount.Type != "bind" {
			return "", false, errors.New("invalid workplace mount")
		}
		source = mount.Source
	}
	if source == "" {
		return "", false, nil
	}
	id := filepath.Base(source)
	if source != filepath.Join(root, id) || !validUUID(id) ||
		c.Config.Labels["openshell.ai/sandbox-name"] != userName(id) {
		return "", false, errors.New("invalid workload user binding")
	}
	return id, true, nil
}
func processIdentity(pid int) (string, string, error) {
	if pid <= 0 {
		return "", "", errors.New("invalid container PID")
	}
	root := "/proc/" + strconv.Itoa(pid)
	stat, err := os.ReadFile(root + "/stat")
	if err != nil {
		return "", "", err
	}
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return "", "", errors.New("malformed process stat")
	}
	fields := strings.Fields(string(stat)[end+1:])
	if len(fields) < 20 {
		return "", "", errors.New("short process stat")
	}
	raw, err := os.ReadFile(root + "/cgroup")
	if err != nil {
		return "", "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "0::/") {
			return fields[19], strings.TrimPrefix(line, "0::"), nil
		}
	}
	return "", "", errors.New("unified cgroup required")
}
func (d *dockerClient) discover(ctx context.Context, cfg config) (map[string]binding, error) {
	if d.version == "" {
		if err := d.negotiate(ctx); err != nil {
			return nil, err
		}
	}
	filters, _ := json.Marshal(map[string][]string{"label": {"openshell.ai/managed-by=openshell", "openshell.ai/sandbox-namespace=" + cfg.Namespace}})
	var list []containerSummary
	if err := d.get(ctx, d.version+"/containers/json?all=1&filters="+url.QueryEscape(string(filters)), &list); err != nil {
		return nil, err
	}
	workloads := map[string]containerSummary{}
	supervisors := map[string]containerSummary{}
	for _, item := range list {
		role := item.Labels["openshell.ai/isolation-role"]
		if role != "sandbox" && role != "supervisor" {
			continue
		}
		sid, _, err := identity(item.Labels, cfg.Namespace)
		if err != nil {
			return nil, err
		}
		dest := workloads
		if role == "supervisor" {
			dest = supervisors
		}
		if _, exists := dest[sid]; exists {
			return nil, errors.New("duplicate sandbox role")
		}
		dest[sid] = item
	}
	result := map[string]binding{}
	for sid, workload := range workloads {
		supervisor, ok := supervisors[sid]
		if !ok {
			continue
		}
		w, err := d.inspect(ctx, workload.ID)
		if err != nil {
			return nil, err
		}
		user, managed, err := workloadUser(w, cfg.WorkplacesRoot)
		if err != nil {
			return nil, err
		}
		if !managed {
			continue
		}
		s, err := d.inspect(ctx, supervisor.ID)
		if err != nil {
			return nil, err
		}
		ws, _, err := identity(w.Config.Labels, cfg.Namespace)
		if err != nil {
			return nil, err
		}
		ss, role, err := identity(s.Config.Labels, cfg.Namespace)
		if err != nil {
			return nil, err
		}
		if ws != sid || ss != sid || role != "supervisor" || w.Config.Labels["openshell.ai/sandbox-name"] != s.Config.Labels["openshell.ai/sandbox-name"] {
			return nil, errors.New("supervisor/workload identity mismatch")
		}
		if !s.State.Running || !w.State.Running {
			continue
		}
		if s.HostConfig.NetworkMode != "host" || w.HostConfig.NetworkMode != "none" {
			return nil, errors.New("unexpected OpenShell network mode")
		}
		workloadNS, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/net", w.State.Pid))
		if err != nil {
			return nil, err
		}
		hostNS, err := os.Readlink("/proc/1/ns/net")
		if err != nil || workloadNS == hostNS {
			return nil, errors.New("workload must have an isolated network namespace")
		}
		cgid, err := d.guardedCgroup(ctx, s, cfg.CgroupParent)
		if err != nil {
			return nil, err
		}
		workloadID, err := d.guardedCgroup(ctx, w, cfg.CgroupParent)
		if err != nil {
			return nil, err
		}
		if cgid == workloadID {
			return nil, errors.New("supervisor and workload must have distinct cgroups")
		}
		result[sid] = binding{sid, user, s.ID, cgid, workloadID}
	}
	return result, nil
}

func (d *dockerClient) guardedCgroup(ctx context.Context, c container, parent string) (uint64, error) {
	start, rel, err := processIdentity(c.State.Pid)
	if err != nil {
		return 0, err
	}
	path := filepath.Join("/sys/fs/cgroup", rel)
	if !strings.HasPrefix(path, parent+"/") {
		return 0, fmt.Errorf("container outside guarded parent: %s", c.ID)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return 0, errors.New("noncanonical container cgroup")
	}
	id, err := cgroupID(path)
	if err != nil {
		return 0, err
	}
	start2, rel2, err := processIdentity(c.State.Pid)
	if err != nil {
		return 0, err
	}
	again, err := d.inspect(ctx, c.ID)
	if err != nil {
		return 0, err
	}
	if start != start2 || rel != rel2 || !again.State.Running || again.State.Pid != c.State.Pid {
		return 0, errors.New("container changed during discovery")
	}
	return id, nil
}

// Events only prompt reconciliation: their attributes are never trusted as identity.
func (d *dockerClient) events(ctx context.Context, wake chan<- struct{}) {
	for ctx.Err() == nil {
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://docker/events?filters="+url.QueryEscape(`{"type":["container"],"event":["create","start","stop","die","destroy","restart","pause","unpause"]}`), nil)
		resp, err := d.client.Do(req)
		if err == nil {
			if resp.StatusCode == 200 {
				dec := json.NewDecoder(resp.Body)
				for {
					var event json.RawMessage
					if dec.Decode(&event) != nil {
						break
					}
					select {
					case wake <- struct{}{}:
					default:
					}
				}
			}
			resp.Body.Close()
		}
		select {
		case wake <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
