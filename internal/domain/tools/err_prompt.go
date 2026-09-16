package tools

import "fmt"

const (
	errTypeTool = "tool"
	errTypeOs   = "os"
)

type ErrorWithPrompt interface {
	Error() string
	Wrap(err error)
	AsPrompt() (string, bool)
}

var ErrPromptTmpl = `### error_type: %s
### error_detail: %s
### suggestion: %s`

func buildErrPrompt(errType, errDetail, sug string) string {
	return fmt.Sprintf(ErrPromptTmpl, errType, errDetail, sug)
}

func NewErrorWithPrompt(e ErrorWithPrompt, innerErr error) ErrorWithPrompt {
	e.Wrap(innerErr)
	return e
}

type errWithPromptBase struct {
	Err error
}

func (f *errWithPromptBase) Error() string {
	if f.Err == nil {
		return ""
	}
	return f.Err.Error()
}

func (f *errWithPromptBase) Wrap(err error) {
	f.Err = err
}

type ParamError struct {
	errWithPromptBase
}

func (f *ParamError) AsPrompt() (string, bool) {
	if f.Err == nil {
		return "", false
	}
	return buildErrPrompt(errTypeTool,
		f.Err.Error(),
		"工具调用参数错误或缺失，仔细阅读工具定义并修正",
	), true
}

type FilePathError struct {
	errWithPromptBase
}

func (f *FilePathError) AsPrompt() (string, bool) {
	return buildErrPrompt(errTypeTool,
		f.Err.Error(),
		"检查文件路径格式标准，是否逃逸出限制目录，并修正",
	), true
}

// FileNotExistError 目标文件不存在，suggestion 引导核对路径并提示
// 编辑仅限已有文件、新建文件应改用 write_file。
type FileNotExistError struct {
	errWithPromptBase
}

func (f *FileNotExistError) AsPrompt() (string, bool) {
	if f.Err == nil {
		return "", false
	}
	return buildErrPrompt(errTypeTool,
		f.Err.Error(),
		"核对文件路径是否为工作目录内正确的相对路径；编辑仅支持已存在的文件，新建文件请使用 write_file",
	), true
}

// EditBatchValidationError 表示 edit_file 的批量精确匹配预检失败。此时文件
// 尚未写入，提示模型重新读取并逐字节核对空白与换行符。
type EditBatchValidationError struct {
	errWithPromptBase
}

func (f *EditBatchValidationError) AsPrompt() (string, bool) {
	if f.Err == nil {
		return "", false
	}
	return buildErrPrompt(errTypeTool,
		f.Err.Error(),
		"本次批量编辑未修改文件。请先用 read_file 重新获取最新内容，逐字核对每个 old_text；特别注意每行行首和行尾空白字符数量、空格与 Tab、空行数量以及 LF/CRLF 换行符，确保每项仅精确匹配一处且区间互不重叠后再试",
	), true
}

// EditPartialError 表示 edit_file 执行批次时发现文件变化或发生写入故障。
// 此前成功项已经落盘，模型必须先重新读取，禁止按旧快照继续编辑。
type EditPartialError struct {
	errWithPromptBase
}

func (f *EditPartialError) AsPrompt() (string, bool) {
	if f.Err == nil {
		return "", false
	}
	return buildErrPrompt(errTypeTool,
		f.Err.Error(),
		"批量编辑已停止，文件可能已有部分替换成功。禁止沿用旧 offset 或原样重试；请立即用 read_file 重新读取文件，确认当前内容后重新组织剩余 edits",
	), true
}

// FileIOError 文件读写类系统错误（权限、磁盘、占用等），error_type 为 os。
type FileIOError struct {
	errWithPromptBase
}

func (f *FileIOError) AsPrompt() (string, bool) {
	if f.Err == nil {
		return "", false
	}
	return buildErrPrompt(errTypeOs,
		f.Err.Error(),
		"文件读写失败，可能是权限、磁盘或文件被占用等系统问题，请检查文件权限与状态，不要盲目重试相同操作",
	), true
}

type BashExecuteError struct {
	errWithPromptBase
}

// AsPrompt 生成给到LLM的纠错提示
func (f *BashExecuteError) AsPrompt() (string, bool) {
	if f.Err == nil {
		return "", false
	}

	return buildErrPrompt(
		errTypeTool,
		f.Err.Error(),
		`Bash进程执行发生系统故障。
排查方向：
1.命令执行超时，可调整 timeout_ms（最多 60000）或拆分任务，先查看已返回的输出；
2.工作目录不存在、权限不足；
3.命令语法错误、可执行文件找不到；
不要反复原样重试，先检查命令与工作环境。`,
	), true
}
