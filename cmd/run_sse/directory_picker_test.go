package run_sse

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

type pickerExitError struct {
	code int
}

func (e pickerExitError) Error() string { return "picker exited" }
func (e pickerExitError) ExitCode() int { return e.code }

func TestPickNativeDirectoryUsesMacOSChooser(t *testing.T) {
	var command string
	var args []string
	run := func(_ context.Context, name string, values ...string) (string, string, error) {
		command = name
		args = append([]string(nil), values...)
		return "/Users/test/project/\n", "", nil
	}

	path, err := pickNativeDirectoryWithRunner(context.Background(), "darwin", run)
	if err != nil || path != "/Users/test/project/" {
		t.Fatalf("path=%q err=%v", path, err)
	}
	if command != "osascript" || !slices.Contains(args, "POSIX path of selectedFolder") {
		t.Fatalf("command=%q args=%q", command, args)
	}
}

func TestPickNativeDirectoryOutcomes(t *testing.T) {
	cancelled := func(context.Context, string, ...string) (string, string, error) {
		return "", "execution error: User canceled. (-128)", pickerExitError{code: 1}
	}
	if _, err := pickNativeDirectoryWithRunner(context.Background(), "darwin", cancelled); !errors.Is(err, errDirectoryPickerCancelled) {
		t.Fatalf("cancel error=%v", err)
	}

	broken := func(context.Context, string, ...string) (string, string, error) {
		return "", "permission denied", pickerExitError{code: 2}
	}
	if _, err := pickNativeDirectoryWithRunner(context.Background(), "darwin", broken); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("failure=%v", err)
	}

	if _, err := pickNativeDirectoryWithRunner(context.Background(), "linux", broken); !errors.Is(err, errDirectoryPickerUnsupported) {
		t.Fatalf("unsupported error=%v", err)
	}
}

func TestHandlePickDirectory(t *testing.T) {
	s := newServer(t.TempDir(), false)
	s.pickDirectory = func(context.Context) (string, error) { return "/Users/test/project/", nil }

	response := httptest.NewRecorder()
	s.handlePickDirectory(response, httptest.NewRequest(http.MethodPost, "/api/directory-picker", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var picked directoryPickerResponse
	if err := json.Unmarshal(response.Body.Bytes(), &picked); err != nil {
		t.Fatal(err)
	}
	if picked.Path == nil || *picked.Path != "/Users/test/project/" {
		t.Fatalf("response=%+v", picked)
	}

	s.pickDirectory = func(context.Context) (string, error) { return "", errDirectoryPickerCancelled }
	cancelled := httptest.NewRecorder()
	s.handlePickDirectory(cancelled, httptest.NewRequest(http.MethodPost, "/api/directory-picker", nil))
	if cancelled.Code != http.StatusOK || strings.TrimSpace(cancelled.Body.String()) != `{"path":null}` {
		t.Fatalf("cancel status=%d body=%s", cancelled.Code, cancelled.Body.String())
	}
}

func TestHandlePickDirectoryRejectsConcurrentPicker(t *testing.T) {
	s := newServer(t.TempDir(), false)
	started := make(chan struct{})
	release := make(chan struct{})
	s.pickDirectory = func(context.Context) (string, error) {
		close(started)
		<-release
		return "/tmp/project", nil
	}

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		s.handlePickDirectory(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/directory-picker", nil))
	}()
	<-started

	conflict := httptest.NewRecorder()
	s.handlePickDirectory(conflict, httptest.NewRequest(http.MethodPost, "/api/directory-picker", nil))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	close(release)
	<-firstDone
}
