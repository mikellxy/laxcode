package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/mikellxy/laxcode/internal/domain/prompt"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

const (
	skillConfirmationKind = "skill_write"
	maxSkillFiles         = 64
	maxSkillFileBytes     = 256 * 1024
	maxSkillPackageBytes  = 1024 * 1024
)

type skillFileArgs struct {
	Path       string  `json:"path"`
	Content    *string `json:"content"`
	Executable bool    `json:"executable,omitempty"`
}

type createSkillArgs struct {
	Name            string          `json:"name"`
	SkillMD         *string         `json:"skill_md"`
	SupportingFiles []skillFileArgs `json:"supporting_files,omitempty"`
}

type skillReplacementArgs struct {
	OldText *string `json:"old_text"`
	NewText *string `json:"new_text"`
}

type skillFileEditArgs struct {
	Path         string                 `json:"path"`
	Replacements []skillReplacementArgs `json:"replacements"`
}

type updateSkillArgs struct {
	Name        string              `json:"name"`
	Edits       []skillFileEditArgs `json:"edits,omitempty"`
	CreateFiles []skillFileArgs     `json:"create_files,omitempty"`
}

type CreateSkillTool struct {
	SkillsRoot string
	Store      SkillPackageStore
}

func NewCreateSkillTool(skillsRoot string, store SkillPackageStore) *CreateSkillTool {
	return &CreateSkillTool{SkillsRoot: skillsRoot, Store: store}
}

func (*CreateSkillTool) Name() string { return ToolCreateSkill }

func (*CreateSkillTool) Definition() sharedkernel.ToolDefinition {
	return sharedkernel.ToolDefinition{
		Name: ToolCreateSkill,
		Description: "创建新的全局 Skill 包。目标固定为系统 Skills 根目录下的 <name>/，" +
			"不接受目标根路径且不会覆盖已有 Skill。SKILL.md 和所有 supporting_files 会统一校验后一次提交；持久化前需要用户确认。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":             map[string]any{"type": "string", "description": "Skill 名称：小写字母、数字和单连字符，最多 64 字符"},
				"skill_md":         map[string]any{"type": "string", "description": "完整 SKILL.md；frontmatter name 必须与 name 一致，description 必须非空"},
				"supporting_files": skillFilesSchema("要随 Skill 一并创建的其他 UTF-8 文本文件；路径相对于 Skill 目录"),
			},
			"required": []string{"name", "skill_md"},
		},
	}
}

func (t *CreateSkillTool) BeforeExecInfo(raw json.RawMessage) string {
	var a createSkillArgs
	if json.Unmarshal(raw, &a) != nil || a.Name == "" {
		return ToolCreateSkill + "()"
	}
	return fmt.Sprintf("%s(name=%s, supporting_files=%d)", ToolCreateSkill, a.Name, len(a.SupportingFiles))
}

func (*CreateSkillTool) AfterExecInfo(json.RawMessage) string { return "" }

func (t *CreateSkillTool) Confirmation(ctx context.Context, raw json.RawMessage) (*ToolConfirmation, error) {
	name, files, err := t.prepare(ctx, raw)
	if err != nil {
		return nil, err
	}
	return &ToolConfirmation{Kind: skillConfirmationKind, Content: fmt.Sprintf(
		"即将创建全局 Skill %q。\n目标：%s\n文件：%s\n该操作会影响后续 LaxCode 会话。输入 yes 创建；其他输入取消。",
		name, filepath.Join(t.SkillsRoot, name), strings.Join(skillFileNames(files), "、"))}, nil
}

func (t *CreateSkillTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	name, files, err := t.prepare(ctx, raw)
	if err != nil {
		return "", err
	}
	if err := t.Store.Create(ctx, name, files); err != nil {
		return "", NewErrorWithPrompt(&FileIOError{}, fmt.Errorf("create skill %q: %w", name, err))
	}
	return marshalSkillResult("created", name, filepath.Join(t.SkillsRoot, name), skillFileNames(files))
}

func (t *CreateSkillTool) prepare(ctx context.Context, raw json.RawMessage) (string, []SkillPackageFile, error) {
	if t.Store == nil {
		return "", nil, errors.New("skill store is not configured")
	}
	var a createSkillArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", nil, NewErrorWithPrompt(&ParamError{}, err)
	}
	if err := prompt.ValidateSkillName(a.Name); err != nil {
		return "", nil, NewErrorWithPrompt(&ParamError{}, err)
	}
	if a.SkillMD == nil {
		return "", nil, NewErrorWithPrompt(&ParamError{}, errors.New("skill_md required"))
	}
	files := []SkillPackageFile{{Path: "SKILL.md", Content: []byte(*a.SkillMD)}}
	for i, input := range a.SupportingFiles {
		file, err := normalizeSkillFile(input, fmt.Sprintf("supporting_files[%d]", i), false)
		if err != nil {
			return "", nil, err
		}
		files = append(files, file)
	}
	if err := validateSkillPackage(a.Name, files); err != nil {
		return "", nil, err
	}
	if _, err := t.Store.Load(ctx, a.Name); err == nil {
		return "", nil, NewErrorWithPrompt(&ParamError{}, fmt.Errorf("skill %q already exists; use update_skill", a.Name))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", nil, NewErrorWithPrompt(&FileIOError{}, fmt.Errorf("inspect skill %q: %w", a.Name, err))
	}
	return a.Name, files, nil
}

type UpdateSkillTool struct {
	SkillsRoot string
	Store      SkillPackageStore
}

func NewUpdateSkillTool(skillsRoot string, store SkillPackageStore) *UpdateSkillTool {
	return &UpdateSkillTool{SkillsRoot: skillsRoot, Store: store}
}

func (*UpdateSkillTool) Name() string { return ToolUpdateSkill }

func (*UpdateSkillTool) Definition() sharedkernel.ToolDefinition {
	return sharedkernel.ToolDefinition{
		Name: ToolUpdateSkill,
		Description: "更新已有全局 Skill。edits 对已有 UTF-8 文本文件执行逐字节唯一匹配替换；" +
			"create_files 只能新增文件。工具不支持删除、移动、重命名或无条件覆盖，完整预检和 Skill 校验通过后一次提交；持久化前需要用户确认。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "要更新的现有 Skill 名称"},
				"edits": map[string]any{
					"type": "array", "description": "按文件组织的精确替换；同一文件只能出现一次",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path": map[string]any{"type": "string", "description": "Skill 包内已有文本文件的相对路径"},
							"replacements": map[string]any{
								"type": "array", "minItems": 1,
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"old_text": map[string]any{"type": "string", "minLength": 1, "description": "必须在原文件中逐字节唯一匹配"},
										"new_text": map[string]any{"type": "string", "description": "替换文本，允许为空"},
									},
									"required": []string{"old_text", "new_text"},
								},
							},
						},
						"required": []string{"path", "replacements"},
					},
				},
				"create_files": skillFilesSchema("要新增的 UTF-8 文本文件；已有路径会导致整个更新失败"),
			},
			"required": []string{"name"},
		},
	}
}

func (t *UpdateSkillTool) BeforeExecInfo(raw json.RawMessage) string {
	var a updateSkillArgs
	if json.Unmarshal(raw, &a) != nil || a.Name == "" {
		return ToolUpdateSkill + "()"
	}
	return fmt.Sprintf("%s(name=%s, edited_files=%d, created_files=%d)", ToolUpdateSkill, a.Name, len(a.Edits), len(a.CreateFiles))
}

func (*UpdateSkillTool) AfterExecInfo(json.RawMessage) string { return "" }

type preparedSkillUpdate struct {
	name       string
	revision   string
	files      []SkillPackageFile
	changed    []string
	replaceOps int
}

func (t *UpdateSkillTool) Confirmation(ctx context.Context, raw json.RawMessage) (*ToolConfirmation, error) {
	prepared, err := t.prepare(ctx, raw)
	if err != nil {
		return nil, err
	}
	return &ToolConfirmation{Kind: skillConfirmationKind, Content: fmt.Sprintf(
		"即将更新全局 Skill %q。\n目标：%s\n变更文件：%s\n精确替换：%d 处\n该操作会影响后续 LaxCode 会话。输入 yes 更新；其他输入取消。",
		prepared.name, filepath.Join(t.SkillsRoot, prepared.name), strings.Join(prepared.changed, "、"), prepared.replaceOps)}, nil
}

func (t *UpdateSkillTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	prepared, err := t.prepare(ctx, raw)
	if err != nil {
		return "", err
	}
	if err := t.Store.Replace(ctx, prepared.name, prepared.revision, prepared.files); err != nil {
		return "", NewErrorWithPrompt(&FileIOError{}, fmt.Errorf("update skill %q: %w", prepared.name, err))
	}
	return marshalSkillResult("updated", prepared.name, filepath.Join(t.SkillsRoot, prepared.name), prepared.changed)
}

func (t *UpdateSkillTool) prepare(ctx context.Context, raw json.RawMessage) (*preparedSkillUpdate, error) {
	if t.Store == nil {
		return nil, errors.New("skill store is not configured")
	}
	var a updateSkillArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, NewErrorWithPrompt(&ParamError{}, err)
	}
	if err := prompt.ValidateSkillName(a.Name); err != nil {
		return nil, NewErrorWithPrompt(&ParamError{}, err)
	}
	if len(a.Edits) == 0 && len(a.CreateFiles) == 0 {
		return nil, NewErrorWithPrompt(&ParamError{}, errors.New("edits or create_files must contain at least one item"))
	}
	snapshot, err := t.Store.Load(ctx, a.Name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, NewErrorWithPrompt(&FileNotExistError{}, fmt.Errorf("skill %q does not exist; use create_skill", a.Name))
		}
		return nil, NewErrorWithPrompt(&FileIOError{}, fmt.Errorf("load skill %q: %w", a.Name, err))
	}
	files := cloneSkillFiles(snapshot.Files)
	index, err := indexSkillFiles(files)
	if err != nil {
		return nil, err
	}
	changedSet := make(map[string]struct{})
	seenEdits := make(map[string]struct{})
	replaceOps := 0
	for i, edit := range a.Edits {
		rel, err := normalizeSkillPath(edit.Path)
		if err != nil {
			return nil, NewErrorWithPrompt(&ParamError{}, fmt.Errorf("edits[%d].path: %w", i, err))
		}
		key := strings.ToLower(rel)
		if _, duplicate := seenEdits[key]; duplicate {
			return nil, NewErrorWithPrompt(&ParamError{}, fmt.Errorf("edits contains duplicate path %q", rel))
		}
		seenEdits[key] = struct{}{}
		fileIndex, ok := index[key]
		if !ok {
			return nil, NewErrorWithPrompt(&FileNotExistError{}, fmt.Errorf("skill file %q does not exist; use create_files", rel))
		}
		if len(edit.Replacements) == 0 {
			return nil, NewErrorWithPrompt(&ParamError{}, fmt.Errorf("edits[%d].replacements must contain at least one item", i))
		}
		exact := make([]editFileChangeArgs, len(edit.Replacements))
		for j, replacement := range edit.Replacements {
			if replacement.OldText == nil || *replacement.OldText == "" {
				return nil, NewErrorWithPrompt(&ParamError{}, fmt.Errorf("edits[%d].replacements[%d].old_text must be non-empty", i, j))
			}
			if replacement.NewText == nil {
				return nil, NewErrorWithPrompt(&ParamError{}, fmt.Errorf("edits[%d].replacements[%d].new_text required", i, j))
			}
			exact[j] = editFileChangeArgs{OldText: replacement.OldText, NewText: replacement.NewText}
		}
		content := string(files[fileIndex].Content)
		planned, err := planExactEdits(content, exact)
		if err != nil {
			return nil, err
		}
		for _, replacement := range planned {
			content = replaceAt(content, replacement.Offset, len(replacement.OldText), replacement.NewText)
		}
		files[fileIndex].Content = []byte(content)
		changedSet[files[fileIndex].Path] = struct{}{}
		replaceOps += len(planned)
	}
	for i, input := range a.CreateFiles {
		file, err := normalizeSkillFile(input, fmt.Sprintf("create_files[%d]", i), false)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(file.Path)
		if _, exists := index[key]; exists {
			return nil, NewErrorWithPrompt(&ParamError{}, fmt.Errorf("skill file %q already exists; use edits", file.Path))
		}
		index[key] = len(files)
		files = append(files, file)
		changedSet[file.Path] = struct{}{}
	}
	if err := validateSkillPackage(a.Name, files); err != nil {
		return nil, err
	}
	changed := make([]string, 0, len(changedSet))
	for name := range changedSet {
		changed = append(changed, name)
	}
	sort.Strings(changed)
	return &preparedSkillUpdate{name: a.Name, revision: snapshot.Revision, files: files, changed: changed, replaceOps: replaceOps}, nil
}

func skillFilesSchema(description string) map[string]any {
	return map[string]any{
		"type": "array", "description": description,
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":       map[string]any{"type": "string", "description": "Skill 包内 slash 分隔的相对路径；禁止绝对路径、.. 和 SKILL.md"},
				"content":    map[string]any{"type": "string", "description": "完整 UTF-8 文本内容"},
				"executable": map[string]any{"type": "boolean", "description": "是否设置可执行位；仅 scripts/ 下允许，默认 false"},
			},
			"required": []string{"path", "content"},
		},
	}
}

func normalizeSkillFile(input skillFileArgs, field string, allowSkillMD bool) (SkillPackageFile, error) {
	rel, err := normalizeSkillPath(input.Path)
	if err != nil {
		return SkillPackageFile{}, NewErrorWithPrompt(&ParamError{}, fmt.Errorf("%s.path: %w", field, err))
	}
	if !allowSkillMD && strings.EqualFold(rel, "SKILL.md") {
		return SkillPackageFile{}, NewErrorWithPrompt(&ParamError{}, fmt.Errorf("%s.path must not duplicate SKILL.md", field))
	}
	if input.Content == nil {
		return SkillPackageFile{}, NewErrorWithPrompt(&ParamError{}, fmt.Errorf("%s.content required", field))
	}
	if input.Executable && !strings.HasPrefix(strings.ToLower(rel), "scripts/") {
		return SkillPackageFile{}, NewErrorWithPrompt(&ParamError{}, fmt.Errorf("%s.executable is only allowed under scripts/", field))
	}
	return SkillPackageFile{Path: rel, Content: []byte(*input.Content), Executable: input.Executable}, nil
}

func normalizeSkillPath(input string) (string, error) {
	if input == "" || strings.TrimSpace(input) == "" {
		return "", errors.New("path required")
	}
	if strings.ContainsAny(input, "\\\x00") || !fs.ValidPath(input) || path.IsAbs(input) {
		return "", fmt.Errorf("path %q must be a normalized slash-separated package-relative path", input)
	}
	cleaned := path.Clean(input)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path %q escapes the skill directory", input)
	}
	return cleaned, nil
}

func validateSkillPackage(name string, files []SkillPackageFile) error {
	if len(files) == 0 || len(files) > maxSkillFiles {
		return NewErrorWithPrompt(&ParamError{}, fmt.Errorf("skill package must contain 1-%d files", maxSkillFiles))
	}
	seen := make(map[string]string, len(files))
	total := 0
	var skillMD []byte
	for i, file := range files {
		rel, err := normalizeSkillPath(file.Path)
		if err != nil {
			return NewErrorWithPrompt(&ParamError{}, fmt.Errorf("files[%d].path: %w", i, err))
		}
		if rel != file.Path {
			return NewErrorWithPrompt(&ParamError{}, fmt.Errorf("files[%d].path is not normalized", i))
		}
		if !utf8.Valid(file.Content) || strings.IndexByte(string(file.Content), 0) >= 0 {
			return NewErrorWithPrompt(&ParamError{}, fmt.Errorf("skill file %q must be UTF-8 text without NUL bytes", rel))
		}
		if len(file.Content) > maxSkillFileBytes {
			return NewErrorWithPrompt(&ParamError{}, fmt.Errorf("skill file %q exceeds %d bytes", rel, maxSkillFileBytes))
		}
		if file.Executable && !strings.HasPrefix(strings.ToLower(rel), "scripts/") {
			return NewErrorWithPrompt(&ParamError{}, fmt.Errorf("executable skill file %q must be under scripts/", rel))
		}
		key := strings.ToLower(rel)
		if previous, ok := seen[key]; ok {
			return NewErrorWithPrompt(&ParamError{}, fmt.Errorf("skill paths %q and %q collide", previous, rel))
		}
		for previousKey, previous := range seen {
			if strings.HasPrefix(key, previousKey+"/") || strings.HasPrefix(previousKey, key+"/") {
				return NewErrorWithPrompt(&ParamError{}, fmt.Errorf("skill paths %q and %q conflict as file and directory", previous, rel))
			}
		}
		seen[key] = rel
		total += len(file.Content)
		if key == strings.ToLower("SKILL.md") {
			if rel != "SKILL.md" {
				return NewErrorWithPrompt(&ParamError{}, errors.New("skill definition filename must be exactly SKILL.md"))
			}
			skillMD = file.Content
		}
	}
	if total > maxSkillPackageBytes {
		return NewErrorWithPrompt(&ParamError{}, fmt.Errorf("skill package exceeds %d bytes", maxSkillPackageBytes))
	}
	if skillMD == nil {
		return NewErrorWithPrompt(&ParamError{}, errors.New("skill package must contain SKILL.md"))
	}
	if err := prompt.ValidateSkillDefinition(string(skillMD), name); err != nil {
		return NewErrorWithPrompt(&ParamError{}, err)
	}
	return nil
}

func indexSkillFiles(files []SkillPackageFile) (map[string]int, error) {
	index := make(map[string]int, len(files))
	for i, file := range files {
		key := strings.ToLower(file.Path)
		if _, exists := index[key]; exists {
			return nil, NewErrorWithPrompt(&FileIOError{}, fmt.Errorf("stored skill contains colliding path %q", file.Path))
		}
		index[key] = i
	}
	return index, nil
}

func cloneSkillFiles(files []SkillPackageFile) []SkillPackageFile {
	cloned := make([]SkillPackageFile, len(files))
	for i, file := range files {
		cloned[i] = SkillPackageFile{Path: file.Path, Content: append([]byte(nil), file.Content...), Executable: file.Executable}
	}
	return cloned
}

func skillFileNames(files []SkillPackageFile) []string {
	names := make([]string, len(files))
	for i, file := range files {
		names[i] = file.Path
	}
	sort.Strings(names)
	return names
}

func marshalSkillResult(status, name, target string, changed []string) (string, error) {
	result := struct {
		Status               string   `json:"status"`
		Name                 string   `json:"name"`
		Path                 string   `json:"path"`
		ChangedFiles         []string `json:"changed_files"`
		AvailableNextSession bool     `json:"available_next_session"`
	}{status, name, target, changed, true}
	data, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
