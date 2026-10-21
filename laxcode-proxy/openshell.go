package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

type sandboxState struct {
	Name      string `json:"name"`
	ID        string `json:"id"`
	Phase     string `json:"phase"`
	Admission struct {
		State         string `json:"state"`
		PolicyVersion uint32 `json:"policy_version"`
	} `json:"configuration_admission"`
}
type sandboxRuntime interface {
	Lookup(context.Context, string) (*sandboxState, error)
	Create(context.Context, string, string) error
	Start(context.Context, string) error
	Stop(context.Context, string) error
	Endpoint(context.Context, string) (*url.URL, error)
}
type openshellRuntime struct {
	image, policy string
	settingsFile  string
	storeURL      string
	gatewayPort   string
	storeToken    func(string) string
	providers     []string
}

func runOpenShell(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "openshell", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("openshell %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return data, nil
}
func (o *openshellRuntime) Lookup(ctx context.Context, id string) (*sandboxState, error) {
	data, err := runOpenShell(ctx, "sandbox", "list", "--selector", "laxcode-app=laxcode,laxcode-user="+id, "--output", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Sandboxes []sandboxState `json:"sandboxes"`
		Next      string         `json:"next_page_token"`
	}
	if err = json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	if len(list.Sandboxes) == 0 {
		return nil, nil
	}
	if len(list.Sandboxes) != 1 || list.Next != "" {
		return nil, errors.New("multiple sandboxes exist for user")
	}
	s := list.Sandboxes[0]
	if s.Name != sandboxName(id) {
		return nil, errors.New("sandbox name does not match user")
	}
	if strings.EqualFold(s.Phase, "Ready") {
		if s.Admission.State != "accepted" || s.Admission.PolicyVersion == 0 {
			return nil, errors.New("sandbox configuration is not admitted")
		}
		raw, err := runOpenShell(ctx, "policy", "get", s.Name, "--rev", strconv.FormatUint(uint64(s.Admission.PolicyVersion), 10), "--output", "json")
		if err != nil {
			return nil, err
		}
		var policy struct {
			Status  string `json:"status"`
			Version uint32 `json:"version"`
			Active  uint32 `json:"active_version"`
		}
		if err = json.Unmarshal(raw, &policy); err != nil {
			return nil, err
		}
		if policy.Status != "loaded" || policy.Version != s.Admission.PolicyVersion || policy.Active != policy.Version {
			return nil, errors.New("sandbox policy is not loaded")
		}
	}
	return &s, nil
}
func (o *openshellRuntime) Create(ctx context.Context, id, root string) error {
	mounts := map[string]any{"docker": map[string]any{"mounts": []any{
		map[string]any{"type": "bind", "source": root, "target": "/workplace", "read_only": false},
		map[string]any{"type": "bind", "source": o.settingsFile, "target": layout.UserSettings("/workplace/agenthome"), "read_only": true},
	}}}
	data, _ := json.Marshal(mounts)
	args := []string{"sandbox", "create", "--name", sandboxName(id), "--from", o.image, "--detach", "--expose", "8080", "--label", "laxcode-app=laxcode", "--label", "laxcode-user=" + id, "--cpu", "2", "--memory", "2Gi", "--policy", o.policy, "--driver-config-json", string(data), "--env", "LAXCODE_USER_ID=" + id, "--env", "HOME=/workplace/agenthome"}
	if o.storeToken != nil {
		args = append(args, "--env", "LAXCODE_STORE_URL="+o.storeURL, "--env", "LAXCODE_STORE_TOKEN="+o.storeToken(id))
	}
	for _, p := range o.providers {
		args = append(args, "--provider", p)
	}
	args = append(args, "--", "/usr/local/bin/laxcode-cloud", "-addr=127.0.0.1:8080")
	_, err := runOpenShell(ctx, args...)
	return err
}
func (o *openshellRuntime) Start(ctx context.Context, name string) error {
	_, err := runOpenShell(ctx, "sandbox", "start", name)
	return err
}
func (o *openshellRuntime) Stop(ctx context.Context, name string) error {
	_, err := runOpenShell(ctx, "sandbox", "stop", name)
	return err
}
func (o *openshellRuntime) Endpoint(ctx context.Context, name string) (*url.URL, error) {
	b, e := runOpenShell(ctx, "service", "list", name, "--output", "json")
	if e != nil {
		return nil, e
	}
	var list struct {
		Services []struct {
			Service string `json:"service"`
			URL     string `json:"url"`
		} `json:"services"`
	}
	if e = json.Unmarshal(b, &list); e != nil {
		return nil, e
	}
	expectedPort := o.gatewayPort
	if expectedPort == "" {
		expectedPort = "18080"
	}
	for _, s := range list.Services {
		if s.Service == "" {
			u, e := url.Parse(s.URL)
			if e != nil || u.Scheme != "http" || !strings.HasSuffix(u.Hostname(), ".openshell.localhost") || u.Port() != expectedPort {
				return nil, errors.New("unexpected sandbox service URL")
			}
			return u, nil
		}
	}
	return nil, errors.New("sandbox has no HTTP service")
}
func waitTick(ctx context.Context) error {
	t := time.NewTimer(200 * time.Millisecond)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// OpenShell 0.1.4 routes sandbox names through DNS and limits them to 19 characters.
// The full UUID label is checked on every lookup; a hash collision fails creation safely.
func sandboxName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return fmt.Sprintf("u-%x", sum)[:19]
}
