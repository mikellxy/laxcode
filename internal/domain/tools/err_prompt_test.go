package tools

import (
	"errors"
	"strings"
	"testing"
)

func TestErrPromptTmpl(t *testing.T) {
	got := buildErrPrompt("tool", "detail msg", "suggestion text")
	want := "### error_type: tool\n### error_detail: detail msg\n### suggestion: suggestion text"
	if got != want {
		t.Errorf("buildErrPrompt() =\n%q\nwant\n%q", got, want)
	}
}

func TestNewErrorWithPromptWraps(t *testing.T) {
	inner := errors.New("inner err")
	pe := NewErrorWithPrompt(&ParamError{}, inner).(*ParamError)
	if pe.Err != inner {
		t.Errorf("应包装 inner err，实际 %v", pe.Err)
	}
	if prompt, ok := pe.AsPrompt(); !ok || !strings.Contains(prompt, "inner err") {
		t.Errorf("AsPrompt 应能携带被包装的错误详情，实际 prompt=%q ok=%v", prompt, ok)
	}
}

func TestErrWithPromptBaseNilError(t *testing.T) {
	pe := &ParamError{}
	if pe.Error() != "" {
		t.Errorf("nil 内部错误时 Error() 应为空串，实际 %q", pe.Error())
	}
	if prompt, ok := pe.AsPrompt(); ok {
		t.Errorf("nil 内部错误时 AsPrompt 应返回 false，实际 prompt=%q", prompt)
	}
}

func TestParamErrorAsPrompt(t *testing.T) {
	pe := NewErrorWithPrompt(&ParamError{}, errors.New("缺少参数 command"))
	prompt, ok := pe.AsPrompt()
	if !ok {
		t.Fatal("AsPrompt 应返回 true")
	}
	if !strings.Contains(prompt, "### error_type: tool") {
		t.Errorf("ParamError 应为 tool 类型：%q", prompt)
	}
	if !strings.Contains(prompt, "缺少参数 command") {
		t.Errorf("应包含原始错误详情：%q", prompt)
	}
	if !strings.Contains(prompt, "工具调用参数错误或缺失") {
		t.Errorf("应包含修正建议：%q", prompt)
	}
}

func TestFilePathErrorAsPrompt(t *testing.T) {
	e := NewErrorWithPrompt(&FilePathError{}, errors.New("path escapes"))
	prompt, ok := e.AsPrompt()
	if !ok {
		t.Fatal("AsPrompt 应返回 true")
	}
	if !strings.Contains(prompt, "检查文件路径格式标准") {
		t.Errorf("FilePathError suggestion 不符：%q", prompt)
	}
}

func TestFileNotExistErrorAsPrompt(t *testing.T) {
	e := NewErrorWithPrompt(&FileNotExistError{}, errors.New("no such file"))
	prompt, ok := e.AsPrompt()
	if !ok {
		t.Fatal("AsPrompt 应返回 true")
	}
	if !strings.Contains(prompt, "新建文件请使用 write_file") {
		t.Errorf("FileNotExistError suggestion 应引导 write_file：%q", prompt)
	}
}

func TestEditBatchValidationErrorSuggestion(t *testing.T) {
	e := NewErrorWithPrompt(&EditBatchValidationError{}, errors.New("batch invalid"))
	prompt, _ := e.AsPrompt()
	for _, want := range []string{"本次批量编辑未修改文件", "read_file", "行首和行尾空白字符数量", "LF/CRLF"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("EditBatchValidationError suggestion 缺少 %q：%q", want, prompt)
		}
	}
}

func TestEditPartialErrorSuggestion(t *testing.T) {
	e := NewErrorWithPrompt(&EditPartialError{}, errors.New("partial edit"))
	prompt, _ := e.AsPrompt()
	for _, want := range []string{"部分替换成功", "禁止沿用旧 offset", "read_file"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("EditPartialError suggestion 缺少 %q：%q", want, prompt)
		}
	}
}

func TestFileIOErrorAsPromptIsOSType(t *testing.T) {
	e := NewErrorWithPrompt(&FileIOError{}, errors.New("permission denied"))
	prompt, _ := e.AsPrompt()
	if !strings.Contains(prompt, "### error_type: os") {
		t.Errorf("FileIOError 应为 os 类型：%q", prompt)
	}
}

func TestBashExecuteErrorAsPrompt(t *testing.T) {
	e := NewErrorWithPrompt(&BashExecuteError{}, errors.New("fork failed"))
	prompt, ok := e.AsPrompt()
	if !ok {
		t.Fatal("AsPrompt 应返回 true")
	}
	if !strings.Contains(prompt, "Bash进程执行发生系统故障") {
		t.Errorf("BashExecuteError 应带系统故障说明：%q", prompt)
	}
	if !strings.Contains(prompt, "不要反复原样重试") {
		t.Errorf("BashExecuteError suggestion 应阻止盲目重试：%q", prompt)
	}
}

// 保证各错误类型均满足 ErrorWithPrompt 接口（编译期契约）。
var _ ErrorWithPrompt = &ParamError{}
var _ ErrorWithPrompt = &FilePathError{}
var _ ErrorWithPrompt = &FileNotExistError{}
var _ ErrorWithPrompt = &EditBatchValidationError{}
var _ ErrorWithPrompt = &EditPartialError{}
var _ ErrorWithPrompt = &FileIOError{}
var _ ErrorWithPrompt = &BashExecuteError{}
