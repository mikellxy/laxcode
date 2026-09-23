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

const grepMatchLimit = 100

type RipgrepPathInfo struct {
	RealPath string
	IsDir    bool
}

type GrepMatch struct {
	Path string
	Line int
	Text string
}

type GrepSearchResult struct {
	Matches   []GrepMatch
	Truncated bool
}

type GlobSearchResult struct {
	Files     []string
	Truncated bool
}

type RipgrepRunner interface {
	Resolve(absPath string) (RipgrepPathInfo, error)
	Search(ctx context.Context, cwd, file, pattern, include string, limit int) (GrepSearchResult, error)
	Glob(ctx context.Context, cwd, pattern string, limit int) (GlobSearchResult, error)
}

type grepArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
	Include string `json:"include,omitempty"`
}

type GrepTool struct {
	WorkDir   string
	ReadRoots []string
	Runner    RipgrepRunner
}

func NewGrepTool(workDir string, runner RipgrepRunner, readRoots ...string) *GrepTool {
	return &GrepTool{WorkDir: workDir, ReadRoots: append([]string(nil), readRoots...), Runner: runner}
}

func (g *GrepTool) Name() string { return ToolGrep }

func (g *GrepTool) Definition() sharedkernel.ToolDefinition {
	return sharedkernel.ToolDefinition{
		Name:        g.Name(),
		Description: "用正则表达式搜索文件内容，返回匹配文件、行号和行文本。相对 path 限制在工作目录内；系统明确提供的只读目录可使用绝对路径。include 用 glob 筛选文件，如 *.go 或 *.{ts,tsx}。最多返回 100 行匹配；需要精确计数时使用 bash 中的 rg。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "用于搜索文件内容的正则表达式"},
				"path":    map[string]any{"type": "string", "description": "工作目录内的路径，或系统明确提供的只读绝对路径；默认工作目录"},
				"include": map[string]any{"type": "string", "description": "文件 glob，如 *.go 或 *.{ts,tsx}"},
			},
			"required": []string{"pattern"},
		},
	}
}

func (g *GrepTool) BeforeExecInfo(raw json.RawMessage) string {
	var a grepArgs
	if json.Unmarshal(raw, &a) != nil || a.Pattern == "" {
		return ToolGrep + "()"
	}
	return fmt.Sprintf("grep(pattern=%q, path=%q, include=%q)", a.Pattern, a.Path, a.Include)
}

func (*GrepTool) AfterExecInfo(json.RawMessage) string { return "" }

func (g *GrepTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	result := g.ExecuteResult(ctx, raw)
	return result.Output, result.Error
}

func (g *GrepTool) ExecuteResult(ctx context.Context, raw json.RawMessage) *sharedkernel.ToolResult {
	var a grepArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return &sharedkernel.ToolResult{Error: NewErrorWithPrompt(&ParamError{}, err)}
	}
	if a.Pattern == "" {
		return &sharedkernel.ToolResult{Error: NewErrorWithPrompt(&ParamError{}, errors.New("pattern is required"))}
	}
	if g.Runner == nil {
		return &sharedkernel.ToolResult{Error: errors.New("grep runner is not configured")}
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
		return &sharedkernel.ToolResult{Error: NewErrorWithPrompt(&FilePathError{}, fmt.Errorf("resolve grep path %q: %w", a.Path, err))}
	}
	// 校验真实路径，防止目录内的符号链接把 rg 带到沙箱外。
	if !withinSearchRoot(root.RealPath, target.RealPath) {
		return &sharedkernel.ToolResult{Error: NewErrorWithPrompt(&FilePathError{}, fmt.Errorf("grep path %q resolves outside working directory", a.Path))}
	}

	cwd, file, displayBase := target.RealPath, ".", requested
	if !target.IsDir {
		cwd = filepath.Dir(target.RealPath)
		file = filepath.Base(target.RealPath)
		displayBase = filepath.Dir(requested)
	}
	search, err := g.Runner.Search(ctx, cwd, file, a.Pattern, a.Include, grepMatchLimit)
	if err != nil {
		return &sharedkernel.ToolResult{Error: err}
	}

	result := &sharedkernel.ToolResult{
		Title:    a.Pattern,
		Metadata: map[string]any{"matches": len(search.Matches), "truncated": search.Truncated},
	}
	if len(search.Matches) == 0 {
		result.Output = "No files found"
		return result
	}

	var output strings.Builder
	fmt.Fprintf(&output, "Found %d matches", len(search.Matches))
	if search.Truncated {
		output.WriteString(" (more matches available)")
	}
	current := ""
	for _, match := range search.Matches {
		if filepath.IsAbs(match.Path) || !withinSearchRoot(".", match.Path) {
			return &sharedkernel.ToolResult{Error: fmt.Errorf("invalid ripgrep match path %q", match.Path)}
		}
		name := filepath.Clean(filepath.Join(displayBase, filepath.FromSlash(match.Path)))
		if name != current {
			if current != "" {
				output.WriteString("\n")
			}
			fmt.Fprintf(&output, "\n%s:", name)
			current = name
		}
		text, clipped := safeTruncateUTF8(match.Text, 2000)
		if clipped {
			text += "..."
		}
		fmt.Fprintf(&output, "\n  Line %d: %s", match.Line, text)
	}
	if search.Truncated {
		output.WriteString("\n\n(Results truncated. Consider using a more specific path or pattern.)")
	}
	result.Output = output.String()
	return result
}

func withinSearchRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
