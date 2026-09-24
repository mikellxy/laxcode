package run_sse

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

// CodeInstanceGuard owns the process-wide browser code-mode lock. The same
// file also publishes a small, versioned record for web.sh. A stale record is
// harmless: ownership is determined by the OS lock, never by file contents.
type CodeInstanceGuard struct {
	file *os.File
}

// AcquireCodeInstanceGuard tries to become the only -sse -code process for the
// current user. The descriptor must remain open for the entire server lifetime.
func AcquireCodeInstanceGuard(homeDir string) (*CodeInstanceGuard, error) {
	root := layout.Root(homeDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create LaxCode data directory: %w", err)
	}
	path := layout.SSECodeInstance(homeDir)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open SSE code instance file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure SSE code instance file: %w", err)
	}
	locked, err := tryExclusiveFileLock(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock SSE code instance file: %w", err)
	}
	if !locked {
		state, _ := io.ReadAll(file)
		_ = file.Close()
		detail := strings.TrimSpace(string(state))
		if detail == "" {
			detail = path
		}
		return nil, fmt.Errorf("another LaxCode SSE code server is already running (%s)", detail)
	}
	return &CodeInstanceGuard{file: file}, nil
}

// Publish replaces any stale record only after the listener has successfully
// bound. It deliberately writes through the locked descriptor: renaming or
// unlinking this file while locked would allow another process to lock a new
// inode at the same path.
func (g *CodeInstanceGuard) Publish(url string) error {
	if g == nil || g.file == nil {
		return fmt.Errorf("SSE code instance guard is not held")
	}
	if err := g.file.Truncate(0); err != nil {
		return fmt.Errorf("truncate SSE code instance file: %w", err)
	}
	if _, err := g.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind SSE code instance file: %w", err)
	}
	if _, err := fmt.Fprintf(g.file, "v1\t%d\t%s\n", os.Getpid(), url); err != nil {
		return fmt.Errorf("publish SSE code server address: %w", err)
	}
	if err := g.file.Sync(); err != nil {
		return fmt.Errorf("sync SSE code instance file: %w", err)
	}
	return nil
}

// Close releases the advisory lock. The file is intentionally retained so a
// concurrent launcher can never switch to a different inode during handoff.
func (g *CodeInstanceGuard) Close() error {
	if g == nil || g.file == nil {
		return nil
	}
	file := g.file
	g.file = nil
	unlockErr := unlockFile(file)
	closeErr := file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
