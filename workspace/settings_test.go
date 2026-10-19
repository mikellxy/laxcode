package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

func TestPlatformSettingsRefreshPreservesSkillsAndSelection(t *testing.T) {
	root, source := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("default"), 0600); err != nil {
		t.Fatal(err)
	}
	catalog := Catalog{Model: "p:a", CompactionModel: "p:b", ProviderList: []config.Provider{{
		ProviderName: "p", OpenaiApiKey: "TEST_PLATFORM_KEY", OpenaiBaseUrl: "https://example.com/v1",
		ModelList: []config.Model{{ModelName: "a"}, {ModelName: "b", UpstreamModel: "upstream-b"}, {ModelName: "c"}},
	}}, Addr: "unused-address", Plan: true}
	for i := range catalog.ProviderList[0].ModelList {
		limit := config.DefaultModelLimit()
		catalog.ProviderList[0].ModelList[i].Limit = &limit
	}
	api, err := Open(root, "owner", catalog, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := api.Models.SetActiveModel("p:b"); err != nil {
		t.Fatal(err)
	}
	if err := api.Close(); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "agenthome")
	settings, err := os.ReadFile(layout.UserSettings(home))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(settings, &doc); err != nil || len(doc) != 3 {
		t.Fatalf("unexpected platform settings fields: %v %v", doc, err)
	}
	t.Setenv("TEST_PLATFORM_KEY", "openshell-placeholder")
	loaded, err := config.Load(home, nil)
	if err != nil || loaded.Model != "p:a" || loaded.CompactionModel != "p:b" ||
		loaded.ProviderList[0].OpenaiApiKey != "openshell-placeholder" || loaded.ProviderList[0].ModelList[1].UpstreamModel != "upstream-b" {
		t.Fatalf("cloud could not load copied platform settings: %v", err)
	}
	skill := filepath.Join(layout.SkillsRoot(home), "SKILL.md")
	if err := os.WriteFile(skill, []byte("user edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("new default"), 0600); err != nil {
		t.Fatal(err)
	}
	catalog.Model = "p:c"
	catalog.ProviderList[0].OpenaiBaseUrl = "https://updated.example.com/v1"
	api, err = Open(root, "owner", catalog, source)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	updated, err := LoadCatalog(layout.Root(home))
	if err != nil || updated.Model != "p:c" || updated.ProviderList[0].OpenaiBaseUrl != "https://updated.example.com/v1" {
		t.Fatal("platform settings were not refreshed", err)
	}
	if err := api.Models.LoadSelection(); err != nil || api.Models.Active().Ref != "p:b" {
		t.Fatal("user model selection was lost", err)
	}
	if data, err := os.ReadFile(skill); err != nil || string(data) != "user edit" {
		t.Fatal("user skill was overwritten", err)
	}
}

func TestPlatformSettingsDoNotFollowExistingSymlink(t *testing.T) {
	root, outside := t.TempDir(), filepath.Join(t.TempDir(), "outside.json")
	home := filepath.Join(root, "agenthome")
	if err := os.MkdirAll(layout.Root(home), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, layout.UserSettings(home)); err != nil {
		t.Fatal(err)
	}
	api, err := Open(root, "owner", Catalog{}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	if data, err := os.ReadFile(outside); err != nil || string(data) != "untouched" {
		t.Fatal("platform settings escaped user root", err)
	}
	if info, err := os.Lstat(layout.UserSettings(home)); err != nil || !info.Mode().IsRegular() {
		t.Fatal("platform settings must be a regular file", err)
	}
}
