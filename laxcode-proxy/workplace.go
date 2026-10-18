package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Called under the user's lifecycle lock. Only a completely initialized directory is published.
func initializeWorkplace(base, id string) (string, error) {
	if err := os.MkdirAll(base, 0700); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return "", err
	}
	defer root.Close()
	target := filepath.Join(base, id)
	if info, err := root.Lstat(id); err == nil {
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return "", errors.New("workplace must be a real directory")
		}
		if _, err := root.Stat(filepath.Join(id, ".initialized")); err != nil {
			return "", fmt.Errorf("workplace is not initialized: %w", err)
		}
		return target, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	stage, err := os.MkdirTemp(base, ".init-"+id+"-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	for _, dir := range []string{"agenthome/.laxcode", "tmp"} {
		if err := os.MkdirAll(filepath.Join(stage, dir), 0700); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(stage, ".initialized"), []byte("v2\n"), 0600); err != nil {
		return "", err
	}
	// Workload runs as UID/GID 1000; the host proxy owns only provisioning.
	if os.Geteuid() == 0 {
		if err := filepath.WalkDir(stage, func(p string, _ fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			return os.Chown(p, 1000, 1000)
		}); err != nil {
			return "", err
		}
	}
	if err := root.Rename(filepath.Base(stage), id); err != nil {
		return "", err
	}
	f, err := os.Open(base)
	if err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	return target, nil
}
