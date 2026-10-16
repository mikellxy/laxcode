package run_sse

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
)

const maxProjectScanEntries = 100000
const projectScanTimeout = 5 * time.Second

var errChatFileTracking = errors.New("chat file tracking failed")

type projectFileState struct {
	size    int64
	modTime time.Time
}

// Metadata only: no file contents are copied or read, including large binary outputs.
func scanProjectFiles(ctx context.Context, root *os.Root) (map[string]projectFileState, error) {
	ctx, cancel := context.WithTimeout(ctx, projectScanTimeout)
	defer cancel()
	files := make(map[string]projectFileState)
	entries := 0
	err := fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Files can disappear while tools or external editors update the project.
		if errors.Is(walkErr, fs.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > maxProjectScanEntries {
			return fmt.Errorf("project scan exceeds %d entries", maxProjectScanEntries)
		}
		// These paths cannot be represented by the existing download API or database.
		if !fs.ValidPath(path) || strings.Contains(path, `\`) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path != "." && entry.Name() == ".git" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if path == "." {
				return nil
			}
			switch entry.Name() {
			case "node_modules", ".venv", "venv", "__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache":
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			files[path] = projectFileState{size: info.Size(), modTime: info.ModTime()}
		}
		return nil
	})
	return files, err
}

func diffProjectFiles(project string, before, after map[string]projectFileState) []session.ChatFile {
	var files []session.ChatFile
	for path, current := range after {
		previous, existed := before[path]
		if existed && previous.size == current.size && previous.modTime.Equal(current.modTime) {
			continue
		}
		kind := "modified"
		if !existed {
			kind = "created"
		}
		files = append(files, session.ChatFile{Path: project + "/" + path, ChangeType: kind})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

// Only a shared cloud project is serialized; local mode retains session-level locking.
func (s *server) lockChatProject(workDir string) (func(), bool) {
	if s.userRoot == "" {
		return func() {}, true
	}
	return s.projectLocks.TryLock(filepath.Clean(workDir))
}

// Runs once per HTTP chat/resume, so child agents and Bash outputs share one diff.
func (s *server) runWithFileTracking(ctx context.Context, workDir string, assembled *agentasm.Assembled,
	run func() (*sharedkernel.Message, error)) (msg *sharedkernel.Message, err error) {
	if s.userRoot == "" {
		return run()
	}
	cleanup := sync.OnceFunc(assembled.Cleanup)
	defer cleanup()
	defer func() {
		if errors.Is(err, errChatFileTracking) {
			slog.ErrorContext(context.WithoutCancel(ctx), "chat_file_tracking", "session_id", assembled.Session.ID, "error", err)
		}
	}()
	repo, ok := s.contextRepo.(session.ChatFilesRepository)
	if !ok {
		return nil, fmt.Errorf("%w: repository unavailable", errChatFileTracking)
	}
	project, openErr := filepath.Rel(s.userRoot, workDir)
	if openErr != nil || validateCloudProjectName(project) != nil {
		return nil, fmt.Errorf("%w: invalid project directory", errChatFileTracking)
	}
	user, openErr := os.OpenRoot(s.userRoot)
	if openErr != nil {
		return nil, fmt.Errorf("%w: %w", errChatFileTracking, openErr)
	}
	defer user.Close()
	info, openErr := user.Lstat(project)
	if openErr != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: project must be a real directory", errChatFileTracking)
	}
	root, openErr := user.OpenRoot(project)
	if openErr != nil {
		return nil, fmt.Errorf("%w: %w", errChatFileTracking, openErr)
	}
	defer root.Close()
	before, scanErr := scanProjectFiles(ctx, root)
	if scanErr != nil {
		return nil, fmt.Errorf("%w: initial scan: %w", errChatFileTracking, scanErr)
	}

	// Cleanup is idempotent and stops tool processes before the final scan.
	msg, err = run()
	cleanup()
	chatID := telemetry.ChatIDFromContext(ctx)
	committed := false
	for _, message := range assembled.Session.Messages {
		if chatID != "" && message.ChatID == chatID && message.Role == sharedkernel.RoleUser {
			committed = true
			break
		}
	}
	if !committed {
		return msg, err
	}
	// Disconnection cancels the run, but already generated files still need recording.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*projectScanTimeout)
	defer cancel()
	after, scanErr := scanProjectFiles(finishCtx, root)
	if scanErr == nil {
		if files := diffProjectFiles(project, before, after); len(files) > 0 {
			scanErr = repo.UpsertChatFiles(finishCtx, assembled.Session.ID, chatID, files)
		}
	}
	if scanErr != nil {
		err = errors.Join(err, fmt.Errorf("%w: final scan/persist: %w", errChatFileTracking, scanErr))
	}
	// shortcut: an abrupt server exit loses this request's in-memory baseline.
	return msg, err
}
