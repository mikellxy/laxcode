package config

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// WriteCatalogAtomic publishes platform settings without CLI options or user preferences.
func WriteCatalogAtomic(path string, conf Conf) error {
	content, err := json.Marshal(fileConf{Model: conf.Model, CompactionModel: conf.CompactionModel, ProviderList: conf.ProviderList})
	if err != nil {
		return err
	}
	return WriteSettingsAtomic(path, content)
}

// WriteSettingsAtomic publishes a complete settings document with mode 0600.
func WriteSettingsAtomic(path string, content []byte) error {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	return WriteSettingsRootAtomic(root, filepath.Base(path), content)
}

// WriteSettingsRootAtomic keeps host writes inside a previously opened user root.
func WriteSettingsRootAtomic(root *os.Root, path string, content []byte) (err error) {
	dir, err := root.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	name := ".settings-" + rand.Text() + ".tmp"
	temp, err := dir.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create temporary settings: %w", err)
	}
	defer func() {
		_ = temp.Close()
		if err != nil {
			_ = dir.Remove(name)
		}
	}()
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		if err = temp.Chown(1000, 1000); err != nil {
			return fmt.Errorf("set settings ownership: %w", err)
		}
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
	if err = dir.Rename(name, filepath.Base(path)); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}
	if handle, err := dir.Open("."); err == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
	return nil
}
