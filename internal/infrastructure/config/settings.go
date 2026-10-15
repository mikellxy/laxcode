package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReadSettings preserves unrelated fields and normalizes legacy model key casing.
func ReadSettings(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse settings: %w", err)
	}
	if document == nil {
		return nil, errors.New("settings must be a JSON object")
	}
	for key, value := range document {
		for _, canonical := range []string{"provider_list", "model"} {
			if key != canonical && strings.EqualFold(key, canonical) {
				if _, exists := document[canonical]; !exists {
					document[canonical] = value
				}
				delete(document, key)
			}
		}
	}
	return document, nil
}

// WriteSettingsAtomic publishes a complete settings document with mode 0600.
func WriteSettingsAtomic(path string, content []byte) (err error) {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary settings: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		if err != nil {
			_ = os.Remove(tempPath)
		}
	}()
	if err = temp.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary settings: %w", err)
	}
	if _, err = temp.Write(content); err != nil {
		return fmt.Errorf("write temporary settings: %w", err)
	}
	if err = temp.Sync(); err != nil {
		return fmt.Errorf("sync temporary settings: %w", err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("close temporary settings: %w", err)
	}
	if err = os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}
	if dirHandle, openErr := os.Open(dir); openErr == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	return nil
}
