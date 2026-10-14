package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/infrastructure/workfs"
)

func TestCloudFileToolsTrackActualWrites(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "demo")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	bounded, err := workfs.NewRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer bounded.Close()
	write := NewWriteFileTool(work, bounded)
	write.UserRoot = root
	reg := NewDefaultRegistry(nil)
	reg.Register(write)
	for i, created := range []bool{true, false} {
		result := reg.Execute(context.Background(), &sharedkernel.ToolCall{ID: "write", Name: ToolWriteFile, Arguments: json.RawMessage(`{"path":"a.txt","content":"alpha\nbeta\n"}`)})
		if result.IsError || len(result.ChangedFiles) != 1 || result.ChangedFiles[0].Created != created || result.ChangedFiles[0].Path != filepath.Join(work, "a.txt") {
			t.Fatalf("write %d: %+v", i, result)
		}
	}
	result := write.ExecuteResult(context.Background(), json.RawMessage(`{"path":"../../escape.txt","content":"x"}`))
	if result.Error == nil || len(result.ChangedFiles) != 0 {
		t.Fatalf("rejected write: %+v", result)
	}
	editFS := &changingEditFS{content: "alpha\nbeta\n"}
	edit := NewEditFileTool(work, editFS)
	edit.UserRoot = root
	result = edit.ExecuteResult(context.Background(), marshalEditArgs(t, "a.txt",
		map[string]string{"old_text": "alpha", "new_text": "A"},
		map[string]string{"old_text": "beta", "new_text": "B"}))
	if result.Error == nil || len(result.ChangedFiles) != 1 || result.ChangedFiles[0].Created {
		t.Fatalf("partial edit lost file: %+v", result)
	}
	result = edit.ExecuteResult(context.Background(), marshalEditArgs(t, "a.txt", map[string]string{"old_text": "missing", "new_text": "x"}))
	if result.Error == nil || len(result.ChangedFiles) != 0 {
		t.Fatalf("failed preflight: %+v", result)
	}
}
