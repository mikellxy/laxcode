package run_sse

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

func writeTestSkill(t *testing.T, home, name, description string) {
	t.Helper()
	dir := filepath.Join(layout.SkillsRoot(home), name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: "+description+"\n---\nPrivate skill body."), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestListSkills(t *testing.T) {
	home := t.TempDir()
	s := &server{homeDir: home}
	request := func() *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		s.routes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/skills", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		return response
	}
	if got := strings.TrimSpace(request().Body.String()); got != `{"skills":[]}` {
		t.Fatalf("empty skills: %s", got)
	}
	writeTestSkill(t, home, "z-last", "last skill")
	writeTestSkill(t, home, "a-first", "first skill")
	writeTestSkill(t, home, "INVALID", "invalid skill")
	response := request()
	var got map[string][]map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string][]map[string]string{"skills": {
		{"name": "a-first", "description": "first skill"},
		{"name": "z-last", "description": "last skill"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("response=%v want=%v", got, want)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("skills response must not be cached by shared proxies")
	}
}
