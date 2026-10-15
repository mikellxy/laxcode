package run_sse

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

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
	previewEndpoint := strings.TrimSuffix(endpoint, "files.zip") + "file"
	preview := func(userID, path string) *httptest.ResponseRecorder {
		return cloudRequest(t, users, userID, "GET", previewEndpoint+"?"+url.Values{"path": {path}}.Encode(), nil)
	}
	if got := preview(id, "demo/a.txt"); got.Code != 200 || got.Body.String() != "current" || got.Header().Get("Content-Type") != "text/plain; charset=utf-8" || got.Header().Get("Cache-Control") != "no-store" || got.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("text preview: %d %s headers=%v", got.Code, got.Body.String(), got.Header())
	}
	for _, path := range []string{"demo/unlisted.txt", "../secret", target, "demo/../agenthome/settings.json", `demo\a.txt`, ""} {
		if got := preview(id, path); got.Code != 404 || strings.Contains(got.Body.String(), user.server.userRoot) {
			t.Fatalf("unlisted path %q: %d %s", path, got.Code, got.Body.String())
		}
	}
	if got := preview(other, "demo/a.txt"); got.Code != 404 {
		t.Fatalf("another user's preview: %d", got.Code)
	}
	if got := cloudRequest(t, users, id, "GET", strings.Replace(previewEndpoint, msg.ChatID, "wrong-chat", 1)+"?path=demo/a.txt", nil); got.Code != 404 {
		t.Fatalf("another chat's preview: %d", got.Code)
	}
	if err := os.WriteFile(target, []byte(strings.Repeat("a", maxChatTextPreviewBytes-1)+"你后面"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := preview(id, "demo/a.txt"); got.Code != 200 || got.Body.Len() != maxChatTextPreviewBytes-1 || !utf8.Valid(got.Body.Bytes()) || got.Header().Get("X-Preview-Truncated") != "true" {
		t.Fatalf("truncated text: status=%d bytes=%d headers=%v", got.Code, got.Body.Len(), got.Header())
	}
	for _, content := range []string{"", "<html><script>alert('test')</script></html>", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert('test')</script></svg>`} {
		if err := os.WriteFile(target, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if got := preview(id, "demo/a.txt"); got.Code != 200 || got.Body.String() != content || got.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Fatalf("literal text: %d %s", got.Code, got.Body.String())
		}
	}
	for _, content := range [][]byte{{0, 1, 2}, {0xff, 0xfe}, []byte("%PDF-1.7\nexample")} {
		if err := os.WriteFile(target, content, 0600); err != nil {
			t.Fatal(err)
		}
		if got := preview(id, "demo/a.txt"); got.Code != 415 {
			t.Fatalf("binary preview: %d %s", got.Code, got.Body.String())
		}
	}
	imagePath := filepath.Join(work, "image.PNG")
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imagePath, imageData.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := user.repo.UpsertChatFiles(ctx, sess.ID, msg.ChatID, []session.ChatFile{{Path: "demo/image.PNG", ChangeType: "created"}}); err != nil {
		t.Fatal(err)
	}
	if got := preview(id, "demo/image.PNG"); got.Code != 200 || got.Header().Get("Content-Type") != "image/png" || !bytes.Equal(got.Body.Bytes(), imageData.Bytes()) {
		t.Fatalf("image preview: status=%d headers=%v", got.Code, got.Header())
	}
	if err := os.Truncate(imagePath, maxChatImagePreviewBytes+1); err != nil {
		t.Fatal(err)
	}
	if got := preview(id, "demo/image.PNG"); got.Code != 413 {
		t.Fatalf("oversized image: %d", got.Code)
	}
	if err := os.WriteFile(imagePath, []byte("<html>not an image</html>"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := preview(id, "demo/image.PNG"); got.Code != 415 {
		t.Fatalf("mislabeled image: %d", got.Code)
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
	if got := preview(id, "demo/a.txt"); got.Code != 409 || strings.Contains(got.Body.String(), user.server.userRoot) {
		t.Fatalf("deleted preview: %d %s", got.Code, got.Body.String())
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
	if got := preview(id, "demo/a.txt"); got.Code != 409 {
		t.Fatalf("symlink preview: %d", got.Code)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(work, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := user.repo.UpsertChatFiles(ctx, sess.ID, msg.ChatID, []session.ChatFile{{Path: "demo/linked/secret", ChangeType: "created"}}); err != nil {
		t.Fatal(err)
	}
	if got := preview(id, "demo/linked/secret"); got.Code != 409 {
		t.Fatalf("parent symlink preview: %d %s", got.Code, got.Body.String())
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
	if got := preview(id, "agenthome/settings.json"); got.Code != 403 {
		t.Fatalf("non-project preview: %d %s", got.Code, got.Body.String())
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
	if got := cloudRequest(t, (&server{}).routes(), "", "GET", previewEndpoint+"?path=demo/a.txt", nil); got.Code != 403 {
		t.Fatalf("local mode preview: %d", got.Code)
	}
}
