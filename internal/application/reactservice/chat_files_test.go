package reactservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
	"github.com/mikellxy/laxcode/internal/infrastructure/workfs"
)

func TestCloudChatFilesSurviveFailureAndResume(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "demo")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	repo, err := sessionrepo.NewSqliteSessionRepo(filepath.Join(root, "agenthome", "sessions.db"), filepath.Join(root, "history"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	bounded, err := workfs.NewRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer bounded.Close()
	reg := tools.NewDefaultRegistry(nil)
	write := tools.NewWriteFileTool(work, bounded)
	write.UserRoot = root
	reg.Register(write)
	ctx := telemetry.ContextWithChatID(context.Background(), "chat-1")
	svc := NewReActService(session.NewSession("session-1", work), repo, &scriptedLLM{responses: []scriptedResp{
		{msg: assistantMsgWithTool(sharedkernel.ToolCall{ID: "write-1", Name: tools.ToolWriteFile, Arguments: json.RawMessage(`{"path":"a.txt","content":"first"}`)})},
		{err: errors.New("upstream disconnected")},
	}}, nil, reg, nil, nil)
	svc.SetUserRoot(root)
	svc.SetWorkDir(work)
	if err := svc.InitSession(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.InitSysPrompt(ctx, "system"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Chat(ctx, "create file"); err == nil {
		t.Fatal("expected interrupted chat")
	}
	files, err := repo.ListChatFiles(ctx, svc.Session.ID, "chat-1")
	if err != nil || len(files) != 1 || files[0].Path != "demo/a.txt" || files[0].ChangeType != "created" {
		t.Fatalf("interrupted files: %+v %v", files, err)
	}
	resumed := NewReActService(session.NewSession(svc.Session.ID, work), repo, &scriptedLLM{responses: []scriptedResp{
		{msg: assistantMsgWithTool(sharedkernel.ToolCall{ID: "write-2", Name: tools.ToolWriteFile, Arguments: json.RawMessage(`{"path":"a.txt","content":"current"}`)})},
		{msg: assistantMsg("done")},
	}}, nil, reg, nil, nil)
	resumed.SetUserRoot(root)
	resumed.SetWorkDir(work)
	if err := resumed.InitSession(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := resumed.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	files, err = repo.ListChatFiles(ctx, svc.Session.ID, "chat-1")
	if err != nil || len(files) != 1 || files[0].ChangeType != "created" {
		t.Fatalf("resume duplicates: %+v %v", files, err)
	}
	page, _, err := repo.ListOriginalHistory(ctx, svc.Session.ID, 0, 1)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ChatID != "chat-1" || len(page.Messages[0].UpsertFiles) != 1 {
		t.Fatalf("history: %+v %v", page, err)
	}
	ctx2 := telemetry.ContextWithChatID(ctx, "chat-2")
	resumed.LLMClient = &scriptedLLM{responses: []scriptedResp{
		{msg: assistantMsgWithTool(sharedkernel.ToolCall{ID: "write-3", Name: tools.ToolWriteFile, Arguments: json.RawMessage(`{"path":"a.txt","content":"next chat"}`)})},
		{msg: assistantMsg("done")},
	}}
	if _, err := resumed.Chat(ctx2, "modify file"); err != nil {
		t.Fatal(err)
	}
	files, err = repo.ListChatFiles(ctx, svc.Session.ID, "chat-2")
	if err != nil || len(files) != 1 || files[0].ChangeType != "modified" {
		t.Fatalf("next chat files: %+v %v", files, err)
	}
}
