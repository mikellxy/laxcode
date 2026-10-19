package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSandboxUsesTrustedReadOnlySettingsMount(t *testing.T) {
	bin, user, release := t.TempDir(), t.TempDir(), t.TempDir()
	record := filepath.Join(bin, "args")
	if err := os.WriteFile(filepath.Join(bin, "openshell"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$LAXCODE_TEST_ARGS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LAXCODE_TEST_ARGS", record)
	runtime := &openshellRuntime{image: "laxcode-cloud:test", settingsFile: filepath.Join(release, "settings.json")}
	if err := runtime.Create(context.Background(), testID, user); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")
	var mounts []struct {
		Source, Target string
		ReadOnly       bool `json:"read_only"`
	}
	for i, arg := range args {
		if arg == "--driver-config-json" {
			var config struct {
				Docker struct {
					Mounts json.RawMessage
				}
			}
			if err := json.Unmarshal([]byte(args[i+1]), &config); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(config.Docker.Mounts, &mounts); err != nil {
				t.Fatal(err)
			}
		}
		if strings.Contains(arg, "settings_dir") || strings.Contains(arg, "LAXCODE_DEFAULT_SKILLS") {
			t.Fatalf("retired sandbox argument remains: %s", arg)
		}
	}
	if len(mounts) != 2 || mounts[0].Source != user || mounts[0].Target != "/workplace" || mounts[0].ReadOnly ||
		mounts[1].Source != runtime.settingsFile || mounts[1].Target != "/workplace/agenthome/.laxcode/settings.json" || !mounts[1].ReadOnly {
		t.Fatalf("user workspace must be writable and platform settings must come from the trusted release read-only: %+v", mounts)
	}
}
