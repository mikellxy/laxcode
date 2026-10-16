package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
)

// OutsideSandboxError 表示路径语法合法、但解析后落在允许的根之外。与参数类
// 错误相区分，越界访问直接拒绝。
type OutsideSandboxError struct {
	// Requested 是解析后的目标绝对路径（Clean 过；write 为目标文件路径）。
	Requested string
	Reason    string
}

func (e *OutsideSandboxError) Error() string { return e.Reason }

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

// realPath 解析 target 的真实路径（EvalSymlinks）：目标已存在时直接解析；
// 不存在（write 新文件）时对最深存在父目录解析后拼回剩余段，防止符号链接越界。
func realPath(target string) (string, error) {
	if real, err := filepath.EvalSymlinks(target); err == nil {
		return real, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	probe := target
	for {
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", fmt.Errorf("resolve real path of %q: no existing ancestor", target)
		}
		real, err := filepath.EvalSymlinks(parent)
		if err == nil {
			rel, relErr := filepath.Rel(parent, target)
			if relErr != nil {
				return "", relErr
			}
			return filepath.Join(real, rel), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		probe = parent
	}
}

// resolveWriteTarget validates existing symlinks and new-file parents within the user root.
func resolveWriteTarget(workDir, userRoot, input string) (target, root string, err error) {
	target, root, err = resolveUserTarget(workDir, userRoot, input)
	if err != nil {
		return "", "", err
	}
	if err = ValidateUserPath(root, target); err != nil {
		return "", "", err
	}
	return target, root, nil
}

func resolveUserTarget(workDir, userRoot, input string) (string, string, error) {
	root, err := filepath.Abs(userRoot)
	if err != nil {
		return "", "", err
	}
	target := input
	if !filepath.IsAbs(target) {
		target = filepath.Join(workDir, target)
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return "", "", err
	}
	if !withinSearchRoot(root, target) {
		return "", "", &OutsideSandboxError{Requested: target, Reason: "path is outside user root"}
	}
	return target, root, nil
}

// ValidateUserPath checks user-scoped working directories and explicit targets.
// File adapters additionally enforce this boundary during the actual operation.
func ValidateUserPath(userRoot, target string) error {
	if !filepath.IsAbs(userRoot) {
		return fmt.Errorf("absolute user root is required")
	}
	if target == "" {
		return fmt.Errorf("user-scoped path is required")
	}
	requested, root, err := resolveUserTarget(userRoot, userRoot, target)
	if err != nil {
		return err
	}
	real, err := realPath(requested)
	if err != nil {
		return err
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if !withinSearchRoot(rootReal, real) {
		return fmt.Errorf("path resolves outside user root")
	}
	return nil
}
