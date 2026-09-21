package sessionrepo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/compactor"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func newTestRepo(t *testing.T) (*SqliteSessionRepo, string) {
	t.Helper()
	root := t.TempDir()
	repo, err := NewSqliteSessionRepo(filepath.Join(root, "sessions.db"), filepath.Join(root, "history"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo, root
}

func createSystem(t *testing.T, repo *SqliteSessionRepo, id, content string) *session.Session {
	t.Helper()
	s := session.NewSession(id)
	sys := s.UpsertSysMessage(content)
	revision, err := repo.CommitCreateMessage(context.Background(), id, s.Snapshot(), sys, sys)
	if err != nil {
		t.Fatal(err)
	}
	s.Revision = revision
	return s
}

func appendMessage(t *testing.T, repo *SqliteSessionRepo, s *session.Session, msg *sharedkernel.Message) {
	t.Helper()
	candidate, err := s.WithAppendedMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := repo.CommitCreateMessage(context.Background(), s.ID, candidate.Snapshot(), *msg, *msg)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Revision = revision
	*s = *candidate
}

func TestListOriginalHistoryFiltersProjectsAndPaginates(t *testing.T) {
	repo, _ := newTestRepo(t)
	s := createSystem(t, repo, "history-page", "secret system prompt")

	user := s.BuildUserMessage("question")
	appendMessage(t, repo, s, &user)
	assistant := sharedkernel.Message{
		Role: sharedkernel.RoleAssistant, Content: "working", ReasoningContent: "thinking",
		ToolCalls: []sharedkernel.ToolCall{{ID: "call-1", Name: "bash"}},
	}
	appendMessage(t, repo, s, &assistant)
	tool := sharedkernel.Message{
		Role: sharedkernel.RoleTool, ToolCallID: "call-1", Content: "private tool output",
		DisplayContent: "bash(go test ./...)",
	}
	appendMessage(t, repo, s, &tool)
	final := sharedkernel.Message{Role: sharedkernel.RoleAssistant, Content: "answer", ReasoningContent: "done"}
	appendMessage(t, repo, s, &final)

	page, found, err := repo.ListOriginalHistory(context.Background(), s.ID, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !found || !page.HasMore || len(page.Messages) != 2 {
		t.Fatalf("unexpected first page: found=%v page=%+v", found, page)
	}
	if page.Messages[0].Role != sharedkernel.RoleTool || page.Messages[0].ToolSummary != "bash(go test ./...)" || page.Messages[0].Content != "" {
		t.Fatalf("tool projection leaked or lost fields: %+v", page.Messages[0])
	}
	if page.Messages[1].Content != "answer" || page.Messages[1].ReasoningContent != "done" {
		t.Fatalf("assistant projection mismatch: %+v", page.Messages[1])
	}

	older, found, err := repo.ListOriginalHistory(context.Background(), s.ID, page.Messages[0].Seq, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !found || older.HasMore || len(older.Messages) != 2 {
		t.Fatalf("unexpected older page: found=%v page=%+v", found, older)
	}
	if older.Messages[0].Role != sharedkernel.RoleUser || older.Messages[0].Content != "question" {
		t.Fatalf("user projection mismatch: %+v", older.Messages[0])
	}
	if older.Messages[1].Role != sharedkernel.RoleAssistant || older.Messages[1].ReasoningContent != "thinking" {
		t.Fatalf("intermediate assistant projection mismatch: %+v", older.Messages[1])
	}

	_, found, err = repo.ListOriginalHistory(context.Background(), "missing", 0, 10)
	if err != nil || found {
		t.Fatalf("missing session: found=%v err=%v", found, err)
	}
}

func TestCreateAndListSessionsByUser(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	userID := "11111111-1111-4111-8111-111111111111"
	otherUserID := "22222222-2222-4222-8222-222222222222"

	for _, id := range []string{"session-a", "session-b", "session-c"} {
		if _, err := repo.CreateSession(ctx, id, userID, ""); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := repo.CreateSession(ctx, "other-session", otherUserID, ""); err != nil {
		t.Fatal(err)
	}

	page, err := repo.ListSessions(ctx, userID, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !page.HasMore || len(page.Sessions) != 2 || page.Sessions[0].ID != "session-c" || page.Sessions[1].ID != "session-b" {
		t.Fatalf("unexpected first session page: %+v", page)
	}
	older, err := repo.ListSessions(ctx, userID, page.Sessions[1].ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if older.HasMore || len(older.Sessions) != 1 || older.Sessions[0].ID != "session-a" {
		t.Fatalf("unexpected older session page: %+v", older)
	}

	// 显式创建的空 session 必须仍能走原有首次 system 消息提交路径。
	s := session.NewSession("session-a")
	snapshot, err := repo.GetRequestContext(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	sys := s.UpsertSysMessage("system")
	revision, err := repo.CommitCreateMessage(ctx, s.ID, s.Snapshot(), sys, sys)
	if err != nil {
		t.Fatalf("append first system message to explicit session: %v", err)
	}
	if revision != 1 {
		t.Fatalf("first revision=%d, want 1", revision)
	}
	refreshed, err := repo.ListSessions(ctx, userID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(refreshed.Sessions) != 3 || refreshed.Sessions[0].ID != "session-a" {
		t.Fatalf("chat update should move session to front without losing ownership: %+v", refreshed)
	}
}

func TestTwoTableLifecycleAndGenerationHistory(t *testing.T) {
	repo, root := newTestRepo(t)
	s := createSystem(t, repo, "lifecycle", "system-v1")
	user := s.BuildUserMessage("raw question")
	appendMessage(t, repo, s, &user)

	compressed := s.Clone()
	compressed.Messages[1].Content = "compressed question"
	if err := compressed.AdvanceMemoryGeneration(); err != nil {
		t.Fatal(err)
	}
	revision, err := repo.CommitNextMemoryGeneration(context.Background(), s.ID, compressed.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	compressed.Revision = revision
	*s = *compressed

	updated := s.Clone()
	sys := updated.UpsertSysMessage("system-v2")
	revision, err = repo.CommitUpdateMessage(context.Background(), s.ID, updated.Snapshot(), sys)
	if err != nil {
		t.Fatal(err)
	}
	updated.Revision = revision
	*s = *updated

	got, err := repo.GetRequestContext(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, s.Snapshot()) {
		t.Fatalf("restored context differs: got=%+v want=%+v", got, s.Snapshot())
	}
	if got.MemoryGeneration != 2 || got.LastSeq != 2 || got.Messages[0].Content != "system-v2" || got.Messages[1].Content != "compressed question" {
		t.Fatalf("unexpected current context: %+v", got)
	}

	var originals, firstGeneration, secondGeneration []messageModel
	if err := repo.db.Where("session_id = ? AND message_type = ?", s.ID, messageTypeOriginal).Order("seq").Find(&originals).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.db.Where("session_id = ? AND message_type = ? AND memory_generation = 1", s.ID, messageTypeMemory).Order("seq").Find(&firstGeneration).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.db.Where("session_id = ? AND message_type = ? AND memory_generation = 2", s.ID, messageTypeMemory).Order("seq").Find(&secondGeneration).Error; err != nil {
		t.Fatal(err)
	}
	if len(originals) != 2 || originals[0].Content != "system-v1" || originals[1].Content != "raw question" {
		t.Fatalf("original history changed: %+v", originals)
	}
	if len(firstGeneration) != 2 || firstGeneration[0].Content != "system-v1" || firstGeneration[1].Content != "raw question" {
		t.Fatalf("sealed first generation changed: %+v", firstGeneration)
	}
	if len(secondGeneration) != 2 || secondGeneration[0].Content != "system-v2" || secondGeneration[1].Content != "compressed question" {
		t.Fatalf("unexpected second generation: %+v", secondGeneration)
	}

	raw, err := os.ReadFile(filepath.Join(root, "history", s.ID, historyFile))
	if err != nil {
		t.Fatal(err)
	}
	var history []sharedkernel.Message
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var msg sharedkernel.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatal(err)
		}
		history = append(history, msg)
	}
	if len(history) != 2 || history[0].Content != "system-v1" || history[1].Content != "raw question" {
		t.Fatalf("unexpected JSONL history: %+v", history)
	}
}

func TestSummaryOriginalSequencesRoundTrip(t *testing.T) {
	repo, _ := newTestRepo(t)
	s := createSystem(t, repo, "summary-origins", "system")
	for _, msg := range []*sharedkernel.Message{
		{Role: sharedkernel.RoleUser, Content: "question one"},
		{Role: sharedkernel.RoleAssistant, Content: "answer one"},
		{Role: sharedkernel.RoleUser, Content: "current question"},
	} {
		appendMessage(t, repo, s, msg)
	}
	candidate := s.Clone()
	merged, err := compactor.MergeSummary(candidate.Messages, 3, `{"objective":"current question"}`)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Messages = merged
	if err := candidate.AdvanceMemoryGeneration(); err != nil {
		t.Fatal(err)
	}
	revision, err := repo.CommitNextMemoryGeneration(context.Background(), s.ID, candidate.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	candidate.Revision = revision
	*s = *candidate

	loaded, err := repo.GetRequestContext(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, s.Snapshot()) ||
		!reflect.DeepEqual(loaded.Messages[1].OriginalSeq, []uint64{2, 3}) {
		t.Fatalf("summary origins did not round trip: %+v", loaded)
	}
	var row messageModel
	if err := repo.db.Where("session_id = ? AND message_type = ? AND memory_generation = ? AND seq = ?",
		s.ID, messageTypeMemory, 2, 2).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if string(row.OriginalSeqJSON) != "[2,3]" {
		t.Fatalf("unexpected persisted original sequence JSON: %s", row.OriginalSeqJSON)
	}
}

func TestSchemaContainsSessionAndMemoryTables(t *testing.T) {
	repo, _ := newTestRepo(t)
	var names []string
	if err := repo.db.Raw(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`).Scan(&names).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"messages", "react_turns", "request_contexts", "user_memory_jobs"}) {
		t.Fatalf("tables=%v", names)
	}
}

func TestCreateRejectsStaleRevisionAndSequence(t *testing.T) {
	repo, _ := newTestRepo(t)
	s := createSystem(t, repo, "conflict", "system")
	before, err := repo.GetRequestContext(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	msg := s.BuildUserMessage("question")
	candidate, err := s.WithAppendedMessage(&msg)
	if err != nil {
		t.Fatal(err)
	}
	stale := candidate.Snapshot()
	stale.Revision--
	if _, err := repo.CommitCreateMessage(context.Background(), s.ID, stale, msg, msg); !errors.Is(err, ErrContextConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	wrong := msg.Clone()
	wrong.Seq++
	wrong.OriginalSeq = []uint64{wrong.Seq}
	if _, err := repo.CommitCreateMessage(context.Background(), s.ID, candidate.Snapshot(), wrong, wrong); !errors.Is(err, ErrStaleSequence) {
		t.Fatalf("stale sequence: %v", err)
	}
	after, err := repo.GetRequestContext(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected writes changed context")
	}
}

func TestUpdateChangesOnlyCurrentMemory(t *testing.T) {
	repo, _ := newTestRepo(t)
	s := createSystem(t, repo, "update", "old")
	candidate := s.Clone()
	sys := candidate.UpsertSysMessage("new")
	revision, err := repo.CommitUpdateMessage(context.Background(), s.ID, candidate.Snapshot(), sys)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Revision = revision
	var originals []messageModel
	if err := repo.db.Where("session_id = ? AND message_type = ?", s.ID, messageTypeOriginal).Find(&originals).Error; err != nil {
		t.Fatal(err)
	}
	if len(originals) != 1 || originals[0].Content != "old" {
		t.Fatalf("system update changed original: %+v", originals)
	}
	got, err := repo.GetRequestContext(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MemoryGeneration != 1 || got.LastSeq != 1 || got.Messages[0].Content != "new" {
		t.Fatalf("unexpected updated context: %+v", got)
	}
}

func TestGenerationFailureRollsBackContextHeadAndRows(t *testing.T) {
	repo, _ := newTestRepo(t)
	s := createSystem(t, repo, "rollback", "system")
	before := s.Snapshot()
	if err := repo.db.Exec(`CREATE TRIGGER fail_generation BEFORE INSERT ON messages
		WHEN NEW.memory_generation = 2
		BEGIN SELECT RAISE(ABORT, 'forced generation failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	candidate := s.Clone()
	candidate.Messages[0].Content = "compressed"
	if err := candidate.AdvanceMemoryGeneration(); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitNextMemoryGeneration(context.Background(), s.ID, candidate.Snapshot()); err == nil {
		t.Fatal("expected generation failure")
	}
	got, err := repo.GetRequestContext(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, before) {
		t.Fatalf("failed generation changed context: %+v", got)
	}
	var count int64
	if err := repo.db.Model(&messageModel{}).Where("session_id = ? AND memory_generation = 2", s.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed generation left %d rows", count)
	}
}
