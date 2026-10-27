package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUserConfigIdentityPersistenceAndVersionChanges(t *testing.T) {
	p, runtime := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("sandbox must not be called") }))
	p.skills = t.TempDir()
	request := func(method, id, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/user-config", strings.NewReader(body))
		r.Header.Set("X-USERID", id)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}
	get := func(id string) map[string]any {
		t.Helper()
		w := request("GET", id, "")
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("GET: %d %s", w.Code, w.Body.String())
		}
		var values map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &values); err != nil {
			t.Fatal(err)
		}
		return values
	}
	if w := request("GET", "invalid", ""); w.Code != 400 {
		t.Fatalf("identity: %d", w.Code)
	}
	values := get(testID)
	if values["user_guide_version"] != guideVersion || values["user_guide_seen_version"] != nil {
		t.Fatalf("new user: %v", values)
	}
	body, _ := json.Marshal(map[string]string{"user_guide_seen_version": guideVersion})
	for range 2 {
		if w := request("PUT", testID, string(body)); w.Code != 204 {
			t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
		}
	}
	for _, body := range []string{`{"unknown":true}`, `{"user_guide_version":"new"}`, `{"user_guide_seen_version":1}`, `{"user_guide_seen_version":null}`, `null`, `[]`, `{} {}`, strings.Repeat("x", 4097)} {
		if w := request("PUT", testID, body); w.Code != 400 {
			t.Fatalf("invalid config accepted: %d %s", w.Code, body)
		}
	}
	if w := request("PUT", testID, `{}`); w.Code != 204 {
		t.Fatalf("empty update: %d", w.Code)
	}
	if get(testID)["user_guide_seen_version"] != guideVersion {
		t.Fatal("update lost saved version")
	}
	if get("22222222-2222-4222-8222-222222222222")["user_guide_seen_version"] != nil {
		t.Fatal("user config leaked across users")
	}
	if runtime.creates != 0 || runtime.starts != 0 {
		t.Fatal("config access started sandbox")
	}
	root, skills := p.root, p.skills
	p.closeWorkspaces()
	p, runtime = fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("sandbox must not be called") }))
	p.root, p.skills = root, skills
	if get(testID)["user_guide_seen_version"] != guideVersion {
		t.Fatal("restart lost saved version")
	}
	// Simulate a newly deployed version while an old browser tab is still open.
	p.user(testID).api.GuideVersion = "next-version"
	values = get(testID)
	if values["user_guide_version"] != "next-version" || values["user_guide_seen_version"] != guideVersion {
		t.Fatalf("version change: %v", values)
	}
	if w := request("PUT", testID, string(body)); w.Code != 409 {
		t.Fatalf("stale acknowledgement: %d", w.Code)
	}
	if get(testID)["user_guide_seen_version"] != guideVersion {
		t.Fatal("stale acknowledgement changed saved version")
	}
	if w := request("PUT", testID, `{"user_guide_seen_version":"next-version"}`); w.Code != 204 {
		t.Fatalf("new acknowledgement: %d", w.Code)
	}
	if get(testID)["user_guide_seen_version"] != "next-version" {
		t.Fatal("new version not saved")
	}
	if runtime.creates != 0 || runtime.starts != 0 {
		t.Fatal("config access started sandbox")
	}
}
