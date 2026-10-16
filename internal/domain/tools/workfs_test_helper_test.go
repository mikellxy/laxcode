package tools

import (
	"github.com/mikellxy/laxcode/internal/infrastructure/workfs"
	"path/filepath"
	"testing"
)

func newTestFS(t *testing.T) *workfs.FS {
	t.Helper()
	fs, err := workfs.NewRoot(filepath.Dir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	return fs
}
