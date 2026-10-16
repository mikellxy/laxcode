package run_sse

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/internal/domain/evaluation"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

func createReportTestJob(t *testing.T, repo *sessionrepo.SqliteSessionRepo, sessionID, userID, jobID, workDir, status string) {
	t.Helper()
	_, err := repo.CreateEvaluationJob(context.Background(), evaluation.Job{
		ID: jobID, SourceSessionID: sessionID, UserID: userID, WorkDir: workDir,
		Requirement: "check task", Status: status, SnapshotHistory: layout.EvaluationSnapshotHistory(workDir, sessionID),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEvaluationReportDownloadLocalAndCloud(t *testing.T) {
	for _, cloud := range []bool{false, true} {
		t.Run(strconv.FormatBool(cloud), func(t *testing.T) {
			ctx := context.Background()
			userID, sessionID, otherSessionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
			var s *server
			var repo *sessionrepo.SqliteSessionRepo
			var handler http.Handler
			var otherUserID string
			if cloud {
				users := newCloudUsers(t.TempDir(), nil)
				t.Cleanup(users.Close)
				user, err := users.user(userID, true)
				if err != nil {
					t.Fatal(err)
				}
				s, repo, handler = user.server, user.repo, users
				otherUserID = uuid.NewString()
				if _, err := users.user(otherUserID, true); err != nil {
					t.Fatal(err)
				}
			} else {
				s = newTestServer(t, t.TempDir(), false)
				var err error
				repo, err = sessionrepo.NewSqliteSessionRepo(layout.SessionDB(s.homeDir), layout.SessionRoot(s.homeDir))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = repo.Close() })
				s.catalog, s.evaluations = repo, repo
				handler = s.routes()
			}
			workDir := filepath.Join(s.homeDir, "project")
			for _, id := range []string{sessionID, otherSessionID} {
				if _, err := repo.CreateSession(ctx, id, userID, "project", "", workDir, "code"); err != nil {
					t.Fatal(err)
				}
			}
			jobID := "eval_" + sessionID + "_20261016_130000"
			createReportTestJob(t, repo, sessionID, userID, jobID, workDir, evaluation.StatusSucceeded)
			endpoint := "/api/sessions/" + sessionID + "/evaluations/" + jobID + "/report"
			request := func(path string) *httptest.ResponseRecorder {
				return cloudRequest(t, handler, userID, http.MethodGet, path, nil)
			}
			if response := request(endpoint); response.Code != http.StatusNotFound {
				t.Fatalf("missing report: %d %s", response.Code, response.Body.String())
			}
			content := "# 评估报告\n" + strings.Repeat("完整报告内容，不截断。\n", 20000)
			path := layout.EvaluationReport(s.homeDir, jobID)
			if err := writeEvaluationReport(path, content); err != nil {
				t.Fatal(err)
			}
			// The download must ignore even a poisoned legacy absolute report_path.
			poison := filepath.Join(t.TempDir(), "private.md")
			if err := os.WriteFile(poison, []byte("DO_NOT_DOWNLOAD"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := repo.UpdateEvaluationJob(ctx, jobID, evaluation.StatusSucceeded, poison, ""); err != nil {
				t.Fatal(err)
			}
			response := request(endpoint + "?path=" + poison)
			_, params, err := mime.ParseMediaType(response.Header().Get("Content-Disposition"))
			if err != nil || params["filename"] != jobID+".md" || !strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment") {
				t.Fatalf("download disposition: %v %v", response.Header(), err)
			}
			if response.Code != http.StatusOK || response.Body.String() != content || response.Header().Get("Content-Length") != strconv.Itoa(len(content)) || response.Header().Get("Content-Type") != "text/markdown; charset=utf-8" || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("download: status=%d bytes=%d headers=%v", response.Code, response.Body.Len(), response.Header())
			}
			if cloud {
				response := cloudRequest(t, handler, otherUserID, http.MethodGet, endpoint, nil)
				if response.Code != http.StatusNotFound {
					t.Fatalf("cross-user download: %d", response.Code)
				}
			}
			rangeRequest := httptest.NewRequest(http.MethodGet, endpoint, nil)
			rangeRequest.Header.Set("X-Laxcode-User-ID", userID)
			rangeRequest.Header.Set("Range", "bytes=0-9")
			rangeResponse := httptest.NewRecorder()
			handler.ServeHTTP(rangeResponse, rangeRequest)
			if rangeResponse.Code != http.StatusPartialContent || rangeResponse.Body.String() != content[:10] {
				t.Fatalf("range: %d %q", rangeResponse.Code, rangeResponse.Body.String())
			}
			if response := request("/api/sessions/" + otherSessionID + "/evaluations/" + jobID + "/report"); response.Code != http.StatusNotFound {
				t.Fatalf("wrong source: %d", response.Code)
			}
			if response := request("/api/sessions/" + sessionID + "/evaluations/missing/report"); response.Code != http.StatusNotFound {
				t.Fatalf("unknown job: %d", response.Code)
			}
			if response := request("/api/sessions/" + sessionID + "/evaluations/%2e%2e/report"); response.Code != http.StatusBadRequest {
				t.Fatalf("invalid job: %d", response.Code)
			}
			for _, status := range []string{evaluation.StatusQueued, evaluation.StatusRunning, evaluation.StatusFailed} {
				id := jobID + "_" + status
				createReportTestJob(t, repo, sessionID, userID, id, workDir, status)
				if response := request("/api/sessions/" + sessionID + "/evaluations/" + id + "/report"); response.Code != http.StatusConflict {
					t.Fatalf("unready %s: %d", status, response.Code)
				}
			}
			foreign := jobID + "_foreign"
			createReportTestJob(t, repo, sessionID, "another-owner", foreign, workDir, evaluation.StatusSucceeded)
			if response := request("/api/sessions/" + sessionID + "/evaluations/" + foreign + "/report"); response.Code != http.StatusNotFound {
				t.Fatalf("mismatched job owner: %d", response.Code)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(poison, path); err != nil {
				t.Fatal(err)
			}
			if response := request(endpoint); response.Code != http.StatusForbidden {
				t.Fatalf("symlink report: %d", response.Code)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if response := request(endpoint); response.Code != http.StatusForbidden {
				t.Fatalf("directory report: %d", response.Code)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Dir(path)); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Dir(poison), filepath.Dir(path)); err != nil {
				t.Fatal(err)
			}
			if response := request(endpoint); response.Code != http.StatusForbidden {
				t.Fatalf("escaping report directory: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestEvaluationDTOCloudHidesPaths(t *testing.T) {
	users := newCloudUsers(t.TempDir(), nil)
	defer users.Close()
	id, sessionID := uuid.NewString(), uuid.NewString()
	user, err := users.user(id, true)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(user.server.userRoot, "demo")
	if _, err := user.repo.CreateSession(context.Background(), sessionID, id, "project", "", work, "code"); err != nil {
		t.Fatal(err)
	}
	jobID := "eval_" + sessionID
	createReportTestJob(t, user.repo, sessionID, id, jobID, work, evaluation.StatusSucceeded)
	path := layout.EvaluationReport(user.server.homeDir, jobID)
	if err := user.repo.UpdateEvaluationJob(context.Background(), jobID, evaluation.StatusSucceeded, path, "cannot write "+path); err != nil {
		t.Fatal(err)
	}
	response := cloudRequest(t, users, id, http.MethodGet, "/api/evaluations?session_id="+sessionID, nil)
	var page evaluationJobListDTO
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(page.Jobs) != 1 || page.Jobs[0].Status != evaluation.StatusSucceeded || strings.Contains(response.Body.String(), "report_path") || strings.Contains(response.Body.String(), user.server.userRoot) {
		t.Fatalf("cloud DTO: %d %s", response.Code, response.Body.String())
	}
	local := (&server{}).evaluationToDTO(evaluation.Job{ReportPath: path})
	if local.ReportPath != path {
		t.Fatalf("local path compatibility: %+v", local)
	}
}
