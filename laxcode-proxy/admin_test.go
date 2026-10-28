package main

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

const adminTestPassword = "test-password-$()'中文"

func adminFixture(t *testing.T) (*proxyServer, *fakeRuntime, string) {
	t.Helper()
	runtime := &fakeRuntime{}
	p := newProxy(runtime, t.TempDir(), fstest.MapFS{"index.html": {Data: []byte("frontend")}}, time.Minute, 2)
	t.Cleanup(p.closeWorkspaces)
	t.Cleanup(p.transport.CloseIdleConnections)
	salt := []byte("0123456789abcdef")
	hash, err := pbkdf2.Key(sha256.New, adminTestPassword, salt, adminPasswordIterations, 32)
	if err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(adminCredentials{"admin", salt, hash})
	p.admin, err = p.adminHandler(base64.StdEncoding.EncodeToString(config))
	if err != nil {
		t.Fatal(err)
	}
	w := adminCall(p, "POST", "/admin/api/login", "", map[string]string{"username": "admin", "password": adminTestPassword})
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Token == "" {
		t.Fatal("missing login token", err)
	}
	return p, runtime, response.Token
}

func adminCall(p *proxyServer, method, path, token string, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}

func TestAdminLoginAndRouteIsolation(t *testing.T) {
	p, runtime, token := adminFixture(t)
	for _, tc := range []struct {
		method, path, token string
		status              int
	}{
		{"GET", "/admin/api/me", token, 200},
		{"GET", "/admin/api/users", "", 401},
		{"GET", "/admin/api/users", token + "broken", 401},
		{"GET", "/admin/api/users/" + testID + "/projects", "", 401},
		{"GET", "/admin/api/users/" + testID + "/sessions", "", 401},
		{"GET", "/admin/api/users/" + testID + "/sessions/" + testID + "/messages", "", 401},
		{"DELETE", "/admin/api/users/" + testID, token, 404},
		{"POST", "/admin/api/users", token, 405},
		{"GET", "/admin/api/unknown", token, 404},
		{"GET", "/", "", 200}, {"GET", "/admin", "", 200}, {"GET", "/admin/", "", 200},
		{"GET", "/api/projects", token, 400}, // Admin token never replaces guest identity.
		{"POST", "/internal/store", token, 404},
	} {
		w := adminCall(p, tc.method, tc.path, tc.token, nil)
		if w.Code != tc.status {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
		if strings.HasPrefix(tc.path, "/admin/api") && w.Header().Get("Cache-Control") != "no-store" {
			t.Error("admin response may be cached")
		}
	}
	for _, input := range []map[string]string{{"username": "other", "password": adminTestPassword}, {"username": "admin", "password": "wrong"}} {
		if w := adminCall(p, "POST", "/admin/api/login", "", input); w.Code != 401 {
			t.Fatalf("wrong credentials: %d", w.Code)
		}
	}
	if w := metadataRequest(t, p, "GET", "/api/projects", nil); w.Code != 200 {
		t.Fatal("guest API requires admin JWT", w.Code)
	}
	if runtime.creates != 0 || runtime.starts != 0 {
		t.Fatal("admin request woke sandbox")
	}
	for i := 0; i < 6; i++ {
		adminCall(p, "POST", "/admin/api/login", "", nil)
	}
	if w := adminCall(p, "POST", "/admin/api/login", "", nil); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal("login not rate limited")
	}
}

func TestAdminJWTValidation(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	a := &adminAuth{credentials: adminCredentials{Username: "admin"}, key: []byte("01234567890123456789012345678901")}
	token := a.token(now)
	if !a.valid(token, now) {
		t.Fatal("valid token rejected")
	}
	if a.valid(token, now.Add(adminTokenLifetime)) || a.valid(token, now.Add(-time.Second)) {
		t.Fatal("invalid token time accepted")
	}
	other := *a
	other.key = []byte("different-key")
	if other.valid(token, now) {
		t.Fatal("token survived signing key change")
	}
	for _, header := range []string{`{"alg":"none","typ":"JWT"}`, `{"alg":"HS512","typ":"JWT"}`} {
		parts := strings.Split(token, ".")
		input := base64.RawURLEncoding.EncodeToString([]byte(header)) + "." + parts[1]
		if a.valid(input+"."+a.sign(input), now) {
			t.Fatal("unexpected algorithm accepted")
		}
	}
	for _, change := range []func(*adminClaims){
		func(c *adminClaims) { c.Subject = "other" }, func(c *adminClaims) { c.Issuer = "other" },
		func(c *adminClaims) { c.Audience = "store" }, func(c *adminClaims) { c.ExpiresAt = 0 },
		func(c *adminClaims) { c.ExpiresAt += 1 },
	} {
		claims := adminClaims{"admin", "laxcode-proxy", "laxcode-admin", now.Unix(), now.Add(adminTokenLifetime).Unix()}
		change(&claims)
		data, _ := json.Marshal(claims)
		input := adminJWTHeader + "." + base64.RawURLEncoding.EncodeToString(data)
		if a.valid(input+"."+a.sign(input), now) {
			t.Fatalf("invalid claims accepted: %+v", claims)
		}
	}
}

func TestAdminPersistedBrowsingAndOwnership(t *testing.T) {
	p, runtime, token := adminFixture(t)
	projectID := responseID(t, metadataRequest(t, p, "POST", "/api/projects", map[string]string{"name": "demo"}), "project_id")
	sessionID := responseID(t, metadataRequest(t, p, "POST", "/api/sessions", map[string]string{"project_id": projectID}), "session_id")
	api := p.user(testID).api
	ctx := context.Background()
	if _, err := api.Catalog.RenameSession(ctx, sessionID, testID, "后台查看测试"); err != nil {
		t.Fatal(err)
	}
	repo := api.ContextRepo
	snapshot, err := repo.GetRequestContext(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	sess := session.NewSession(sessionID, "/workplace/demo")
	sess.RequestContext = snapshot
	sys := sess.UpsertSysMessage("system")
	sess.Revision, err = repo.CommitCreateMessage(ctx, sessionID, sess.Snapshot(), sys, sys)
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"older message", "newer message"} {
		msg := sharedkernel.Message{Role: sharedkernel.RoleUser, Content: content}
		candidate, err := sess.WithAppendedMessage(&msg)
		if err != nil {
			t.Fatal(err)
		}
		candidate.Revision, err = repo.CommitCreateMessage(ctx, sessionID, candidate.Snapshot(), msg, msg)
		if err != nil {
			t.Fatal(err)
		}
		sess = candidate
	}
	base := "/admin/api/users/" + testID
	for _, path := range []string{"/admin/api/users", base + "/projects", base + "/sessions?project_id=" + projectID, base + "/sessions/" + sessionID + "/messages"} {
		w := adminCall(p, "GET", path, token, nil)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w := adminCall(p, "GET", base+"/sessions?project_id="+projectID, token, nil)
	if !strings.Contains(w.Body.String(), "后台查看测试") || !strings.Contains(w.Body.String(), "created_at") {
		t.Fatal("missing session metadata")
	}
	w = adminCall(p, "GET", base+"/sessions/"+sessionID+"/messages?limit=1", token, nil)
	var page struct {
		HasMore bool   `json:"has_more"`
		Before  uint64 `json:"next_before_seq"`
	}
	if json.Unmarshal(w.Body.Bytes(), &page) != nil || !page.HasMore || page.Before == 0 || !strings.Contains(w.Body.String(), "newer message") || strings.Contains(w.Body.String(), "older message") {
		t.Fatalf("pagination: %s", w.Body.String())
	}
	otherID := "22222222-2222-4222-8222-222222222222"
	if _, err := initializeWorkplace(p.root, otherID); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"/sessions?project_id=" + projectID, "/sessions/" + sessionID + "/messages"} {
		if w := adminCall(p, "GET", "/admin/api/users/"+otherID+suffix, token, nil); w.Code != 404 {
			t.Fatalf("cross-user data: %d %s", w.Code, w.Body.String())
		}
	}
	if w := adminCall(p, "GET", base+"/projects?user_id="+otherID, token, nil); w.Code != 400 {
		t.Fatal("identity conflict accepted")
	}
	unknown := "33333333-3333-4333-8333-333333333333"
	if w := adminCall(p, "GET", "/admin/api/users/"+unknown+"/projects", token, nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if _, err := os.Stat(filepath.Join(p.root, unknown)); !os.IsNotExist(err) {
		t.Fatal("unknown admin user was provisioned")
	}
	if runtime.creates != 0 || runtime.starts != 0 {
		t.Fatal("browsing woke sandbox")
	}
}

func TestAdminUserDiscoveryAndPagination(t *testing.T) {
	p, _, token := adminFixture(t)
	ids := []string{testID, "22222222-2222-4222-8222-222222222222"}
	for _, id := range ids {
		if _, err := initializeWorkplace(p.root, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(p.root, testID), filepath.Join(p.root, "33333333-3333-4333-8333-333333333333")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(p.root, ".init-incomplete"), 0700); err != nil {
		t.Fatal(err)
	}
	// No user has been opened in this process; discovery must work after restart.
	if len(p.users) != 0 {
		t.Fatal("fixture has cached users")
	}
	w := adminCall(p, "GET", "/admin/api/users?limit=1", token, nil)
	if !strings.Contains(w.Body.String(), `"next_after_user_id":"`+testID+`"`) {
		t.Fatal(w.Body.String())
	}
	w = adminCall(p, "GET", "/admin/api/users?limit=1&after_user_id="+testID, token, nil)
	if !strings.Contains(w.Body.String(), ids[1]) || !strings.Contains(w.Body.String(), `"has_more":false`) {
		t.Fatal(w.Body.String())
	}
	if len(p.users) != 0 {
		t.Fatal("listing users opened workspaces")
	}
	for _, path := range []string{"/admin/api/users?limit=101", "/admin/api/users?after_user_id=bad"} {
		if w := adminCall(p, "GET", path, token, nil); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
}

func TestAdminInvalidBuildConfiguration(t *testing.T) {
	p := &proxyServer{}
	for _, config := range []string{"bad", base64.StdEncoding.EncodeToString([]byte(`{"username":"admin"}`))} {
		if _, err := p.adminHandler(config); err == nil {
			t.Fatal("invalid build config accepted")
		}
	}
	if handler, err := p.adminHandler(""); handler != nil || err != nil {
		t.Fatal("unconfigured development build should disable admin")
	}
}
