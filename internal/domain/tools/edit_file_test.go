package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/infrastructure/workfs"
)

func marshalEditArgs(t *testing.T, path string, edits ...map[string]string) json.RawMessage {
	t.Helper()
	args, err := json.Marshal(map[string]any{"path": path, "edits": edits})
	if err != nil {
		t.Fatalf("marshal edit args: %v", err)
	}
	return args
}

func TestEditFileToolBatchExact(t *testing.T) {
	workDir := t.TempDir()
	ctx := context.Background()
	e := NewEditFileTool(workDir, workfs.New())

	seed := func(rel, content string) string {
		t.Helper()
		target := filepath.Join(workDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("seed mkdir: %v", err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Fatalf("seed file: %v", err)
		}
		return target
	}
	read := func(target string) string {
		t.Helper()
		content, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("read target: %v", err)
		}
		return string(content)
	}

	t.Run("批量替换按 offset 倒序执行并按请求顺序报告", func(t *testing.T) {
		target := seed("batch.txt", "first\nsecond\nthird\n")
		out, err := e.Execute(ctx, marshalEditArgs(t, "batch.txt",
			map[string]string{"old_text": "second", "new_text": "SECOND-LONG"},
			map[string]string{"old_text": "first", "new_text": "F"},
			map[string]string{"old_text": "third\n", "new_text": ""},
		))
		if err != nil {
			t.Fatalf("edit: %v", err)
		}
		if got, want := read(target), "F\nSECOND-LONG\n"; got != want {
			t.Fatalf("content mismatch: got %q, want %q", got, want)
		}
		want := "已在 batch.txt 完成 3/3 处精确替换\n#1 第 2-2 行\n#2 第 1-1 行\n#3 第 3-3 行"
		if out != want {
			t.Fatalf("output mismatch:\n got  %q\n want %q", out, want)
		}
	})

	t.Run("行首空白不一致时预检失败且零写入", func(t *testing.T) {
		original := "if ok {\n\treturn nil\n}\n"
		target := seed("indent.go", original)
		_, err := e.Execute(ctx, marshalEditArgs(t, "indent.go",
			map[string]string{"old_text": "if ok {\n    return nil\n}", "new_text": "changed"},
		))
		if err == nil {
			t.Fatal("expected exact-match error")
		}
		if got := read(target); got != original {
			t.Fatalf("preflight failure modified file: %q", got)
		}
		prompt := asPromptErr(t, err)
		for _, want := range []string{"read_file", "行首和行尾空白字符数量", "空格与 Tab", "LF/CRLF"} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("prompt missing %q: %q", want, prompt)
			}
		}
	})

	t.Run("换行符不一致不再归一化匹配", func(t *testing.T) {
		original := "a\r\nb\r\n"
		target := seed("crlf.txt", original)
		_, err := e.Execute(ctx, marshalEditArgs(t, "crlf.txt",
			map[string]string{"old_text": "a\nb", "new_text": "x"},
		))
		if err == nil || !strings.Contains(err.Error(), "未找到逐字节精确匹配") {
			t.Fatalf("expected CRLF exact-match failure, got %v", err)
		}
		if got := read(target); got != original {
			t.Fatalf("preflight failure modified file: %q", got)
		}
	})

	t.Run("任一项未命中则整个批次零写入", func(t *testing.T) {
		original := "alpha\nbeta\n"
		target := seed("missing.txt", original)
		_, err := e.Execute(ctx, marshalEditArgs(t, "missing.txt",
			map[string]string{"old_text": "alpha", "new_text": "A"},
			map[string]string{"old_text": "missing", "new_text": "M"},
		))
		if err == nil || !strings.Contains(err.Error(), "#2 old_text 未找到") {
			t.Fatalf("expected indexed preflight error, got %v", err)
		}
		if got := read(target); got != original {
			t.Fatalf("preflight failure modified file: %q", got)
		}
	})

	t.Run("多处精确匹配列出行号且零写入", func(t *testing.T) {
		original := "same\nother\nsame\n"
		target := seed("duplicate.txt", original)
		_, err := e.Execute(ctx, marshalEditArgs(t, "duplicate.txt",
			map[string]string{"old_text": "same", "new_text": "changed"},
		))
		if err == nil || !strings.Contains(err.Error(), "匹配到 2 处（第 1、3 行）") {
			t.Fatalf("expected multi-match line numbers, got %v", err)
		}
		if got := read(target); got != original {
			t.Fatalf("preflight failure modified file: %q", got)
		}
	})

	t.Run("重叠精确匹配区间被预检拒绝", func(t *testing.T) {
		original := "abcdef"
		target := seed("overlap.txt", original)
		_, err := e.Execute(ctx, marshalEditArgs(t, "overlap.txt",
			map[string]string{"old_text": "abcd", "new_text": "AB"},
			map[string]string{"old_text": "cdef", "new_text": "CD"},
		))
		if err == nil || !strings.Contains(err.Error(), "#1 与 #2") || !strings.Contains(err.Error(), "区间重叠") {
			t.Fatalf("expected overlap error, got %v", err)
		}
		if got := read(target); got != original {
			t.Fatalf("overlap preflight modified file: %q", got)
		}
	})

	t.Run("重叠出现也视为多处匹配", func(t *testing.T) {
		original := "aaa"
		target := seed("overlapping-hits.txt", original)
		_, err := e.Execute(ctx, marshalEditArgs(t, "overlapping-hits.txt",
			map[string]string{"old_text": "aa", "new_text": "x"},
		))
		if err == nil || !strings.Contains(err.Error(), "匹配到 2 处") {
			t.Fatalf("expected overlapping occurrences to be ambiguous, got %v", err)
		}
		if got := read(target); got != original {
			t.Fatalf("ambiguous preflight modified file: %q", got)
		}
	})

	t.Run("纯空白 old_text 可在唯一匹配时精确替换", func(t *testing.T) {
		target := seed("whitespace.txt", "a\n \nb\n")
		_, err := e.Execute(ctx, marshalEditArgs(t, "whitespace.txt",
			map[string]string{"old_text": " \n", "new_text": "\t\n"},
		))
		if err != nil {
			t.Fatalf("edit whitespace: %v", err)
		}
		if got, want := read(target), "a\n\t\nb\n"; got != want {
			t.Fatalf("content mismatch: got %q, want %q", got, want)
		}
	})

	t.Run("目标不存在时指引使用 write_file", func(t *testing.T) {
		_, err := e.Execute(ctx, marshalEditArgs(t, "nope.txt",
			map[string]string{"old_text": "a", "new_text": "b"},
		))
		if err == nil || !strings.Contains(err.Error(), "write_file") {
			t.Fatalf("expected not-exist error pointing to write_file, got %v", err)
		}
	})

	t.Run("路径穿越和绝对路径被拒绝", func(t *testing.T) {
		for _, path := range []string{"../../evil.txt", "/tmp/laxcode_edit_file_evil.txt"} {
			_, err := e.Execute(ctx, marshalEditArgs(t, path,
				map[string]string{"old_text": "a", "new_text": "b"},
			))
			if err == nil {
				t.Fatalf("path %q should be rejected", path)
			}
		}
	})
}

type changingEditFS struct {
	content   string
	readCount int
}

func (f *changingEditFS) OpenRead(string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(f.content)), nil
}

func (f *changingEditFS) ReadFile(string) ([]byte, error) {
	f.readCount++
	// 第一次读取用于预检，第二次读取用于执行高 offset 项。高位项写入后，
	// 在第三次读取前模拟外部修改低 offset 内容。
	if f.readCount == 3 {
		f.content = strings.Replace(f.content, "alpha", "changed", 1)
	}
	return []byte(f.content), nil
}

func (f *changingEditFS) WriteFile(_ string, content []byte) error {
	f.content = string(content)
	return nil
}

func TestEditFileToolPartialSuccessOnOffsetChange(t *testing.T) {
	fs := &changingEditFS{content: "alpha\nmiddle\nomega\n"}
	e := NewEditFileTool(t.TempDir(), fs)

	out, err := e.Execute(context.Background(), marshalEditArgs(t, "partial.txt",
		map[string]string{"old_text": "alpha", "new_text": "A"},
		map[string]string{"old_text": "omega", "new_text": "O"},
	))
	if err == nil {
		t.Fatal("expected partial edit error")
	}
	var partial *EditPartialError
	if !errors.As(err, &partial) {
		t.Fatalf("error type = %T, want *EditPartialError", err)
	}
	if got, want := fs.content, "changed\nmiddle\nO\n"; got != want {
		t.Fatalf("partial content mismatch: got %q, want %q", got, want)
	}
	for _, want := range []string{
		"完成 1/2 处精确替换",
		"#2 第 3-3 行",
		"处理 #1 时停止",
		"立即重新 read_file",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("partial output missing %q: %q", want, out)
		}
	}
	prompt := asPromptErr(t, err)
	if !strings.Contains(prompt, "禁止沿用旧 offset") || !strings.Contains(prompt, "read_file") {
		t.Fatalf("partial prompt should force reread: %q", prompt)
	}
}

type failingWriteEditFS struct {
	content string
}

func (f *failingWriteEditFS) OpenRead(string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(f.content)), nil
}

func (f *failingWriteEditFS) ReadFile(string) ([]byte, error) {
	return []byte(f.content), nil
}

func (f *failingWriteEditFS) WriteFile(string, []byte) error {
	return errors.New("disk full")
}

func TestEditFileToolWriteFailureReportsUncertainPartialState(t *testing.T) {
	fs := &failingWriteEditFS{content: "old\n"}
	e := NewEditFileTool(t.TempDir(), fs)

	out, err := e.Execute(context.Background(), marshalEditArgs(t, "write-failure.txt",
		map[string]string{"old_text": "old", "new_text": "new"},
	))
	if err == nil {
		t.Fatal("expected write failure")
	}
	var partial *EditPartialError
	if !errors.As(err, &partial) {
		t.Fatalf("error type = %T, want *EditPartialError", err)
	}
	for _, want := range []string{"尚未写入任何替换", "写入文件失败: disk full", "重新 read_file"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %q", want, out)
		}
	}
}

func TestEditFileToolDefinitionAndParams(t *testing.T) {
	e := NewEditFileTool(t.TempDir(), workfs.New())

	def := e.Definition()
	properties, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T", def.Parameters["properties"])
	}
	edits, ok := properties["edits"].(map[string]any)
	if !ok || edits["type"] != "array" || edits["minItems"] != 1 {
		t.Fatalf("invalid edits schema: %#v", edits)
	}
	if _, exists := properties["old_text"]; exists {
		t.Fatalf("legacy top-level old_text should not remain: %#v", properties)
	}

	info := e.BeforeExecInfo(marshalEditArgs(t, "a.go",
		map[string]string{"old_text": "a", "new_text": "b"},
		map[string]string{"old_text": "c", "new_text": "d"},
	))
	if info != "edit_file(path=a.go, edits=2)" {
		t.Fatalf("BeforeExecInfo = %q", info)
	}

	for _, tc := range []struct {
		name string
		args json.RawMessage
	}{
		{name: "invalid json", args: json.RawMessage(`{bad`)},
		{name: "missing path", args: json.RawMessage(`{"edits":[{"old_text":"a","new_text":"b"}]}`)},
		{name: "missing edits", args: json.RawMessage(`{"path":"x.txt"}`)},
		{name: "empty edits", args: json.RawMessage(`{"path":"x.txt","edits":[]}`)},
		{name: "missing old text", args: json.RawMessage(`{"path":"x.txt","edits":[{"new_text":"b"}]}`)},
		{name: "empty old text", args: json.RawMessage(`{"path":"x.txt","edits":[{"old_text":"","new_text":"b"}]}`)},
		{name: "missing new text", args: json.RawMessage(`{"path":"x.txt","edits":[{"old_text":"a"}]}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.Execute(context.Background(), tc.args); err == nil {
				t.Fatal("expected parameter error")
			} else {
				var paramErr *ParamError
				if !errors.As(err, &paramErr) {
					t.Fatalf("error type = %T, want *ParamError", err)
				}
			}
		})
	}
}
