package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
)

// OutsideSandboxError 表示路径语法合法、但解析后落在允许的根之外。与参数类
// 错误相区分：这类调用可经 human-in-the-loop 人工确认后放行一次（授权精确
// 匹配 Requested，见 ToolConfirmation.Grants），其余失败仍按普通错误处理。
type OutsideSandboxError struct {
	// Requested 是解析后的目标绝对路径（Clean 过；write 为目标文件路径）。
	Requested string
	Reason    string
}

func (e *OutsideSandboxError) Error() string { return e.Reason }

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
			return "", "", &OutsideSandboxError{Requested: requested, Reason: fmt.Sprintf("path %q escapes working directory", input)}
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
	return "", "", &OutsideSandboxError{Requested: requested, Reason: fmt.Sprintf("absolute path %q is outside configured read-only roots", input)}
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

// realPath 解析 target 的真实路径（EvalSymlinks）：目标已存在时直接解析；
// 不存在（write 新文件）时对最深存在父目录解析后拼回剩余段。供确认文案
// 展示“实际将访问的位置”，防止 symlink 伪装目标。
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

// realPathWithinRoots 报告 real（已解析的真实路径）是否落在 workDir 或任一
// root 之内。root 先做 EvalSymlinks 再比较（macOS /tmp → /private/tmp），
// 与 realpath 口径一致；解析失败的 root 跳过。
func realPathWithinRoots(real, workDir string, roots []string) bool {
	if root, err := filepath.Abs(workDir); err == nil {
		if rootReal, err := filepath.EvalSymlinks(root); err == nil && withinSearchRoot(rootReal, real) {
			return true
		}
	}
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if rootReal, err := filepath.EvalSymlinks(abs); err == nil && withinSearchRoot(rootReal, real) {
			return true
		}
	}
	return false
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
			return "", "", &OutsideSandboxError{Requested: target, Reason: fmt.Sprintf("absolute path %q is outside configured write roots", input)}
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
				return "", "", &OutsideSandboxError{Requested: target, Reason: "path resolves outside configured write root"}
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
