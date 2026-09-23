package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

type readFileToolArgs struct {
	Path        string `json:"path"`
	StartLineNo int    `json:"start_line_no"`
	StartBytes  int    `json:"start_bytes"`
	LineCount   int    `json:"line_count"`
}

const readFileToolMaxReadBytes = 50 * 1024

type ReadFileTool struct {
	WorkDir   string   `json:"work_dir"`
	ReadRoots []string `json:"-"`
	// FS 是沙箱文件读写端口，经构造注入；实现见 infrastructure/workfs。
	FS WorkFS
}

func NewReadFileTool(workDir string, workFS WorkFS, readRoots ...string) *ReadFileTool {
	return &ReadFileTool{WorkDir: workDir, ReadRoots: append([]string(nil), readRoots...), FS: workFS}
}

func (r *ReadFileTool) AfterExecInfo(message json.RawMessage) string {
	return ""
}

func (r *ReadFileTool) BeforeExecInfo(args json.RawMessage) string {
	var argsObj readFileToolArgs
	if err := json.Unmarshal(args, &argsObj); err != nil {
		return ToolReadFile + "()"
	}
	if argsObj.Path == "" {
		return ToolReadFile + "()"
	}

	return fmt.Sprintf("%s(path=%s, start_line_no=%d, start_bytes=%d, line_count=%d)", ToolReadFile, argsObj.Path, argsObj.StartLineNo, argsObj.StartBytes, argsObj.LineCount)
}

func (r *ReadFileTool) Name() string {
	return ToolReadFile
}

func (r *ReadFileTool) Definition() sharedkernel.ToolDefinition {
	return sharedkernel.ToolDefinition{
		Name:        r.Name(),
		Description: "读取文件内容。相对路径限制在工作目录内；系统明确提供的只读目录（如全局 skills）可使用绝对路径。可通过 line_count 指定本次读取的总行数；不指定时不限制行数。单次内容不超过 50KB，输出末尾以 (...) 标注是否读完、最后一行行号及续读参数，未读完时按标注的 start_line_no/start_bytes 续读",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "工作目录内的相对路径，或系统明确提供的只读绝对路径",
				},
				"start_line_no": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "起始行号，1-based，最小为 1，禁止传 0；从头开始读传 start_line_no=1",
				},
				"start_bytes": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "起始行内字节偏移，1-based，最小为 1，禁止传 0；从起始字节开始读传 start_bytes=1",
				},
				"line_count": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "本次读取的总行数，最小为 1；不传则不限制行数，仅受 50KB 最大读取字节数限制",
				},
			},
			"required": []string{"path", "start_line_no", "start_bytes"},
		},
	}
}

func (r *ReadFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var argsObj readFileToolArgs
	if err := json.Unmarshal(args, &argsObj); err != nil {
		return "", NewErrorWithPrompt(&ParamError{}, err)
	}

	if argsObj.Path == "" || strings.TrimSpace(argsObj.Path) == "" {
		return "", NewErrorWithPrompt(&ParamError{}, errors.New("path required"))
	}

	pathSafe, root, err := resolveReadTarget(r.WorkDir, r.ReadRoots, argsObj.Path)
	if err != nil {
		return "", NewErrorWithPrompt(&FilePathError{}, err)
	}
	if err := ensureRealPathWithin(root, pathSafe); err != nil {
		return "", NewErrorWithPrompt(&FilePathError{}, err)
	}

	file, err := r.FS.OpenRead(pathSafe)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", NewErrorWithPrompt(&FileNotExistError{},
				fmt.Errorf("文件 %s 不存在，请核对相对路径是否正确", argsObj.Path))
		}
		return "", NewErrorWithPrompt(&FileIOError{}, err)
	}
	defer file.Close()

	result := ReadPaged(file, PagedReadRequest{
		MaxBytes:    readFileToolMaxReadBytes,
		MaxLines:    argsObj.LineCount,
		StartLineNo: argsObj.StartLineNo,
		StartBytes:  argsObj.StartBytes,
	})
	if result.Err != nil {
		return "", NewErrorWithPrompt(&FileIOError{}, result.Err)
	}

	if len(result.Content) == 0 {
		return "(" + readFooter(result) + ")\n", nil
	}

	var sb strings.Builder
	sb.Write(result.Content)
	sb.WriteString("\n(")
	sb.WriteString(readFooter(result))
	sb.WriteString(")\n")
	return sb.String(), nil
}

// readFooter 生成 read_file 输出的尾部状态说明：文件是否读完、读到的
// 最后一行行号、最后一行是否截断及该行已读字节数，并给出续读参数，
// 使模型无需额外信息即可自行翻页。
func readFooter(res *PagedReadResult) string {
	if res.EndLineNo == 0 {
		return "文件为空"
	}

	var b strings.Builder
	if res.Finished {
		b.WriteString("文件已读完")
	} else {
		b.WriteString("文件未读完")
	}
	fmt.Fprintf(&b, "，最后一行行号: %d", res.EndLineNo)

	switch {
	case res.LastLineTruncated:
		fmt.Fprintf(&b, "，该行未读完整(已读 %d 字节)，续读请传 start_line_no=%d, start_bytes=%d",
			res.LastLineTruncatedBytes, res.EndLineNo, res.LastLineTruncatedBytes+1)
	case !res.Finished:
		fmt.Fprintf(&b, "，续读请传 start_line_no=%d, start_bytes=1", res.EndLineNo+1)
	}

	if res.Finished && len(res.Content) == 0 {
		b.WriteString("（本次未读取到内容：起始行超出文件范围）")
	}
	return b.String()
}
