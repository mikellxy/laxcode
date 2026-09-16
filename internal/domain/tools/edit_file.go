package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

type editFileChangeArgs struct {
	OldText *string `json:"old_text"`
	NewText *string `json:"new_text"`
}

type editFileArgs struct {
	Path  string               `json:"path"`
	Edits []editFileChangeArgs `json:"edits"`
}

type EditFileTool struct {
	WorkDir string
	// FS 是沙箱文件读写端口，经构造注入；实现见 infrastructure/workfs。
	FS WorkFS
}

func NewEditFileTool(workDir string, workFS WorkFS) *EditFileTool {
	return &EditFileTool{WorkDir: workDir, FS: workFS}
}

func (e *EditFileTool) AfterExecInfo(message json.RawMessage) string {
	return ""
}

func (e *EditFileTool) BeforeExecInfo(args json.RawMessage) string {
	var argsObj editFileArgs
	if err := json.Unmarshal(args, &argsObj); err != nil {
		return ToolEditFile + "()"
	}
	if argsObj.Path == "" {
		return ToolEditFile + "()"
	}

	return fmt.Sprintf("%s(path=%s, edits=%d)", ToolEditFile, argsObj.Path, len(argsObj.Edits))
}

func (e *EditFileTool) Name() string {
	return ToolEditFile
}

func (e *EditFileTool) Definition() sharedkernel.ToolDefinition {
	return sharedkernel.ToolDefinition{
		Name:        e.Name(),
		Description: "批量替换文件中已有的文本片段。每个 old_text 必须与文件原始字节精确一致、仅匹配一处且匹配区间互不重叠；行首和行尾空白数量、空格与 Tab、空行以及 LF/CRLF 均须完全一致。全部预检通过后按 offset 从后向前替换；若执行期间文件变化则保留已完成项、停止后续编辑并要求重新 read_file。文件必须已存在，新建文件请使用 write_file。**严格限制**只编辑工作目录内的文件，提供相对路径",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "要编辑的文件的相对路径，如 cmd/main/main.go",
				},
				"edits": map[string]any{
					"type":        "array",
					"minItems":    1,
					"description": "要执行的精确替换列表；所有项目先统一预检，再按匹配 offset 从后向前执行",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"old_text": map[string]any{
								"type":        "string",
								"minLength":   1,
								"description": "要替换的原文，必须逐字节精确匹配且在文件中仅出现一次",
							},
							"new_text": map[string]any{
								"type":        "string",
								"description": "替换后的原始内容，允许为空字符串（删除该片段）",
							},
						},
						"required": []string{"old_text", "new_text"},
					},
				},
			},
			"required": []string{"path", "edits"},
		},
	}
}

func (e *EditFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var argsObj editFileArgs
	if err := json.Unmarshal(args, &argsObj); err != nil {
		return "", NewErrorWithPrompt(&ParamError{}, err)
	}

	if strings.TrimSpace(argsObj.Path) == "" {
		return "", NewErrorWithPrompt(&ParamError{}, errors.New("path required"))
	}
	if len(argsObj.Edits) == 0 {
		return "", NewErrorWithPrompt(&ParamError{}, errors.New("edits must contain at least one item"))
	}
	for i, edit := range argsObj.Edits {
		if edit.OldText == nil || *edit.OldText == "" {
			return "", NewErrorWithPrompt(&ParamError{}, fmt.Errorf("edits[%d].old_text must be non-empty", i))
		}
		if edit.NewText == nil {
			return "", NewErrorWithPrompt(&ParamError{}, fmt.Errorf("edits[%d].new_text required", i))
		}
	}

	target, err := safeJoinWorkDir(argsObj.Path, e.WorkDir)
	if err != nil {
		return "", err
	}

	b, err := e.FS.ReadFile(target)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", NewErrorWithPrompt(&FileNotExistError{},
				fmt.Errorf("文件 %s 不存在，新建文件请使用 write_file", argsObj.Path))
		}
		return "", NewErrorWithPrompt(&FileIOError{}, err)
	}

	planned, err := planExactEdits(string(b), argsObj.Edits)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(e.WorkDir, target)
	if err != nil {
		rel = target
	}
	rel = filepath.ToSlash(rel)

	completed := make([]plannedEdit, 0, len(planned))
	for _, edit := range planned {
		currentBytes, readErr := e.FS.ReadFile(target)
		if readErr != nil {
			return partialEditResult(rel, completed, len(planned), edit,
				fmt.Sprintf("重新读取文件失败: %v", readErr))
		}
		current := string(currentBytes)
		if edit.Offset < 0 || edit.End > len(current) || current[edit.Offset:edit.End] != edit.OldText {
			return partialEditResult(rel, completed, len(planned), edit,
				"原 offset 处内容已不再与 old_text 精确匹配")
		}

		updated := replaceAt(current, edit.Offset, len(edit.OldText), edit.NewText)
		if writeErr := e.FS.WriteFile(target, []byte(updated)); writeErr != nil {
			return partialEditResult(rel, completed, len(planned), edit,
				fmt.Sprintf("写入文件失败: %v", writeErr))
		}
		completed = append(completed, edit)
	}

	return formatCompletedEdits(rel, completed, len(planned)), nil
}

// safeJoinWorkDir 将用户提供的相对路径安全地解析到工作目录内，
// 防止 ../ 路径穿越或绝对路径逃逸到工作目录之外。
func safeJoinWorkDir(rel string, workDir string) (string, error) {
	workDirAbs, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("resolve work dir: %w", err)
	}

	// 工具契约要求相对路径，显式拒绝绝对路径，避免语义歧义
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be a relative path within working directory %q", rel, workDirAbs)
	}

	target := filepath.Clean(filepath.Join(workDirAbs, rel))

	// 校验目标路径必须位于工作目录内
	check, err := filepath.Rel(workDirAbs, target)
	if err != nil {
		return "", fmt.Errorf("resolve target path: %w", err)
	}
	if check == ".." || strings.HasPrefix(check, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes working directory %q", rel, workDirAbs)
	}

	return target, nil
}

// ---------- edit_file 批量精确匹配引擎 ----------

type plannedEdit struct {
	RequestIndex int
	OldText      string
	NewText      string
	Offset       int
	End          int
	StartLine    int
	EndLine      int
}

// planExactEdits 在原始文件快照中预检全部编辑项。每个 old_text 必须逐字节
// 精确匹配一处，且各匹配区间不得重叠。预检失败时不返回执行计划，调用方不会
// 写入文件。成功计划按 offset 降序排列，保证高位替换不会移动低位 offset。
func planExactEdits(content string, edits []editFileChangeArgs) ([]plannedEdit, error) {
	planned := make([]plannedEdit, 0, len(edits))
	var issues []string

	for i, edit := range edits {
		oldText := *edit.OldText
		hits := findAllExact(content, oldText)
		switch len(hits) {
		case 0:
			issues = append(issues, fmt.Sprintf(
				"#%d old_text 未找到逐字节精确匹配", i+1))
		case 1:
			offset := hits[0]
			planned = append(planned, plannedEdit{
				RequestIndex: i,
				OldText:      oldText,
				NewText:      *edit.NewText,
				Offset:       offset,
				End:          offset + len(oldText),
				StartLine:    lineAt(content, offset),
				EndLine:      lineAt(content, offset+len(oldText)-1),
			})
		default:
			issues = append(issues, fmt.Sprintf(
				"#%d old_text 精确匹配到 %d 处（第 %s 行）",
				i+1, len(hits), joinLineNumbers(offsetLines(content, hits))))
		}
	}

	// 即使其他项目未命中，也检查所有已唯一命中的区间，一次返回尽可能完整的
	// 预检问题，避免模型逐项试错。
	byOffset := append([]plannedEdit(nil), planned...)
	sort.SliceStable(byOffset, func(i, j int) bool {
		if byOffset[i].Offset == byOffset[j].Offset {
			return byOffset[i].End < byOffset[j].End
		}
		return byOffset[i].Offset < byOffset[j].Offset
	})
	for i := 1; i < len(byOffset); i++ {
		prev, current := byOffset[i-1], byOffset[i]
		if current.Offset < prev.End {
			issues = append(issues, fmt.Sprintf(
				"#%d 与 #%d 的精确匹配区间重叠（字节区间 [%d,%d) 与 [%d,%d)）",
				prev.RequestIndex+1, current.RequestIndex+1,
				prev.Offset, prev.End, current.Offset, current.End))
		}
	}

	if len(issues) > 0 {
		return nil, NewErrorWithPrompt(&EditBatchValidationError{},
			errors.New("批量编辑预检失败，文件未修改：\n- "+strings.Join(issues, "\n- ")))
	}

	sort.SliceStable(planned, func(i, j int) bool {
		return planned[i].Offset > planned[j].Offset
	})
	return planned, nil
}

// findAllExact 返回 sub 在 s 中的全部精确起始字节偏移。每次从命中位置的下
// 一个字节继续查找，因此重叠出现也会被识别为多处匹配。
func findAllExact(s, sub string) []int {
	if sub == "" {
		return nil
	}

	var offsets []int
	for from := 0; from+len(sub) <= len(s); {
		i := strings.Index(s[from:], sub)
		if i < 0 {
			break
		}
		offset := from + i
		offsets = append(offsets, offset)
		from = offset + 1
	}
	return offsets
}

// lineAt 返回字节偏移 off 对应的 1-based 行号。
func lineAt(s string, off int) int {
	return 1 + strings.Count(s[:off], "\n")
}

// offsetLines 批量将字节偏移转换为 1-based 行号。
func offsetLines(s string, offsets []int) []int {
	lines := make([]int, len(offsets))
	for i, off := range offsets {
		lines[i] = lineAt(s, off)
	}
	return lines
}

func joinLineNumbers(lines []int) string {
	parts := make([]string, len(lines))
	for i, line := range lines {
		parts[i] = fmt.Sprintf("%d", line)
	}
	return strings.Join(parts, "、")
}

// replaceAt 将 s 中 [start, start+length) 区间替换为 repl。
func replaceAt(s string, start, length int, repl string) string {
	return s[:start] + repl + s[start+length:]
}

func formatCompletedEdits(path string, completed []plannedEdit, total int) string {
	ordered := append([]plannedEdit(nil), completed...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].RequestIndex < ordered[j].RequestIndex
	})

	var b strings.Builder
	fmt.Fprintf(&b, "已在 %s 完成 %d/%d 处精确替换", path, len(ordered), total)
	for _, edit := range ordered {
		fmt.Fprintf(&b, "\n#%d 第 %d-%d 行", edit.RequestIndex+1, edit.StartLine, edit.EndLine)
	}
	return b.String()
}

func partialEditResult(path string, completed []plannedEdit, total int, stopped plannedEdit, reason string) (string, error) {
	var b strings.Builder
	if len(completed) == 0 {
		fmt.Fprintf(&b, "批量编辑未执行：%s 尚未写入任何替换", path)
	} else {
		b.WriteString(formatCompletedEdits(path, completed, total))
	}
	fmt.Fprintf(&b, "\n处理 #%d 时停止：%s。文件当前可能已被部分修改，请立即重新 read_file 确认后再编辑",
		stopped.RequestIndex+1, reason)

	message := b.String()
	return message, NewErrorWithPrompt(&EditPartialError{}, errors.New(message))
}
