package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

func TestCatalogRefreshPreservesSkillsAndSelection(t *testing.T) {
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
	if _, err := os.Stat(layout.UserSettings(home)); !os.IsNotExist(err) {
		t.Fatalf("workspace must not write platform settings: %v", err)
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
	if got := api.Models.Active().Ref; got != "p:c" {
		t.Fatalf("platform default was not refreshed: %s", got)
	}
	if err := api.Models.LoadSelection(); err != nil || api.Models.Active().Ref != "p:b" {
		t.Fatal("user model selection was lost", err)
	}
	if data, err := os.ReadFile(skill); err != nil || string(data) != "user edit" {
		t.Fatal("user skill was overwritten", err)
	}
}
