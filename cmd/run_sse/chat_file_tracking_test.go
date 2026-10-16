package run_sse

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
	"github.com/mikellxy/laxcode/internal/infrastructure/workfs"
)

func writeTrackedTestFile(t *testing.T, root, path, content string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestProjectSnapshotDiffAndBoundaries(t *testing.T) {
	work := t.TempDir()
	for _, path := range []string{"unchanged.txt", "size.txt", "mtime.txt", "deleted.txt"} {
		writeTrackedTestFile(t, work, path, "old")
	}
	outside := t.TempDir()
	writeTrackedTestFile(t, outside, "secret.txt", "secret")
	root, err := os.OpenRoot(work)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	before, err := scanProjectFiles(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	writeTrackedTestFile(t, work, "size.txt", "longer")
	writeTrackedTestFile(t, work, "mtime.txt", "new")
	stamp := before["mtime.txt"].modTime.Add(2 * time.Second)
	if err := os.Chtimes(filepath.Join(work, "mtime.txt"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(work, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"docs/house.png", "docs/house.svg", "dist/app.js", "build/output.bin",
		"node_modules/dep/index.js", ".git/index", "nested-worktree/.git", ".venv/lib/x.py", "__pycache__/x.pyc", `docs/not\downloadable.txt`} {
		writeTrackedTestFile(t, work, path, "generated")
	}
	for name, target := range map[string]string{"outside": outside, "secret.txt": filepath.Join(outside, "secret.txt"), "alias.png": "docs/house.png"} {
		if err := os.Symlink(target, filepath.Join(work, name)); err != nil {
			t.Fatal(err)
		}
	}
	after, err := scanProjectFiles(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := []session.ChatFile{
		{Path: "demo/build/output.bin", ChangeType: "created"},
		{Path: "demo/dist/app.js", ChangeType: "created"},
		{Path: "demo/docs/house.png", ChangeType: "created"},
		{Path: "demo/docs/house.svg", ChangeType: "created"},
		{Path: "demo/mtime.txt", ChangeType: "modified"},
		{Path: "demo/size.txt", ChangeType: "modified"},
	}
	if got := diffProjectFiles("demo", before, after); !reflect.DeepEqual(got, want) {
		t.Fatalf("files=%+v want=%+v", got, want)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanProjectFiles(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scan: %v", err)
	}
}

type fileTrackingLLM struct {
	approvalFlowLLM
	generate func(context.Context) (*sharedkernel.Message, error)
}

func (l *fileTrackingLLM) GenerateStream(ctx context.Context, _ []sharedkernel.Message, _ []sharedkernel.ToolDefinition, _ func(sharedkernel.StreamChunk)) (*sharedkernel.Message, error) {
	return l.generate(ctx)
}

type fileTrackingShell struct {
	run func()
}

func (s *fileTrackingShell) Run(context.Context, string, string, time.Duration) (tools.ShellOutcome, error) {
	s.run()
	return tools.ShellOutcome{Output: "rendered"}, nil
}
func (*fileTrackingShell) BashSandboxed() bool { return true }
func (*fileTrackingShell) Close() error        { return nil }

func fileToolMessage(name, args string) *sharedkernel.Message {
	return &sharedkernel.Message{Role: sharedkernel.RoleAssistant, ToolCalls: []sharedkernel.ToolCall{{ID: "tool", Name: name, Arguments: json.RawMessage(args)}}}
}

func newFileTrackingServer(t *testing.T) (*server, *sessionrepo.SqliteSessionRepo, string) {
	t.Helper()
	stubActiveModel(t)
	root := t.TempDir()
	work := filepath.Join(root, "demo")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "agenthome")
	s := newTestServer(t, home, false)
	s.userRoot = root
	repo, err := sessionrepo.NewSqliteSessionRepo(filepath.Join(home, "sessions.db"), filepath.Join(home, "history"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close(); s.oauth.Close(); s.stopEvaluations() })
	s.contextRepo, s.history = repo, repo
	s.catalog = catalogWithSession("files-session", work)
	return s, repo, work
}

// Checks the persisted file list at the instant the terminal SSE event is written.
type fileTrackingResponse struct {
	*httptest.ResponseRecorder
	terminal func()
}

func (w fileTrackingResponse) Write(data []byte) (int, error) {
	if strings.HasPrefix(string(data), "event: done\n") || strings.HasPrefix(string(data), "event: error\n") {
		w.terminal()
	}
	return w.ResponseRecorder.Write(data)
}

func TestCloudSnapshotChatFailureResumeAndNextChat(t *testing.T) {
	s, repo, work := newFileTrackingServer(t)
	ctx := context.Background()
	step, renders, cleanups := 0, 0, 0
	llm := &fileTrackingLLM{}
	llm.generate = func(context.Context) (*sharedkernel.Message, error) {
		step++
		switch step {
		case 1:
			return fileToolMessage(tools.ToolWriteFile, `{"path":"docs/house.excalidraw","content":"scene"}`), nil
		case 2, 4:
			return fileToolMessage(tools.ToolBash, `{"command":"python3 render.py"}`), nil
		case 3:
			// Neither file tools nor Bash persist files in the middle of a request.
			state, err := repo.GetRequestContext(ctx, "files-session")
			if err != nil {
				t.Fatal(err)
			}
			files, err := repo.ListChatFiles(ctx, "files-session", latestChatID(state.Messages))
			if err != nil || len(files) != 0 {
				t.Fatalf("files recorded before final scan: %+v %v", files, err)
			}
			return nil, errors.New("upstream disconnected")
		case 6:
			return fileToolMessage(tools.ToolEditFile, `{"path":"docs/house.excalidraw","edits":[{"old_text":"scene","new_text":"updated scene"}]}`), nil
		default:
			return &sharedkernel.Message{Role: sharedkernel.RoleAssistant, Content: "done"}, nil
		}
	}
	s.assemble = func(ctx context.Context, in agentasm.Input) (*agentasm.Assembled, error) {
		bounded, err := workfs.NewRoot(s.userRoot)
		if err != nil {
			return nil, err
		}
		reg := tools.NewDefaultRegistry(nil)
		write := tools.NewWriteFileTool(work, bounded)
		write.UserRoot = s.userRoot
		reg.Register(write)
		edit := tools.NewEditFileTool(work, bounded)
		edit.UserRoot = s.userRoot
		reg.Register(edit)
		reg.Register(tools.NewBashTool(work, &fileTrackingShell{run: func() {
			renders++
			writeTrackedTestFile(t, work, "docs/house.png", strings.Repeat("PNG", renders))
			writeTrackedTestFile(t, work, "docs/house.svg", strings.Repeat("SVG", renders))
			writeTrackedTestFile(t, s.userRoot, "tmp/render.log", "temporary")
			writeTrackedTestFile(t, s.userRoot, "other-project/file.txt", "other")
		}}, nil, in.SessionID))
		sess := session.NewSession(in.SessionID, work)
		svc := reactservice.NewReActService(sess, repo, llm, nil, reg, in.Consumer, nil)
		svc.SetWorkDir(work)
		svc.SetUserRoot(s.userRoot)
		if err := svc.InitSession(ctx); err != nil {
			bounded.Close()
			return nil, err
		}
		if err := svc.InitSysPrompt(ctx, "system"); err != nil {
			bounded.Close()
			return nil, err
		}
		return &agentasm.Assembled{Service: svc, Session: sess, Cleanup: func() {
			cleanups++
			reg.Close()
			bounded.Close()
			// Output finalized during cleanup must also be discovered.
			writeTrackedTestFile(t, work, "docs/finished.txt", strings.Repeat("x", cleanups))
		}}, nil
	}
	mux := s.routes()
	var firstChatID string
	for i, path := range []string{"/chat", "/api/sessions/files-session/resume", "/chat"} {
		response := fileTrackingResponse{ResponseRecorder: httptest.NewRecorder(), terminal: func() {
			state, err := repo.GetRequestContext(ctx, "files-session")
			if err != nil {
				t.Fatal(err)
			}
			chatID := latestChatID(state.Messages)
			if i == 0 {
				firstChatID = chatID
			} else if (i == 1) != (chatID == firstChatID) {
				t.Fatal("incorrect chat identity on resume/new chat")
			}
			files, err := repo.ListChatFiles(ctx, "files-session", chatID)
			want := []session.ChatFile{{Path: "demo/docs/finished.txt", ChangeType: "created"}, {Path: "demo/docs/house.excalidraw", ChangeType: "created"}, {Path: "demo/docs/house.png", ChangeType: "created"}, {Path: "demo/docs/house.svg", ChangeType: "created"}}
			if i == 2 {
				want = []session.ChatFile{{Path: "demo/docs/finished.txt", ChangeType: "modified"}, {Path: "demo/docs/house.excalidraw", ChangeType: "modified"}}
			}
			if err != nil || !reflect.DeepEqual(files, want) {
				t.Fatalf("request %d files=%+v want=%+v err=%v", i, files, want, err)
			}
		}}
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"session_id":"files-session","task":"draw"}`)))
		wantEvent := "event: done"
		if i == 0 {
			wantEvent = `"code":"CHAT_FAILED"`
		}
		if !strings.Contains(response.Body.String(), wantEvent) || cleanups != i+1 {
			t.Fatalf("request %d cleanup=%d response=%s", i, cleanups, response.Body.String())
		}
	}
	page, _, err := repo.ListOriginalHistory(ctx, "files-session", 0, 1)
	if err != nil || len(page.Messages) != 1 || len(page.Messages[0].UpsertFiles) != 2 {
		t.Fatalf("history: %+v %v", page, err)
	}
}

func TestCloudSnapshotAfterCancellation(t *testing.T) {
	s, repo, work := newFileTrackingServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.assemble = func(ctx context.Context, in agentasm.Input) (*agentasm.Assembled, error) {
		sess := session.NewSession(in.SessionID, work)
		llm := &fileTrackingLLM{generate: func(context.Context) (*sharedkernel.Message, error) {
			writeTrackedTestFile(t, work, "result.png", "generated before cancellation")
			cancel()
			return nil, context.Canceled
		}}
		svc := reactservice.NewReActService(sess, repo, llm, nil, tools.NewDefaultRegistry(nil), in.Consumer, nil)
		if err := svc.InitSession(ctx); err != nil {
			return nil, err
		}
		if err := svc.InitSysPrompt(ctx, "system"); err != nil {
			return nil, err
		}
		return &agentasm.Assembled{Service: svc, Session: sess, Cleanup: func() {}}, nil
	}
	request := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"session_id":"files-session","task":"draw"}`)).WithContext(ctx)
	s.handleChat(httptest.NewRecorder(), request)
	state, err := repo.GetRequestContext(context.Background(), "files-session")
	if err != nil {
		t.Fatal(err)
	}
	files, err := repo.ListChatFiles(context.Background(), "files-session", latestChatID(state.Messages))
	if err != nil || len(files) != 1 || files[0].Path != "demo/result.png" {
		t.Fatalf("canceled request lost files: %+v %v", files, err)
	}
}

func TestCloudProjectLockAndSnapshotFailure(t *testing.T) {
	s, _, work := newFileTrackingServer(t)
	unlock, ok := s.lockChatProject(work)
	if !ok {
		t.Fatal("cannot lock project")
	}
	for _, path := range []string{"/chat", "/api/sessions/files-session/resume"} {
		response := httptest.NewRecorder()
		s.routes().ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"session_id":"files-session","task":"draw"}`)))
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "project is busy") {
			t.Fatalf("locked project: %d %s", response.Code, response.Body.String())
		}
	}
	otherUnlock, ok := s.lockChatProject(filepath.Join(s.userRoot, "other"))
	if !ok {
		t.Fatal("different projects block each other")
	}
	otherUnlock()
	unlock()

	for _, dir := range []string{filepath.Dir(s.userRoot), filepath.Join(s.userRoot, "tmp"), filepath.Join(s.userRoot, "alias")} {
		if filepath.Base(dir) == "alias" {
			if err := os.Symlink(work, dir); err != nil {
				t.Fatal(err)
			}
		}
		cleaned := false
		_, err := s.runWithFileTracking(context.Background(), dir, &agentasm.Assembled{Session: session.NewSession("files-session"), Cleanup: func() { cleaned = true }}, func() (*sharedkernel.Message, error) {
			t.Fatal("invalid scan root executed the agent")
			return nil, nil
		})
		if !errors.Is(err, errChatFileTracking) || !cleaned {
			t.Fatalf("invalid workdir: err=%v cleanup=%v", err, cleaned)
		}
	}
}

func TestCloudSnapshotPersistenceFailureIsVisible(t *testing.T) {
	s, repo, work := newFileTrackingServer(t)
	s.assemble = func(ctx context.Context, in agentasm.Input) (*agentasm.Assembled, error) {
		sess := session.NewSession(in.SessionID, work)
		llm := &fileTrackingLLM{generate: func(context.Context) (*sharedkernel.Message, error) {
			writeTrackedTestFile(t, work, "result.png", "generated")
			return &sharedkernel.Message{Role: sharedkernel.RoleAssistant, Content: "done"}, nil
		}}
		svc := reactservice.NewReActService(sess, repo, llm, nil, tools.NewDefaultRegistry(nil), in.Consumer, nil)
		if err := svc.InitSession(ctx); err != nil {
			return nil, err
		}
		if err := svc.InitSysPrompt(ctx, "system"); err != nil {
			return nil, err
		}
		return &agentasm.Assembled{Service: svc, Session: sess, Cleanup: func() { repo.Close() }}, nil
	}
	response := httptest.NewRecorder()
	s.handleChat(response, httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"session_id":"files-session","task":"draw"}`)))
	body := response.Body.String()
	if !strings.Contains(body, `"code":"INTERNAL_ERROR"`) || !strings.Contains(body, "文件产物统计失败") ||
		strings.Contains(body, "event: done") || strings.Contains(body, "retry_action") || strings.Contains(body, s.userRoot) {
		t.Fatalf("tracking failure should be visible without exposing paths or retrying the task: %s", body)
	}
}

func TestCloudSnapshotSkipsUncommittedChat(t *testing.T) {
	s, _, work := newFileTrackingServer(t)
	runErr := errors.New("failed before committing user message")
	cleanups := 0
	_, err := s.runWithFileTracking(context.Background(), work, &agentasm.Assembled{
		Session: session.NewSession("files-session"), Cleanup: func() { cleanups++ },
	}, func() (*sharedkernel.Message, error) {
		return nil, runErr
	})
	if !errors.Is(err, runErr) || errors.Is(err, errChatFileTracking) || cleanups != 1 {
		t.Fatalf("uncommitted chat: err=%v cleanup=%d", err, cleanups)
	}
}
