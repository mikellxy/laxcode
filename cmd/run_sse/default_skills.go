package run_sse

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// An existing directory (including one from older releases) belongs to the user.
// Publish the complete seed atomically so interrupted starts can safely retry.
func initializeDefaultSkills(source, dest string) error {
	info, err := os.Lstat(dest)
	exists := err == nil
	if exists && !info.IsDir() {
		return errors.New("skills must be a real directory")
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if source == "" {
		return os.MkdirAll(dest, 0700)
	}
	marker := filepath.Join(filepath.Dir(dest), ".skills-initialized")
	if _, err := os.Lstat(marker); err == nil {
		return os.MkdirAll(dest, 0700)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if !exists {
		stage, err := os.MkdirTemp(filepath.Dir(dest), ".skills-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(stage)
		if err := copySkills(source, stage); err != nil {
			return err
		}
		if err := os.Rename(stage, dest); err != nil {
			return err
		}
	}
	// Keep the marker outside skills so deleting the whole directory does not re-seed it.
	return os.WriteFile(marker, []byte("v1\n"), 0600)
}

func copySkills(source, dest string) error {
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer sourceRoot.Close()
	return fs.WalkDir(sourceRoot.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("skill symlink is unsupported: %s", name)
		}
		target := filepath.Join(dest, name)
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported skill file %s", name)
		}
		b, err := sourceRoot.ReadFile(name)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0600|info.Mode().Perm()&0111)
	})
}
