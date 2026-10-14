package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

// Track successful writes even when a later edit in the same call fails.
type changedFilesFS struct {
	WorkFS
	files []sharedkernel.ChangedFile
}

func (f *changedFilesFS) WriteFile(path string, content []byte) error {
	reader, err := f.WorkFS.OpenRead(path)
	created := errors.Is(err, fs.ErrNotExist)
	if reader != nil {
		_ = reader.Close()
	}
	if err := f.WorkFS.WriteFile(path, content); err != nil {
		return err
	}
	for _, file := range f.files {
		if file.Path == path {
			return nil
		}
	}
	f.files = append(f.files, sharedkernel.ChangedFile{Path: path, Created: created})
	return nil
}

func (w *WriteFileTool) ExecuteResult(ctx context.Context, args json.RawMessage) *sharedkernel.ToolResult {
	copy := *w
	if w.UserRoot == "" {
		output, err := copy.Execute(ctx, args)
		return &sharedkernel.ToolResult{Output: output, Error: err}
	}
	tracked := &changedFilesFS{WorkFS: w.FS}
	copy.FS = tracked
	output, err := copy.Execute(ctx, args)
	return &sharedkernel.ToolResult{Output: output, Error: err, ChangedFiles: tracked.files}
}

func (e *EditFileTool) ExecuteResult(ctx context.Context, args json.RawMessage) *sharedkernel.ToolResult {
	copy := *e
	if e.UserRoot == "" {
		output, err := copy.Execute(ctx, args)
		return &sharedkernel.ToolResult{Output: output, Error: err}
	}
	tracked := &changedFilesFS{WorkFS: e.FS}
	copy.FS = tracked
	output, err := copy.Execute(ctx, args)
	return &sharedkernel.ToolResult{Output: output, Error: err, ChangedFiles: tracked.files}
}
