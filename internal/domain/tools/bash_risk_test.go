package tools

import "testing"

func TestAssessBashRisk(t *testing.T) {
	tests := []struct {
		name, command string
		risky         bool
	}{
		{"read only", "rg -n 'foo' internal | head -20", false},
		{"quoted dangerous text", "printf '%s' 'rm -rf /'", false},
		{"delete", "rm -rf build", true},
		{"substitution", "echo $(rm -rf build)", true},
		{"redirect", "printf hello > config.json", true},
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
			reason, risky := AssessBashRisk(tt.command)
			if risky != tt.risky || risky && reason == "" {
				t.Fatalf("AssessBashRisk(%q) = (%q, %v), want risky=%v", tt.command, reason, risky, tt.risky)
			}
		})
	}
}
