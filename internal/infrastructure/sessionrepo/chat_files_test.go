package sessionrepo

import (
	"context"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func TestChatFilesDeduplicateAndAttachAcrossPages(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	sess := createSystem(t, repo, "file-session", "system")
	sess.UserID = "user"
	if err := repo.db.Model(&requestContextModel{}).Where("session_id = ?", sess.ID).Update("user_id", "user").Error; err != nil {
		t.Fatal(err)
	}
	appendMessage(t, repo, sess, &sharedkernel.Message{Role: sharedkernel.RoleUser, ChatID: "chat-one", Content: "create"})
	appendMessage(t, repo, sess, &sharedkernel.Message{Role: sharedkernel.RoleAssistant, ChatID: "chat-one", Content: "working"})
	early := sess.LastSeq
	appendMessage(t, repo, sess, &sharedkernel.Message{Role: sharedkernel.RoleTool, ToolCallID: "write", Content: "written"})
	if err := repo.UpsertChatFiles(ctx, sess.ID, "chat-one", []session.ChatFile{{Path: "demo/a.txt", ChangeType: "created"}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertChatFiles(ctx, sess.ID, "chat-one", []session.ChatFile{{Path: "demo/a.txt", ChangeType: "modified"}, {Path: "demo/b.txt", ChangeType: "modified"}}); err != nil {
		t.Fatal(err)
	}
	appendMessage(t, repo, sess, &sharedkernel.Message{Role: sharedkernel.RoleAssistant, ChatID: "chat-one", Content: "done"})
	last := sess.LastSeq
	appendMessage(t, repo, sess, &sharedkernel.Message{Role: sharedkernel.RoleUser, ChatID: "chat-two", Content: "next"})
	appendMessage(t, repo, sess, &sharedkernel.Message{Role: sharedkernel.RoleAssistant, ChatID: "chat-two", Content: "done"})
	page, _, err := repo.ListOriginalHistory(ctx, sess.ID, last+1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Seq != last || len(page.Messages[0].UpsertFiles) != 2 || page.Messages[0].UpsertFiles[0].ChangeType != "created" {
		t.Fatalf("last assistant without preceding user: %+v", page)
	}
	page, _, err = repo.ListOriginalHistory(ctx, sess.ID, early+1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages[0].UpsertFiles) != 0 {
		t.Fatalf("duplicate card on early assistant: %+v", page)
	}
	for _, file := range []session.ChatFile{{Path: "../outside", ChangeType: "created"}, {Path: "/absolute", ChangeType: "created"}, {Path: "demo/x", ChangeType: "deleted"}} {
		if err := repo.UpsertChatFiles(ctx, sess.ID, "chat-one", []session.ChatFile{file}); err == nil {
			t.Fatalf("accepted %+v", file)
		}
	}
	if err := repo.UpsertChatFiles(ctx, sess.ID, "missing", []session.ChatFile{{Path: "demo/x", ChangeType: "created"}}); err == nil {
		t.Fatal("accepted unknown chat")
	}
	if _, err := repo.ListChatFiles(ctx, "another-session", "chat-one"); err == nil {
		t.Fatal("cross-session chat accepted")
	}
	if err := repo.DeleteSession(ctx, sess.ID, "user"); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := repo.db.Table("chat_upsert_files").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("records after deletion: %d %v", count, err)
	}
}
