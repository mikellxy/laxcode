// Package workfs 实现 domain/tools.WorkFS：文件类工具（read/write/edit）
// 唯一的 os 触点。领域层只持有端口，故沙箱内文件的真实读写、父目录创建与
// 权限位全部收口于此，os 错误原样透传（已满足 errors.Is(err, fs.ErrNotExist)）。
//
// 本地模式依赖领域层校验；云端 NewRoot 在实际文件操作时强制限制用户根，
// 避免校验后符号链接被替换导致越界。
package workfs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// 沙箱内文件的权限位：目录 0o755、文件 0o644，与 umask 共同决定最终权限。
const (
	dirPerm  = 0o755
	filePerm = 0o644
)

// FS implements WorkFS; cloud instances retain a root directory handle.
type FS struct {
	root     *os.Root
	rootPath string
}

// New 返回本地文件系统实现。
func New() *FS { return &FS{} }

// OpenRead 打开 absPath 供顺序读取，调用方负责 Close。
func (f FS) OpenRead(absPath string) (io.ReadCloser, error) {
	if f.root != nil {
		rel, err := f.relative(absPath)
		if err != nil {
			return nil, err
		}
		return f.root.Open(rel)
	}
	return os.Open(absPath)
}

// ReadFile 一次性读取 absPath 的全部内容。
func (f FS) ReadFile(absPath string) ([]byte, error) {
	if f.root != nil {
		rel, err := f.relative(absPath)
		if err != nil {
			return nil, err
		}
		return f.root.ReadFile(rel)
	}
	return os.ReadFile(absPath)
}

// WriteFile 写入 absPath，父目录不存在时自动创建。
func (f FS) WriteFile(absPath string, content []byte) error {
	if f.root != nil {
		rel, err := f.relative(absPath)
		if err != nil {
			return err
		}
		if err := f.root.MkdirAll(filepath.Dir(rel), dirPerm); err != nil {
			return err
		}
		return f.root.WriteFile(rel, content, filePerm)
	}
	if err := os.MkdirAll(filepath.Dir(absPath), dirPerm); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	return os.WriteFile(absPath, content, filePerm)
}

// NewRoot holds the directory handle for the lifetime of a cloud agent.
func NewRoot(path string) (*FS, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &FS{root: root, rootPath: path}, nil
}
func (f *FS) Close() error {
	if f.root != nil {
		return f.root.Close()
	}
	return nil
}
func (f FS) relative(path string) (string, error) {
	rel, err := filepath.Rel(f.rootPath, path)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path outside user root")
	}
	return rel, nil
}
