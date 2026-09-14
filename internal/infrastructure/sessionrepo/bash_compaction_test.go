package sessionrepo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/compactor"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/artifactstore"
	"github.com/mikellxy/laxcode/internal/infrastructure/shell"
)

// 真实 Bash → Registry → Message → 压缩 → SQLite/JSONL → 重启后续读原始日志。
func TestBashArtifactAndCompactContentSurviveCompactionAndReload(t *testing.T) {
	repo, root := newTestRepo(t)
	ctx := context.Background()
	s := createSystem(t, repo, "bash-output", "system")
	store := artifactstore.New(filepath.Join(root, "artifacts"))
	runner := shell.New()
	t.Cleanup(func() { _ = runner.Close() })
	reg := tools.NewDefaultRegistry(nil)
	reg.Register(tools.NewBashTool(root, runner, store, s.ID))
	args, err := json.Marshal(map[string]any{"command": "for ((i=0; i<1000; i++)); do printf '0123456789'; done; printf 'TAIL'; exit 7", "timeout_ms": 90000})
	if err != nil {
		t.Fatal(err)
	}
	call := sharedkernel.ToolCall{ID: "bash-first", Name: tools.ToolBash, Arguments: args}
	appendMessage(t, repo, s, &sharedkernel.Message{Role: sharedkernel.RoleAssistant, ToolCalls: []sharedkernel.ToolCall{call}})
	result := reg.Execute(ctx, &call)
	if result.Error != nil || result.Artifact == nil {
		t.Fatalf("execute: %+v", result)
	}
	original := tools.ToolResultAsMsg(result)
	appendMessage(t, repo, s, original)
	for _, id := range []string{"recent-1", "recent-2", "recent-3"} {
		appendMessage(t, repo, s, &sharedkernel.Message{Role: sharedkernel.RoleAssistant, ToolCalls: []sharedkernel.ToolCall{{ID: id, Name: "other", Arguments: json.RawMessage(`{}`)}}})
		appendMessage(t, repo, s, &sharedkernel.Message{Role: sharedkernel.RoleTool, ToolCallID: id, Content: "short result"})
	}
	if len(compactor.ArtifactCandidates(s.Messages)) != 0 {
		t.Fatal("Bash output would be rearchived from preview")
	}
	candidate := s.Clone()
	compressed, saved, err := compactor.SimpleCompactor.Compress(candidate.Messages, 1)
	if err != nil || saved <= 0 {
		t.Fatalf("compress: saved=%d err=%v", saved, err)
	}
	candidate.Messages = compressed
	if err := candidate.AdvanceMemoryGeneration(); err != nil {
		t.Fatal(err)
	}
	revision, err := repo.CommitNextMemoryGeneration(ctx, s.ID, candidate.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	candidate.Revision = revision
	if err := runner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSqliteSessionRepo(filepath.Join(root, "sessions.db"), filepath.Join(root, "history"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := reopened.GetRequestContext(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, candidate.Snapshot()) {
		t.Fatal("persisted request context lost metadata")
	}
	msg := loaded.Messages[2]
	if msg.Artifact == nil || *msg.Artifact != *result.Artifact || !strings.Contains(msg.Content, "exit_code:7") || !strings.Contains(msg.Content, "stdout_truncated:true") || !strings.Contains(msg.Content, msg.Artifact.ID) {
		t.Fatalf("compressed result: %+v", msg)
	}
	reader := tools.NewReadArtifactTool(store, s.ID)
	var assembled strings.Builder
	for offset := 0; ; {
		args, _ := json.Marshal(map[string]any{"artifact_id": msg.Artifact.ID, "offset": offset, "limit": 4000})
		raw, err := reader.Execute(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		var page tools.ArtifactPage
		if err := json.Unmarshal([]byte(raw), &page); err != nil {
			t.Fatal(err)
		}
		assembled.WriteString(page.Content)
		if page.EOF {
			break
		}
		offset = page.NextOffset
	}
	if assembled.String() != strings.Repeat("0123456789", 1000)+"TAIL" {
		t.Fatal("artifact contains preview instead of full output")
	}
	history, err := os.ReadFile(filepath.Join(root, "history", s.ID, historyFile))
	if err != nil {
		t.Fatal(err)
	}
	var historical sharedkernel.Message
	if err := json.Unmarshal([]byte(strings.Split(strings.TrimSpace(string(history)), "\n")[2]), &historical); err != nil {
		t.Fatal(err)
	}
	if historical.Content != original.Content || historical.CompactContent != original.CompactContent || historical.Artifact == nil || *historical.Artifact != *original.Artifact {
		t.Fatal("immutable original message metadata lost")
	}
}

func TestMigrateAddsCompactContentToExistingDatabase(t *testing.T) {
	repo, root := newTestRepo(t)
	s := createSystem(t, repo, "existing", "old system")
	// 模拟升级前的 schema，保留已有消息。
	if err := repo.db.Exec("ALTER TABLE messages DROP COLUMN compact_content").Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSqliteSessionRepo(filepath.Join(root, "sessions.db"), filepath.Join(root, "history"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := reopened.GetRequestContext(context.Background(), s.ID)
	if err != nil || !reflect.DeepEqual(loaded, s.Snapshot()) {
		t.Fatalf("migration changed old messages: %v %+v", err, loaded)
	}
	if !reopened.db.Migrator().HasColumn(&messageModel{}, "compact_content") {
		t.Fatal("missing compact_content column")
	}
}
