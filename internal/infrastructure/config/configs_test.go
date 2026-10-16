package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

func TestAPIKeyMigrationAndReload(t *testing.T) {
	host, userHome := t.TempDir(), t.TempDir()
	t.Setenv("HOME", host)
	before := `{"MODEL":"p:one","custom":{"keep":true},"PROVIDER_LIST":[
		{"provider_name":"p","OPENAI_API_KEY":"migration-secret","openai_base_url":"https://example.com/v1","custom_provider":true,"model_list":[{"model_name":"one"},{"model_name":"two"}]},
		{"provider_name":"oauth","auth_type":"oauth","credential_ref":"existing-ref","model_list":[{"model_name":"oauth-model"}]}
	]}`
	writeSettings(t, userHome, before)
	conf, err := LoadSettings(userHome, nil)
	if err != nil {
		t.Fatal(err)
	}
	if conf.ProviderList[0].OpenaiApiKey != "migration-secret" || conf.ProviderList[1].AuthType != "oauth" {
		t.Fatal("migration changed runtime credentials")
	}
	data, err := os.ReadFile(layout.UserSettings(userHome))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"migration-secret", "OPENAI_API_KEY", "openai_api_key", "cli-secret", "ephemeral"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("settings contain %q", forbidden)
		}
	}
	for _, keep := range []string{"custom_provider", "existing-ref", "https://example.com/v1", "custom"} {
		if !strings.Contains(string(data), keep) {
			t.Fatalf("migration removed %q", keep)
		}
	}
	info, err := os.Stat(layout.ConfigsDB(host))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("host database permissions: %v %v", info, err)
	}
	if _, err := os.Stat(layout.ConfigsDB(userHome)); !os.IsNotExist(err) {
		t.Fatalf("database was placed in agenthome: %v", err)
	}
	db, err := openConfigs(false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM api_keys`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("expected only two persisted API-key models, count=%d err=%v", count, err)
	}
	conf, err = LoadSettings(userHome, nil)
	if err != nil || conf.ProviderList[0].OpenaiApiKey != "migration-secret" || conf.Model != "p:one" {
		t.Fatalf("cannot reload migrated key: %v", err)
	}
	after, err := os.ReadFile(layout.UserSettings(userHome))
	if err != nil || string(data) != string(after) {
		t.Fatal("reloading rewrote migrated settings")
	}
}

func TestAPIKeysAreScopedByCloudUserAndUpserted(t *testing.T) {
	host := t.TempDir()
	t.Setenv("HOME", host)
	makeProviders := func(key string) []Provider {
		return []Provider{{ProviderName: "same", OpenaiApiKey: key, ModelList: []Model{{ModelName: "same-model"}}}}
	}
	first := layout.Root(filepath.Join(host, "laxcode", "first", "agenthome"))
	second := layout.Root(filepath.Join(host, "laxcode", "second", "agenthome"))
	for _, entry := range []struct{ dir, key string }{{first, "old"}, {second, "second-secret"}, {first, "replacement"}} {
		if err := SaveProviderAPIKeys(entry.dir, makeProviders(entry.key)); err != nil {
			t.Fatal(err)
		}
	}
	// The scope must stay stable when the staged home is finally created.
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		p := makeProviders("")
		if err := LoadProviderAPIKeys(dir, p); err != nil {
			t.Fatal(err)
		}
		want := "replacement"
		if dir == second {
			want = "second-secret"
		}
		if p[0].OpenaiApiKey != want {
			t.Fatal("cloud users share or lost API keys")
		}
	}
	db, err := openConfigs(false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var key string
	if err := db.QueryRow(`SELECT api_key FROM api_keys WHERE user_id='first' AND provider='same' AND model='same-model'`).Scan(&key); err != nil || key != "replacement" {
		t.Fatalf("user:provider:model upsert failed: %v", err)
	}
	unknown := makeProviders("")
	if err := LoadProviderAPIKeys(layout.Root(filepath.Join(host, "laxcode", "other", "agenthome")), unknown); err != nil || unknown[0].OpenaiApiKey != "" {
		t.Fatal("unknown cloud user borrowed another user's key")
	}
	if err := SaveProviderAPIKeys(first, makeProviders("")); err != nil {
		t.Fatal(err)
	}
	cleared := makeProviders("")
	if err := LoadProviderAPIKeys(first, cleared); err != nil || cleared[0].OpenaiApiKey != "" {
		t.Fatal("an empty replacement retained the stale key")
	}
}

func TestDefaultLocalCredentialsSurviveHomeRelocation(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	t.Setenv("HOME", first)
	providers := []Provider{{ProviderName: "p", OpenaiApiKey: "portable-key", OpenaiBaseUrl: "https://example.com/v1", ModelList: []Model{{ModelName: "m"}}}}
	if err := SaveProviderAPIKeys(layout.Root(first), providers); err != nil {
		t.Fatal(err)
	}
	database, err := os.ReadFile(layout.ConfigsDB(first))
	if err != nil {
		t.Fatal(err)
	}
	writeSettings(t, second, `{"provider_list":[{"provider_name":"p","openai_base_url":"https://example.com/v1","model_list":[{"model_name":"m"}]}]}`)
	if err := os.WriteFile(layout.ConfigsDB(second), database, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", second)
	conf, err := LoadSettings(second, nil)
	if err != nil || conf.ProviderList[0].OpenaiApiKey != "portable-key" {
		t.Fatalf("copied local credentials cannot be reloaded: %v", err)
	}
}

func TestCloudTemplatesAndOAuthAreNotMigrated(t *testing.T) {
	host := t.TempDir()
	t.Setenv("HOME", host)
	template := `{"provider_list":[{"provider_name":"p","openai_api_key":"template-secret","openai_base_url":"https://example.com/v1","model_list":[{"model_name":"m"}]}]}`
	writeSettings(t, host, template)
	for _, load := range []func() (Conf, error){
		func() (Conf, error) { return Load(host, nil) },
		func() (Conf, error) { return LoadSettingsFromDir(layout.Root(host), nil) },
	} {
		conf, err := load()
		if err != nil || conf.ProviderList[0].OpenaiApiKey != "template-secret" {
			t.Fatalf("template credentials unavailable: %v", err)
		}
		if data, err := os.ReadFile(layout.UserSettings(host)); err != nil || string(data) != template {
			t.Fatal("cloud template was modified")
		}
	}
	if _, err := os.Stat(layout.ConfigsDB(host)); !os.IsNotExist(err) {
		t.Fatalf("template loading created configs database: %v", err)
	}
	oauthHome := t.TempDir()
	oauth := `{"provider_list":[{"provider_name":"oauth","auth_type":"oauth","credential_ref":"existing","model_list":[{"model_name":"m"}]}]}`
	writeSettings(t, oauthHome, oauth)
	conf, err := LoadSettings(oauthHome, nil)
	if err != nil || conf.ProviderList[0].AuthType != "oauth" {
		t.Fatalf("OAuth config changed: %v", err)
	}
	if data, err := os.ReadFile(layout.UserSettings(oauthHome)); err != nil || string(data) != oauth {
		t.Fatal("OAuth settings were rewritten")
	}
	if _, err := os.Stat(layout.ConfigsDB(host)); !os.IsNotExist(err) {
		t.Fatal("OAuth-only load touched the API-key database")
	}
}

func TestFailedAPIKeyMigrationKeepsPlaintextForRetry(t *testing.T) {
	host, userHome := t.TempDir(), t.TempDir()
	t.Setenv("HOME", host)
	before := `{"provider_list":[{"provider_name":"p","openai_api_key":"do-not-lose","model_list":[{"model_name":"m"}]}]}`
	writeSettings(t, userHome, before)
	if err := os.MkdirAll(layout.ConfigsDB(host), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSettings(userHome, nil); err == nil {
		t.Fatal("database failure was ignored")
	}
	data, err := os.ReadFile(layout.UserSettings(userHome))
	if err != nil || string(data) != before {
		t.Fatal("failed migration removed the original key")
	}
	if err := os.Remove(layout.ConfigsDB(host)); err != nil {
		t.Fatal(err)
	}
	conf, err := LoadSettings(userHome, nil)
	if err != nil || conf.ProviderList[0].OpenaiApiKey != "do-not-lose" {
		t.Fatalf("migration cannot be retried: %v", err)
	}
	// JSON serialization used by settings writers must not mutate live credentials.
	encoded, err := json.Marshal(WithoutProviderAPIKeys(conf.ProviderList))
	if err != nil || strings.Contains(string(encoded), "do-not-lose") || conf.ProviderList[0].OpenaiApiKey != "do-not-lose" {
		t.Fatal("stripping JSON keys changed the runtime provider")
	}
}
