package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

type WriteFileTool struct {
	WorkDir    string
	WriteRoots []string
	// FS 是沙箱文件读写端口，经构造注入；实现见 infrastructure/workfs。
	FS WorkFS
}

func NewWriteFileTool(workDir string, workFS WorkFS, writeRoots ...string) *WriteFileTool {
	return &WriteFileTool{WorkDir: workDir, WriteRoots: append([]string(nil), writeRoots...), FS: workFS}
}

func (w *WriteFileTool) AfterExecInfo(message json.RawMessage) string {
	return ""
}

func (w *WriteFileTool) BeforeExecInfo(args json.RawMessage) string {
	argsMap := make(map[string]string)
	if err := json.Unmarshal(args, &argsMap); err != nil {
		return ToolWriteFile + "()"
	}
	path, ok := argsMap["path"]
	if !ok {
		return ToolWriteFile + "()"
	}

	return fmt.Sprintf("%s(%s)", ToolWriteFile, path)
}

func (w *WriteFileTool) Name() string {
	return ToolWriteFile
}

func (w *WriteFileTool) Definition() sharedkernel.ToolDefinition {
	return sharedkernel.ToolDefinition{
		Name:        w.Name(),
		Description: "写入完整文件内容，创建新文件或覆写已有文件。相对路径严格限制在工作目录内；Plan Mode 明确提供的规划目录可使用绝对路径。全局 skills 等只读目录不可写；若父目录不存在会自动创建",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "工作目录内的相对路径，或 Plan Mode 明确提供的规划目录绝对路径",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "要写入的完整文件内容",
				},
			},
			"required": []string{"path", "content"},
		},
	}
}

func (w *WriteFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	argsMap := make(map[string]string)
	if err := json.Unmarshal(args, &argsMap); err != nil {
		return "", NewErrorWithPrompt(&ParamError{}, err)
	}

	path, ok := argsMap["path"]
	if !ok || strings.TrimSpace(path) == "" {
		return "", NewErrorWithPrompt(&ParamError{}, errors.New("path required"))
	}

	content, ok := argsMap["content"]
	if !ok {
		return "", NewErrorWithPrompt(&ParamError{}, errors.New("content required"))
	}

	target, displayRoot, err := resolveWriteTarget(w.WorkDir, w.WriteRoots, path)
	if err != nil {
		return "", NewErrorWithPrompt(&FilePathError{}, err)
	}

	// 写入沙箱内目标路径，父目录不存在时由端口实现自动创建
	if err := w.FS.WriteFile(target, []byte(content)); err != nil {
		return "", NewErrorWithPrompt(&FileIOError{}, err)
	}

	// 返回相对工作目录的路径，便于模型确认
	rel, err := filepath.Rel(displayRoot, target)
	if err != nil {
		rel = target
	}

	return fmt.Sprintf("内容成功写入文件：%s", filepath.ToSlash(rel)), nil
}
