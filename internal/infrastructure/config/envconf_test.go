package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseEnvConf(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []string
		wantErr string
	}{
		{name: "常规键值与注释空行", input: "# 代理\n\nHTTP_PROXY=http://127.0.0.1:7890\n  \nNO_PROXY=localhost,127.0.0.1\n", want: []string{"HTTP_PROXY=http://127.0.0.1:7890", "NO_PROXY=localhost,127.0.0.1"}},
		{name: "容忍CRLF与export前缀与空值", input: "export HTTPS_PROXY=https://p:8443\r\nexport EMPTY=\r\n", want: []string{"HTTPS_PROXY=https://p:8443", "EMPTY="}},
		{name: "等号两侧空白与成对引号剥离", input: "A = \"quoted value\"\nB='single'\n", want: []string{"A=quoted value", "B=single"}},
		{name: "不成对引号原样保留", input: "C=\"unmatched\n", want: []string{"C=\"unmatched"}},
		{name: "重复键后者覆盖且位置稳定", input: "A=1\nB=2\nA=3\n", want: []string{"A=3", "B=2"}},
		{name: "值内含等号原样保留", input: "CONNECTION=host=db port=5432\n", want: []string{"CONNECTION=host=db port=5432"}},
		{name: "缺等号报错并带行号", input: "GOOD=1\nBROKEN_LINE\n", wantErr: "第 2 行"},
		{name: "键以数字开头报错", input: "1BAD=x\n", wantErr: "非法"},
		{name: "键含空格报错", input: "BAD KEY=x\n", wantErr: "非法"},
		{name: "空键报错", input: "=x\n", wantErr: "非法"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseEnvConf([]byte(tt.input))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("应报错含 %q，实际 err=%v, entries=%v", tt.wantErr, err, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseEnvConf: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("条数不符：got %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("第 %d 条不符：got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// writeEnvConf 在临时主目录写入 .laxcode/env.conf，返回主目录路径。
func writeEnvConf(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".laxcode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "env.conf"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return home
}

func TestApplyEnvFile(t *testing.T) {
	t.Run("文件不存在是no-op", func(t *testing.T) {
		if err := ApplyEnvFile(t.TempDir()); err != nil {
			t.Fatalf("ApplyEnvFile: %v", err)
		}
	})
	t.Run("注入并覆盖继承值", func(t *testing.T) {
		t.Setenv("LAXCODE_ENVCONF_PROBE", "inherited")
		home := writeEnvConf(t, "LAXCODE_ENVCONF_PROBE=from-file\n")
		if err := ApplyEnvFile(home); err != nil {
			t.Fatalf("ApplyEnvFile: %v", err)
		}
		if got := os.Getenv("LAXCODE_ENVCONF_PROBE"); got != "from-file" {
			t.Fatalf("应覆盖继承值：got %q", got)
		}
	})
	t.Run("解析错误fail-fast并带行号", func(t *testing.T) {
		home := writeEnvConf(t, "GOOD=1\nBROKEN_LINE\n")
		err := ApplyEnvFile(home)
		if err == nil || !strings.Contains(err.Error(), "第 2 行") {
			t.Fatalf("应报错含行号，实际 %v", err)
		}
	})
}

// TestApplyEnvFileBashInheritance 验证端到端语义：env.conf 注入进程环境后，
// 以 cmd.Env 为空方式派生的 bash 子进程（bash 工具的实际路径）能看到注入值。
func TestApplyEnvFileBashInheritance(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("端到端用例依赖 bash")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash 不可用")
	}
	t.Setenv("LAXCODE_ENVCONF_E2E", "inherited")
	home := writeEnvConf(t, "LAXCODE_ENVCONF_E2E=from-conf\n")
	if err := ApplyEnvFile(home); err != nil {
		t.Fatalf("ApplyEnvFile: %v", err)
	}
	out, err := exec.Command(bash, "-c", `printf %s "$LAXCODE_ENVCONF_E2E"`).Output()
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if string(out) != "from-conf" {
		t.Fatalf("bash 子进程应看到 env.conf 注入值，实际 %q", out)
	}
}
