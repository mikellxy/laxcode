package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialReferenceResolution(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".laxcode")
	os.MkdirAll(dir, 0700)
	raw := `{"provider_list":[{"provider_name":"ark","openai_api_key":"ARK_API_KEY","openai_base_url":"https://example.com/v1","model_list":[{"model_name":"m"}]}]}`
	path := filepath.Join(dir, "settings.json")
	os.WriteFile(path, []byte(raw), 0600)
	t.Setenv("ARK_API_KEY", "opaque-runtime-reference")
	c, e := LoadSettings(home, nil)
	if e != nil || c.ProviderList[0].OpenaiApiKey != "opaque-runtime-reference" {
		t.Fatalf("%v", e)
	}
	data, _ := os.ReadFile(path)
	if string(data) != raw {
		t.Fatal("settings mutated")
	}
	if _, e = os.Stat(filepath.Join(home, ".laxcode", "configs.db")); !os.IsNotExist(e) {
		t.Fatal("credential database created")
	}
	t.Setenv("ARK_API_KEY", "")
	if _, e = LoadSettings(home, nil); e == nil {
		t.Fatal("accepted missing credential")
	}
	p := []Provider{{ProviderName: "p", OpenaiApiKey: "sk-real-secret"}}
	if e = LoadProviderAPIKeys(p); e == nil || strings.Contains(e.Error(), "sk-real-secret") {
		t.Fatal("plaintext accepted or exposed")
	}
}
