package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
)

// resolveReadTarget keeps relative paths rooted at workDir and permits absolute
// paths only when they are inside an explicitly configured read-only root.
func resolveReadTarget(workDir string, readRoots []string, input string) (requested, allowedRoot string, err error) {
	workDir, err = filepath.Abs(workDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve work dir: %w", err)
	}
	if input == "" {
		return workDir, workDir, nil
	}
	if !filepath.IsAbs(input) {
		requested = filepath.Clean(filepath.Join(workDir, input))
		if !withinSearchRoot(workDir, requested) {
			return "", "", fmt.Errorf("path %q escapes working directory", input)
		}
		return requested, workDir, nil
	}

	requested = filepath.Clean(input)
	if withinSearchRoot(workDir, requested) {
		return requested, workDir, nil
	}
	for _, root := range readRoots {
		root, rootErr := filepath.Abs(root)
		if rootErr == nil && withinSearchRoot(root, requested) {
			return requested, root, nil
		}
	}
	return "", "", fmt.Errorf("absolute path %q is outside configured read-only roots", input)
}

func ensureRealPathWithin(root, target string) error {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve read root: %w", err)
	}
	targetReal, err := filepath.EvalSymlinks(target)
	if err != nil {
		return err
	}
	if !withinSearchRoot(rootReal, targetReal) {
		return fmt.Errorf("path resolves outside configured read root")
	}
	return nil
}

// resolveWriteTarget permits the normal workdir plus explicitly configured
// writable roots. It resolves existing symlinks (or the deepest existing
// parent for a new file) so a link cannot redirect a write outside that root.
func resolveWriteTarget(workDir string, writeRoots []string, input string) (target, allowedRoot string, err error) {
	if filepath.IsAbs(input) {
		target = filepath.Clean(input)
		for _, root := range writeRoots {
			root, rootErr := filepath.Abs(root)
			if rootErr == nil && withinSearchRoot(root, target) {
				allowedRoot = root
				break
			}
		}
		if allowedRoot == "" {
			return "", "", fmt.Errorf("absolute path %q is outside configured write roots", input)
		}
	} else {
		target, allowedRoot, err = resolveReadTarget(workDir, nil, input)
		if err != nil {
			return "", "", err
		}
	}
	rootReal, err := filepath.EvalSymlinks(allowedRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve write root: %w", err)
	}
	probe := target
	for {
		probeReal, probeErr := filepath.EvalSymlinks(probe)
		if probeErr == nil {
			if !withinSearchRoot(rootReal, probeReal) {
				return "", "", fmt.Errorf("path resolves outside configured write root")
			}
			return target, allowedRoot, nil
		}
		if !errors.Is(probeErr, fs.ErrNotExist) {
			return "", "", probeErr
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", "", probeErr
		}
		probe = parent
	}
}
