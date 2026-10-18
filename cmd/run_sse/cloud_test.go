package run_sse

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Test routing models separate sandbox processes; production has no user dispatcher.
type testCloudUsers struct {
	home  string
	users map[string]*cloudService
}

func newCloudUsers(home string, _ telemetry.Tracer) *testCloudUsers {
	return &testCloudUsers{home: home, users: map[string]*cloudService{}}
}
func (c *testCloudUsers) user(id string, _ bool) (*cloudService, error) {
	if s := c.users[id]; s != nil {
		return s, nil
	}
	home := filepath.Join(c.home, id, "agenthome")
	if e := os.MkdirAll(home, 0700); e != nil {
		return nil, e
	}
	s, e := newCloudService(home, id, config.Conf{}, nil)
	if e == nil {
		c.users[id] = s
	}
	return s, e
}
func (c *testCloudUsers) Close() {
	for _, s := range c.users {
		s.Close()
	}
}
func (c *testCloudUsers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.Header.Get("X-USERID")
	s, e := c.user(id, true)
	if e != nil {
		writeJSONError(w, 400, e.Error())
		return
	}
	s.ServeHTTP(w, r)
}
func cloudRequest(t *testing.T, h http.Handler, id, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, e := json.Marshal(body)
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(string(b)))
	if id == "" {
		id = r.URL.Query().Get("user_id")
	}
	r.Header.Set("X-USERID", id)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestSingleSandboxIdentityAndPersistence(t *testing.T) {
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "agenthome")
	if e := os.Mkdir(home, 0700); e != nil {
		t.Fatal(e)
	}
	id := uuid.NewString()
	service, e := newCloudService(home, id, config.Conf{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if got := cloudRequest(t, service, uuid.NewString(), "GET", "/api/models", nil); got.Code != 403 {
		t.Fatal(got.Code)
	}
	if got := cloudRequest(t, service, id, "POST", "/api/models", map[string]string{"api_key": "secret"}); got.Code != 405 {
		t.Fatal(got.Code)
	}
	result := cloudRequest(t, service, id, "POST", "/api/projects", map[string]string{"user_id": id, "name": "demo"})
	if result.Code != 201 {
		t.Fatal(result.Body.String())
	}
	service.Close()
	service, e = newCloudService(home, id, config.Conf{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer service.Close()
	projects, e := service.repo.ListProjects(context.Background(), id)
	if e != nil || len(projects) != 1 || projects[0].WorkDir != filepath.Join(root, "demo") {
		t.Fatalf("projects=%v err=%v", projects, e)
	}
}
