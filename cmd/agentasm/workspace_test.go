package agentasm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func assembleTest(t *testing.T, ctx context.Context, in Input) (*Assembled, error) {
	t.Helper()
	if in.UserRoot == "" {
		root := filepath.Dir(in.HomeDir)
		for {
			rel, err := filepath.Rel(root, in.WorkDir)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				break
			}
			root = filepath.Dir(root)
		}
		in.UserRoot = root
		in.TempDir = filepath.Join(root, "tmp")
		if err := os.MkdirAll(in.TempDir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return Assemble(ctx, in)
}
