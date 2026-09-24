// Package skillstore persists complete global Skill packages beneath one fixed
// root. All caller paths are interpreted through os.Root, and package commits
// are staged beside the destination before a directory rename.
package skillstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/mikellxy/laxcode/internal/domain/tools"
)

const (
	dirPerm        = 0o700
	filePerm       = 0o600
	executablePerm = 0o700
	tempDir        = ".tmp"
)

var ErrRevisionChanged = errors.New("skill package changed after preflight")

type Store struct {
	root string
	mu   sync.Mutex
}

func New(root string) *Store { return &Store{root: root} }

var _ tools.SkillPackageStore = (*Store)(nil)

func (s *Store) Load(ctx context.Context, name string) (tools.SkillPackageSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.openRoot()
	if err != nil {
		return tools.SkillPackageSnapshot{}, err
	}
	defer root.Close()
	return loadPackage(ctx, root, name)
}

func (s *Store) Create(ctx context.Context, name string, files []tools.SkillPackageFile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := root.Lstat(name); err == nil {
		return fs.ErrExist
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	stage, err := writeStage(ctx, root, files)
	if err != nil {
		return err
	}
	defer root.RemoveAll(stage)
	if err := root.Rename(stage, name); err != nil {
		return fmt.Errorf("publish staged skill: %w", err)
	}
	return nil
}

func (s *Store) Replace(ctx context.Context, name, expectedRevision string, files []tools.SkillPackageFile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	current, err := loadPackage(ctx, root, name)
	if err != nil {
		return err
	}
	if current.Revision != expectedRevision {
		return ErrRevisionChanged
	}
	stage, err := writeStage(ctx, root, files)
	if err != nil {
		return err
	}
	defer root.RemoveAll(stage)
	suffix, err := randomSuffix()
	if err != nil {
		return err
	}
	backup := path.Join(tempDir, "backup-"+suffix)
	if err := root.Rename(name, backup); err != nil {
		return fmt.Errorf("move current skill to backup: %w", err)
	}
	if err := root.Rename(stage, name); err != nil {
		rollbackErr := root.Rename(backup, name)
		return errors.Join(fmt.Errorf("publish updated skill: %w", err), rollbackErr)
	}
	// The new package is already committed. Backup cleanup is best effort so a
	// cleanup failure cannot be reported as if the update itself had failed.
	_ = root.RemoveAll(backup)
	return nil
}

func (s *Store) openRoot() (*os.Root, error) {
	if err := os.MkdirAll(s.root, dirPerm); err != nil {
		return nil, fmt.Errorf("create skills root: %w", err)
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, fmt.Errorf("open skills root: %w", err)
	}
	if err := root.MkdirAll(tempDir, dirPerm); err != nil {
		root.Close()
		return nil, fmt.Errorf("create skill staging directory: %w", err)
	}
	return root, nil
}

func loadPackage(ctx context.Context, root *os.Root, name string) (tools.SkillPackageSnapshot, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return tools.SkillPackageSnapshot{}, err
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return tools.SkillPackageSnapshot{}, fmt.Errorf("skill %q is not a regular directory", name)
	}
	var files []tools.SkillPackageFile
	err = fs.WalkDir(root.FS(), name, func(entryPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entryPath == name {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("skill package contains symbolic link %q", entryPath)
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("skill package contains non-regular file %q", entryPath)
		}
		content, err := root.ReadFile(entryPath)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(entryPath, name+"/")
		files = append(files, tools.SkillPackageFile{
			Path: rel, Content: content, Executable: info.Mode().Perm()&0o111 != 0,
		})
		return nil
	})
	if err != nil {
		return tools.SkillPackageSnapshot{}, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return tools.SkillPackageSnapshot{Files: files, Revision: packageRevision(files)}, nil
}

func writeStage(ctx context.Context, root *os.Root, files []tools.SkillPackageFile) (string, error) {
	suffix, err := randomSuffix()
	if err != nil {
		return "", err
	}
	stage := path.Join(tempDir, "stage-"+suffix)
	if err := root.Mkdir(stage, dirPerm); err != nil {
		return "", fmt.Errorf("create skill stage: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = root.RemoveAll(stage)
		}
	}()
	ordered := append([]tools.SkillPackageFile(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	for _, file := range ordered {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !fs.ValidPath(file.Path) || file.Path == "." {
			return "", fmt.Errorf("invalid skill package path %q", file.Path)
		}
		target := path.Join(stage, file.Path)
		if parent := path.Dir(target); parent != stage {
			if err := root.MkdirAll(parent, dirPerm); err != nil {
				return "", fmt.Errorf("create skill directory for %q: %w", file.Path, err)
			}
		}
		perm := fs.FileMode(filePerm)
		if file.Executable {
			perm = executablePerm
		}
		if err := root.WriteFile(target, file.Content, perm); err != nil {
			return "", fmt.Errorf("write skill file %q: %w", file.Path, err)
		}
	}
	cleanup = false
	return stage, nil
}

func packageRevision(files []tools.SkillPackageFile) string {
	ordered := append([]tools.SkillPackageFile(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	h := sha256.New()
	var size [8]byte
	for _, file := range ordered {
		binary.BigEndian.PutUint64(size[:], uint64(len(file.Path)))
		h.Write(size[:])
		h.Write([]byte(file.Path))
		if file.Executable {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}
		binary.BigEndian.PutUint64(size[:], uint64(len(file.Content)))
		h.Write(size[:])
		h.Write(file.Content)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func randomSuffix() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate skill staging name: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
