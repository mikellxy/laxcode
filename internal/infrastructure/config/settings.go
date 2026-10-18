package config

import (
	"fmt"
	"os"
	"path/filepath"
)

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
