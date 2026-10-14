package run_sse

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func TestCloudChatZIPCurrentVersionAndBoundaries(t *testing.T) {
	users := newCloudUsers(t.TempDir(), nil)
	defer users.Close()
	id, other := uuid.NewString(), uuid.NewString()
	user, err := users.user(id, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.user(other, true); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(user.server.userRoot, "demo")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := user.repo.CreateSession(ctx, "download-session", id, "", "", work, "code"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := user.repo.GetRequestContext(ctx, "download-session")
	if err != nil {
		t.Fatal(err)
	}
	sess := session.NewSession("download-session", work)
	sess.RequestContext = snapshot
	system := sess.UpsertSysMessage("system")
	revision, err := user.repo.CommitCreateMessage(ctx, sess.ID, sess.Snapshot(), system, system)
	if err != nil {
		t.Fatal(err)
	}
	sess.Revision = revision
	msg := sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "create", ChatID: "download-chat"}
	candidate, err := sess.WithAppendedMessage(&msg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := user.repo.CommitCreateMessage(ctx, sess.ID, candidate.Snapshot(), msg, msg); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(work, "a.txt")
	if err := os.WriteFile(target, []byte("initial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := user.repo.UpsertChatFiles(ctx, sess.ID, msg.ChatID, []session.ChatFile{{Path: "demo/a.txt", ChangeType: "created"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("current"), 0600); err != nil {
		t.Fatal(err)
	}
	endpoint := "/api/sessions/" + sess.ID + "/chats/" + msg.ChatID + "/files.zip"
	response := cloudRequest(t, users, id, "GET", endpoint, nil)
	if response.Code != 200 || response.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("download: %d %s", response.Code, response.Body.String())
	}
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 1 || archive.File[0].Name != "demo/a.txt" {
		t.Fatalf("ZIP entries: %+v", archive.File)
	}
	reader, err := archive.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(data) != "current" {
		t.Fatalf("ZIP content: %q %v", data, err)
	}
	if got := cloudRequest(t, users, other, "GET", endpoint, nil); got.Code != 404 {
		t.Fatalf("another user: %d", got.Code)
	}
	if got := cloudRequest(t, users, id, "GET", strings.Replace(endpoint, msg.ChatID, "wrong-chat", 1), nil); got.Code != 404 {
		t.Fatalf("another chat: %d", got.Code)
	}
	if err := os.Truncate(target, maxChatDownloadBytes+1); err != nil {
		t.Fatal(err)
	}
	if got := cloudRequest(t, users, id, "GET", endpoint, nil); got.Code != 413 {
		t.Fatalf("oversized: %d", got.Code)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if got := cloudRequest(t, users, id, "GET", endpoint, nil); got.Code != 409 || strings.Contains(got.Body.String(), user.server.userRoot) {
		t.Fatalf("deleted: %d %s", got.Code, got.Body.String())
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Fatal(err)
	}
	if got := cloudRequest(t, users, id, "GET", endpoint, nil); got.Code != 409 {
		t.Fatalf("symlink: %d", got.Code)
	}
	entries, err := os.ReadDir(user.server.tempDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary ZIPs not cleaned: %+v %v", entries, err)
	}
	if err := user.repo.UpsertChatFiles(ctx, sess.ID, msg.ChatID, []session.ChatFile{{Path: "agenthome/settings.json", ChangeType: "modified"}}); err != nil {
		t.Fatal(err)
	}
	if got := cloudRequest(t, users, id, "GET", endpoint, nil); got.Code != 403 {
		t.Fatalf("non-project path: %d %s", got.Code, got.Body.String())
	}
	var tooMany []session.ChatFile
	for i := 0; i <= maxChatDownloadFiles; i++ {
		tooMany = append(tooMany, session.ChatFile{Path: fmt.Sprintf("demo/file-%d.txt", i), ChangeType: "created"})
	}
	if err := user.repo.UpsertChatFiles(ctx, sess.ID, msg.ChatID, tooMany); err != nil {
		t.Fatal(err)
	}
	if got := cloudRequest(t, users, id, "GET", endpoint, nil); got.Code != 413 {
		t.Fatalf("too many files: %d", got.Code)
	}
	if got := cloudRequest(t, (&server{}).routes(), "", "GET", endpoint, nil); got.Code != 403 {
		t.Fatalf("local mode download: %d", got.Code)
	}
}
