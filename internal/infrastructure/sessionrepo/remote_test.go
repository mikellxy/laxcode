package sessionrepo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func TestRemotePreservesContextTransactionsAndConflicts(t *testing.T) {
	root := t.TempDir()
	repo, err := NewSqliteSessionRepo(filepath.Join(root, "sessions.db"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	server := httptest.NewServer(RPCHandler(repo, "user"))
	defer server.Close()
	remote := NewRemote(server.URL, "user", "token")
	defer remote.Close()
	ctx := context.Background()
	if _, err := remote.GetSession(ctx, "missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("missing error: %v", err)
	}
	if _, err := remote.CreateSession(ctx, "s1", "user", "p1", "", "/workplace/demo", "code"); err != nil {
		t.Fatal(err)
	}
	if renamed, err := remote.RenameSession(ctx, "s1", "user", "My session"); err != nil || renamed.Title != "My session" {
		t.Fatalf("remote rename: %+v %v", renamed, err)
	}
	snapshot, err := remote.GetRequestContext(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	sess := session.NewSession("s1", "/workplace/demo")
	sess.RequestContext = snapshot
	sys := sess.UpsertSysMessage("system")
	revision, err := remote.CommitCreateMessage(ctx, "s1", sess.Snapshot(), sys, sys)
	if err != nil || revision == 0 {
		t.Fatalf("commit: %d %v", revision, err)
	}
	if _, err := remote.CommitCreateMessage(ctx, "s1", sess.Snapshot(), sys, sys); !errors.Is(err, ErrContextConflict) {
		t.Fatalf("conflict: %v", err)
	}
	sess.Revision = revision
	msg := sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "create file", ChatID: "c1"}
	next, err := sess.WithAppendedMessage(&msg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.CommitCreateMessage(ctx, "s1", next.Snapshot(), msg, msg); err != nil {
		t.Fatal(err)
	}
	if err := remote.UpsertChatFiles(ctx, "s1", "c1", []session.ChatFile{{Path: "demo/out.txt", ChangeType: "created"}}); err != nil {
		t.Fatal(err)
	}
	files, err := remote.ListChatFiles(ctx, "s1", "c1")
	if err != nil || len(files) != 1 {
		t.Fatalf("files: %v %v", files, err)
	}
	state, err := remote.GetRequestContext(ctx, "s1")
	if err != nil || state.LastSeq != 2 || state.Revision != 2 || state.Messages[1].Content != "create file" {
		t.Fatalf("context: %+v %v", state, err)
	}
	page, found, err := remote.ListOriginalHistory(ctx, "s1", 0, 50)
	if err != nil || !found || len(page.Messages) != 1 {
		t.Fatalf("history: %+v %v %v", page, found, err)
	}
	sess.RequestContext = state
	tool := sharedkernel.Message{Role: sharedkernel.RoleTool, Content: "raw tool output", DisplayContent: "safe summary", CompactContent: "short result", ToolCallID: "call1"}
	next, err = sess.WithAppendedMessage(&tool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.CommitCreateMessage(ctx, "s1", next.Snapshot(), tool, tool); err != nil {
		t.Fatal(err)
	}
	state, err = remote.GetRequestContext(ctx, "s1")
	if err != nil || state.Revision != 3 || state.Messages[2].DisplayContent != "safe summary" {
		t.Fatalf("persistence-only fields lost: %+v %v", state, err)
	}
	if stored, err := remote.GetSession(ctx, "s1"); err != nil || stored.Title != "My session" {
		t.Fatalf("message writes overwrote title: %+v %v", stored, err)
	}

}

func TestRemoteLostWriteResponseIsNotSafeToResend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("incomplete")) }))
	defer server.Close()
	remote := NewRemote(server.URL, "user", "token")
	if _, err := remote.CreateSession(context.Background(), "s1", "user", "p", "", "/workplace/demo", "code"); !errors.Is(err, ErrWriteUncertain) {
		t.Fatalf("write outcome: %v", err)
	}
	if _, err := remote.GetSession(context.Background(), "s1"); errors.Is(err, ErrWriteUncertain) {
		t.Fatal("read incorrectly marked as a write")
	}
}
