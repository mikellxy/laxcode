package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/infrastructure/workfs"
)

// TestFileAccessConfirmation 覆盖 read/write/edit 沙箱外访问的三态判定：
// root 内返回 nil 不打扰；越界返回确认（授权目标=解析后路径、文案含真实路径）；
// 参数非法交由 Execute 报标准错误。
func TestFileAccessConfirmation(t *testing.T) {
	workDir, outside := t.TempDir(), t.TempDir()
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(workDir, "in.txt"), []byte("in"), 0o644); err != nil {
		t.Fatalf("seed in-root file: %v", err)
	}
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	t.Run("read root内不打扰", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"path": "in.txt"})
		c, err := NewReadFileTool(workDir, workfs.New()).Confirmation(ctx, args)
		if err != nil || c != nil {
			t.Fatalf("应返回 nil 确认，实际 %+v, %v", c, err)
		}
	})

	t.Run("read 沙箱外发起确认且授权目标为解析路径", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{"path": outsideFile})
		c, err := NewReadFileTool(workDir, workfs.New()).Confirmation(ctx, args)
		if err != nil {
			t.Fatalf("Confirmation: %v", err)
		}
		if c == nil || c.Kind != FileAccessConfirmationKind || len(c.Grants) != 1 || c.Grants[0] != outsideFile {
			t.Fatalf("确认不符：%+v", c)
		}
		if !strings.Contains(c.Content, "secret.txt") {
			t.Fatalf("文案应包含目标：%q", c.Content)
		}
	})

	t.Run("read root内symlink指向外部也确认并展示真实路径", func(t *testing.T) {
		link := filepath.Join(workDir, "link.txt")
		if err := os.Symlink(outsideFile, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		args, _ := json.Marshal(map[string]any{"path": "link.txt"})
		c, err := NewReadFileTool(workDir, workfs.New()).Confirmation(ctx, args)
		if err != nil {
			t.Fatalf("Confirmation: %v", err)
		}
		if c == nil {
			t.Fatal("symlink 指向沙箱外应发起确认")
		}
		// 真实路径经 EvalSymlinks（macOS /tmp → /private/tmp），不能逐字比较
		if !strings.Contains(c.Content, "secret.txt") || strings.Count(c.Content, "secret.txt") < 1 {
			t.Fatalf("文案应展示真实路径：%q", c.Content)
		}
	})

	t.Run("read 参数非法交由Execute报错", func(t *testing.T) {
		c, err := NewReadFileTool(workDir, workfs.New()).Confirmation(ctx, json.RawMessage(`{"path":""}`))
		if err != nil || c != nil {
			t.Fatalf("应返回 nil 确认，实际 %+v, %v", c, err)
		}
	})

	t.Run("write root内不打扰", func(t *testing.T) {
		args, _ := json.Marshal(map[string]string{"path": "new.txt", "content": "x"})
		c, err := NewWriteFileTool(workDir, workfs.New()).Confirmation(ctx, args)
		if err != nil || c != nil {
			t.Fatalf("应返回 nil 确认，实际 %+v, %v", c, err)
		}
	})

	t.Run("write 沙箱外发起确认（新文件也解析真实路径）", func(t *testing.T) {
		target := filepath.Join(outside, "created.txt")
		args, _ := json.Marshal(map[string]string{"path": target, "content": "x"})
		c, err := NewWriteFileTool(workDir, workfs.New()).Confirmation(ctx, args)
		if err != nil {
			t.Fatalf("Confirmation: %v", err)
		}
		if c == nil || len(c.Grants) != 1 || c.Grants[0] != target {
			t.Fatalf("确认不符：%+v", c)
		}
	})

	t.Run("edit 沙箱外发起确认", func(t *testing.T) {
		args, _ := json.Marshal(editFileArgs{Path: outsideFile, Edits: []editFileChangeArgs{{OldText: strPtr("secret"), NewText: strPtr("public")}}})
		c, err := NewEditFileTool(workDir, workfs.New()).Confirmation(ctx, args)
		if err != nil {
			t.Fatalf("Confirmation: %v", err)
		}
		if c == nil || len(c.Grants) != 1 || c.Grants[0] != outsideFile {
			t.Fatalf("确认不符：%+v", c)
		}
	})
}

func strPtr(s string) *string { return &s }

// TestWriteOutsideSandboxGrantFlow 验证 Execute 的放行与维持失败：无授权时
// 沙箱错误原样返回；授权目标精确匹配时放行写入。
func TestWriteOutsideSandboxGrantFlow(t *testing.T) {
	workDir, outside := t.TempDir(), t.TempDir()
	w := NewWriteFileTool(workDir, workfs.New())
	target := filepath.Join(outside, "granted.txt")
	args, _ := json.Marshal(map[string]string{"path": target, "content": "granted"})

	t.Run("无授权维持沙箱失败", func(t *testing.T) {
		if _, err := w.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "outside configured write roots") {
			t.Fatalf("应维持越界失败，实际 %v", err)
		}
	})

	t.Run("授权目标精确匹配后放行", func(t *testing.T) {
		ctx := WithApprovalGrants(context.Background(), []string{target})
		if _, err := w.Execute(ctx, args); err != nil {
			t.Fatalf("授权后应放行: %v", err)
		}
		b, err := os.ReadFile(target)
		if err != nil || string(b) != "granted" {
			t.Fatalf("写入结果不符: %v %q", err, b)
		}
	})

	t.Run("授权不覆盖其他目标", func(t *testing.T) {
		other := filepath.Join(outside, "other.txt")
		otherArgs, _ := json.Marshal(map[string]string{"path": other, "content": "x"})
		ctx := WithApprovalGrants(context.Background(), []string{target})
		if _, err := w.Execute(ctx, otherArgs); err == nil {
			t.Fatal("未授权目标应维持失败")
		}
	})
}

// TestGrantAllows 覆盖 ctx 授权通道的基本语义。
func TestGrantAllows(t *testing.T) {
	ctx := WithApprovalGrants(context.Background(), []string{"/a/b"})
	if !GrantAllows(ctx, "/a/b") {
		t.Fatal("授权目标应命中")
	}
	if GrantAllows(ctx, "/a/c") {
		t.Fatal("未授权目标不应命中")
	}
	if GrantAllows(context.Background(), "/a/b") {
		t.Fatal("空 ctx 不应命中")
	}
}
