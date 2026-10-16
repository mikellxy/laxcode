package reactservice

import (
	"github.com/mikellxy/laxcode/internal/infrastructure/workfs"
	"path/filepath"
	"testing"
)

func newTestFS(t *testing.T, roots ...string) *workfs.FS {
	t.Helper()
	root := filepath.Dir(t.TempDir())
	if len(roots) > 0 {
		root = roots[0]
	}
	fs, err := workfs.NewRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	return fs
}
