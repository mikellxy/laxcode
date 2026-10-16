package config

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	_ "modernc.org/sqlite"
)

// configurationOwner is derived from the server-selected directory, never from JSON.
func configurationOwner(configDir string) (string, error) {
	hostHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	canonical := func(path string) (string, error) {
		path, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		// Cloud initialization saves credentials before publishing agenthome.
		parent := path
		for {
			real, err := filepath.EvalSymlinks(parent)
			if err == nil {
				rel, err := filepath.Rel(parent, path)
				return filepath.Join(real, rel), err
			}
			if !errors.Is(err, os.ErrNotExist) || filepath.Dir(parent) == parent {
				return "", err
			}
			parent = filepath.Dir(parent)
		}
	}
	root, err := canonical(filepath.Join(hostHome, "laxcode"))
	if err != nil {
		return "", err
	}
	dir, err := canonical(configDir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return "", err
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 3 && parts[0] != ".." && parts[1] == "agenthome" && parts[2] == layout.RootDirName {
		return parts[0], nil
	}
	// Preserve legacy template key namespaces so upgrades do not lose stored keys.
	localDir, err := canonical(layout.Root(hostHome))
	if err != nil {
		return "", err
	}
	if dir == localDir {
		return "local", nil
	}
	return "local:" + dir, nil
}

func openConfigs(create bool) (*sql.DB, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	path := layout.ConfigsDB(home)
	if !create {
		if _, err := os.Stat(path); err != nil {
			return nil, err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		err = file.Chmod(0600)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
	}
	dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if create {
		_, err = db.Exec(`CREATE TABLE IF NOT EXISTS api_keys (
			user_id TEXT NOT NULL, provider TEXT NOT NULL, model TEXT NOT NULL,
			api_key TEXT NOT NULL, PRIMARY KEY (user_id, provider, model)
		)`)
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize configs database: %w", err)
		}
	}
	return db, nil
}

// SaveProviderAPIKeys upserts API-key models, skipping legacy subscription entries.
func SaveProviderAPIKeys(configDir string, providers []Provider) error {
	var apiModels bool
	for _, p := range providers {
		apiModels = apiModels || (p.AuthType != "oauth" && len(p.ModelList) > 0)
	}
	if !apiModels {
		return nil
	}
	owner, err := configurationOwner(configDir)
	if err != nil {
		return err
	}
	db, err := openConfigs(true)
	if err != nil {
		return fmt.Errorf("open configs database: %w", err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range providers {
		if p.AuthType == "oauth" {
			continue
		}
		for _, m := range p.ModelList {
			if _, err := tx.Exec(`INSERT INTO api_keys (user_id, provider, model, api_key) VALUES (?, ?, ?, ?)
				ON CONFLICT (user_id, provider, model) DO UPDATE SET api_key=excluded.api_key`,
				owner, p.ProviderName, m.ModelName, p.OpenaiApiKey); err != nil {
				return fmt.Errorf("save model API key: %w", err)
			}
		}
	}
	return tx.Commit()
}

// LoadProviderAPIKeys fills missing keys only; plaintext templates and CLI inputs win.
func LoadProviderAPIKeys(configDir string, providers []Provider) error {
	var missing bool
	for _, p := range providers {
		missing = missing || (p.AuthType != "oauth" && p.OpenaiApiKey == "" && len(p.ModelList) > 0)
	}
	if !missing {
		return nil
	}
	owner, err := configurationOwner(configDir)
	if err != nil {
		return err
	}
	db, err := openConfigs(false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open configs database: %w", err)
	}
	defer db.Close()
	for i := range providers {
		p := &providers[i]
		if p.AuthType == "oauth" || p.OpenaiApiKey != "" {
			continue
		}
		for _, m := range p.ModelList {
			var key string
			err := db.QueryRow(`SELECT api_key FROM api_keys WHERE user_id=? AND provider=? AND model=?`, owner, p.ProviderName, m.ModelName).Scan(&key)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return fmt.Errorf("load model API key: %w", err)
			}
			if p.OpenaiApiKey != "" && p.OpenaiApiKey != key {
				return fmt.Errorf("provider %q has conflicting model API keys", p.ProviderName)
			}
			p.OpenaiApiKey = key
		}
	}
	return nil
}

func WithoutProviderAPIKeys(providers []Provider) []Provider {
	result := make([]Provider, len(providers))
	copy(result, providers)
	for i := range result {
		if result[i].AuthType != "oauth" {
			result[i].OpenaiApiKey = ""
		}
	}
	return result
}

// migrateAPIKeys commits credentials before removing plaintext, so retrying is safe.
func migrateAPIKeys(configDir string, providers []Provider) error {
	path := layout.SettingsFile(configDir)
	doc, err := ReadSettings(path)
	if err != nil {
		return err
	}
	if _, ok := doc["provider_list"]; !ok {
		return nil
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(doc["provider_list"], &entries); err != nil {
		return err
	}
	var legacy bool
	for _, entry := range entries {
		var authType string
		for key, value := range entry {
			if strings.EqualFold(key, "auth_type") {
				_ = json.Unmarshal(value, &authType)
			}
		}
		if authType != "oauth" {
			for key := range entry {
				if strings.EqualFold(key, "openai_api_key") {
					legacy = true
					delete(entry, key)
				}
			}
		}
	}
	if !legacy {
		return nil
	}
	for _, p := range providers {
		if p.AuthType != "oauth" && p.OpenaiApiKey != "" && len(p.ModelList) == 0 {
			return fmt.Errorf("cannot migrate API key for provider %q without models", p.ProviderName)
		}
	}
	if err := SaveProviderAPIKeys(configDir, providers); err != nil {
		return err
	}
	doc["provider_list"], err = json.Marshal(entries)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return WriteSettingsAtomic(path, append(data, '\n'))
}
