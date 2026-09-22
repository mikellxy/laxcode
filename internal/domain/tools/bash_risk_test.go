package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAssessBashRisk(t *testing.T) {
	tests := []struct {
		name, command string
		risky         bool
	}{
		{"read only", "rg -n 'foo' internal | head -20", false},
		{"quoted dangerous text", "printf '%s' 'rm -rf /'", false},
		{"delete", "rm -rf build", true},
		{"substitution", "echo $(rm -rf build)", true},
		{"redirect relative workdir", "printf hello > config.json", false},
		{"redirect abs workdir", "printf hi > /ws/logs/app.log", false},
		{"redirect append workdir", "cmd >> log.txt", false},
		{"redirect outside", "printf hi > /etc/hosts", true},
		{"redirect clobber outside", "cmd >| /etc/hosts", true},
		{"redirect parent escape", "printf hi > ../outside.log", true},
		{"redirect tmp escape", "printf hi > /tmp/../etc/hosts", true},
		{"redirect dev null", "cmd > /dev/null 2>&1", false},
		{"redirect tmp allowed", "python3 server.py > /tmp/srv.log 2>&1 & echo \"pid=$!\"", false},
		{"redirect dynamic target", "printf hi > $OUT", true},
		{"redirect home target", "printf hi > ~/notes.txt", true},
		{"redirect dup to file outside", "cmd >&/etc/hosts", true},
		{"redirect all outside", "cmd &>/etc/hosts", true},
		{"redirect all workdir", "cmd &>all.log", false},
		{"redirect read write workdir", "cmd <> data.txt", false},
		{"fd dup", "cmd 2>&1", false},
		{"fd dup stderr", "cmd >&2", false},
		{"fd close", "cmd >&-", false},
		{"fd dup input", "cmd <&0", false},
		{"input redirect", "cmd < input.txt", false},
		{"heredoc", "cat <<EOF\nhello\nEOF", false},
		{"herestring", "cat <<<hello", false},
		{"hard reset", "git reset --hard HEAD", true},
		{"git global option", "git -C repo reset --hard HEAD", true},
		{"in place edit", "sed -i 's/a/b/' config.yml", true},
		{"quoted shell", "bash -c 'rm -rf build'", true},
		{"dynamic command", "$action build", true},
		{"dynamic git option", "git reset $mode", true},
		{"wrapper", "env rm -rf build", true},
		{"syntax error", "echo $(", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, risky := AssessBashRisk(tt.command, "/ws")
			if risky != tt.risky || risky && reason == "" {
				t.Fatalf("AssessBashRisk(%q) = (%q, %v), want risky=%v", tt.command, reason, risky, tt.risky)
			}
		})
	}
}

func TestAssessBashRiskRedirectWithoutWorkDir(t *testing.T) {
	if _, risky := AssessBashRisk("cmd > out.log", ""); !risky {
		t.Fatal("relative redirect target without workDir must require confirmation")
	}
	if _, risky := AssessBashRisk("cmd > /tmp/out.log", ""); risky {
		t.Fatal("absolute tmp target must stay allowed without workDir")
	}
}

func TestAssessBashRiskSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if reason, risky := AssessBashRisk("echo hi > "+link, root); !risky || reason == "" {
		t.Fatalf("symlink escape must be flagged, got (%q, %v)", reason, risky)
	}
	if _, risky := AssessBashRisk("echo hi > "+filepath.Join(root, "ok.txt"), root); risky {
		t.Fatal("plain workdir target must be allowed")
	}
}
