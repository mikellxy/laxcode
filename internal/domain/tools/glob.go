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

const globFileLimit = 100

type globArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

type GlobTool struct {
	WorkDir   string
	ReadRoots []string
	Runner    RipgrepRunner
}

func NewGlobTool(workDir string, runner RipgrepRunner, readRoots ...string) *GlobTool {
	return &GlobTool{WorkDir: workDir, ReadRoots: append([]string(nil), readRoots...), Runner: runner}
}

func (g *GlobTool) Name() string { return ToolGlob }

func (g *GlobTool) Definition() sharedkernel.ToolDefinition {
	return sharedkernel.ToolDefinition{
		Name:        g.Name(),
		Description: "按文件名 glob 模式快速查找文件，返回绝对路径。相对 path 限制在工作目录内；系统明确提供的只读目录可使用绝对路径。需要按名称定位文件时使用本工具，不要用 bash 或 find。支持 **/*.go、src/**/*.ts 等模式，最多返回 100 个文件；结果截断时请缩小 path 或使用更具体的 pattern。path 必须是目录，缺省为工作目录；需要多轮开放式搜索时使用 run_sub_agent。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "用于匹配文件路径的 glob 模式"},
				"path": map[string]any{
					"type":        "string",
					"description": "工作目录内的目录，或系统明确提供的只读绝对目录；不传则使用工作目录",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

func (g *GlobTool) BeforeExecInfo(raw json.RawMessage) string {
	var a globArgs
	if json.Unmarshal(raw, &a) != nil || a.Pattern == "" {
		return ToolGlob + "()"
	}
	return fmt.Sprintf("glob(pattern=%q, path=%q)", a.Pattern, a.Path)
}

func (*GlobTool) AfterExecInfo(json.RawMessage) string { return "" }

func (g *GlobTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	result := g.ExecuteResult(ctx, raw)
	return result.Output, result.Error
}

func (g *GlobTool) ExecuteResult(ctx context.Context, raw json.RawMessage) *sharedkernel.ToolResult {
	var a globArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return &sharedkernel.ToolResult{Error: NewErrorWithPrompt(&ParamError{}, err)}
	}
	if a.Pattern == "" {
		return &sharedkernel.ToolResult{Error: NewErrorWithPrompt(&ParamError{}, errors.New("pattern is required"))}
	}
	if g.Runner == nil {
		return &sharedkernel.ToolResult{Error: errors.New("ripgrep runner is not configured")}
	}

	requested, allowedRoot, err := resolveReadTarget(g.WorkDir, g.ReadRoots, a.Path)
	if err != nil {
		return &sharedkernel.ToolResult{Error: NewErrorWithPrompt(&FilePathError{}, err)}
	}
	root, err := g.Runner.Resolve(allowedRoot)
	if err != nil {
		return &sharedkernel.ToolResult{Error: fmt.Errorf("resolve working directory: %w", err)}
	}
	if !root.IsDir {
		return &sharedkernel.ToolResult{Error: errors.New("working directory is not a directory")}
	}

	target, err := g.Runner.Resolve(requested)
	if err != nil {
		return &sharedkernel.ToolResult{Error: NewErrorWithPrompt(&FilePathError{}, fmt.Errorf("resolve glob path %q: %w", a.Path, err))}
	}
	if !target.IsDir {
		return &sharedkernel.ToolResult{Error: NewErrorWithPrompt(&FilePathError{}, fmt.Errorf("glob path must be a directory: %s", requested))}
	}
	// 同时校验真实路径，防止目录内的符号链接把搜索根目录带出沙箱。
	if !withinSearchRoot(root.RealPath, target.RealPath) {
		return &sharedkernel.ToolResult{Error: NewErrorWithPrompt(&FilePathError{}, fmt.Errorf("glob path %q resolves outside working directory", a.Path))}
	}

	search, err := g.Runner.Glob(ctx, target.RealPath, a.Pattern, globFileLimit)
	if err != nil {
		return &sharedkernel.ToolResult{Error: err}
	}
	result := &sharedkernel.ToolResult{
		Metadata: map[string]any{"count": len(search.Files), "truncated": search.Truncated},
	}
	result.Title, err = filepath.Rel(allowedRoot, requested)
	if err != nil {
		result.Title = requested
	}
	if len(search.Files) == 0 {
		result.Output = "No files found"
		return result
	}

	files := make([]string, 0, len(search.Files)+2)
	for _, file := range search.Files {
		if filepath.IsAbs(file) || !withinSearchRoot(".", file) {
			return &sharedkernel.ToolResult{Error: fmt.Errorf("invalid ripgrep file path %q", file)}
		}
		files = append(files, filepath.Clean(filepath.Join(requested, filepath.FromSlash(file))))
	}
	if search.Truncated {
		files = append(files, "", fmt.Sprintf("(Results are truncated: showing first %d results. Consider using a more specific path or pattern.)", globFileLimit))
	}
	result.Output = strings.Join(files, "\n")
	return result
}
