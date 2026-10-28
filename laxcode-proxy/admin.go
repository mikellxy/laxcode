package main

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
	"golang.org/x/time/rate"
)

// Release builds inject only the username, salt and password hash, never plaintext.
var adminConfig string

const adminPasswordIterations = 600000
const adminTokenLifetime = 2 * time.Hour
const adminJWTHeader = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"

type adminCredentials struct {
	Username string `json:"username"`
	Salt     []byte `json:"salt"`
	Hash     []byte `json:"hash"`
}

type adminAuth struct {
	credentials adminCredentials
	key         []byte
	loginLimit  *rate.Limiter
}

type adminClaims struct {
	Subject   string `json:"sub"`
	Issuer    string `json:"iss"`
	Audience  string `json:"aud"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

func (a *adminAuth) sign(input string) string {
	mac := hmac.New(sha256.New, a.key)
	_, _ = mac.Write([]byte(input))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *adminAuth) token(now time.Time) string {
	claims, _ := json.Marshal(adminClaims{a.credentials.Username, "laxcode-proxy", "laxcode-admin", now.Unix(), now.Add(adminTokenLifetime).Unix()})
	input := adminJWTHeader + "." + base64.RawURLEncoding.EncodeToString(claims)
	return input + "." + a.sign(input)
}

func (a *adminAuth) valid(token string, now time.Time) bool {
	if len(token) > 4096 {
		return false
	}
	parts := strings.Split(token, ".")
	// This service accepts only its own HS256 tokens; algorithm negotiation is forbidden.
	if len(parts) != 3 || parts[0] != adminJWTHeader || !hmac.Equal([]byte(parts[2]), []byte(a.sign(parts[0]+"."+parts[1]))) {
		return false
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims adminClaims
	if json.Unmarshal(data, &claims) != nil {
		return false
	}
	return claims.Subject == a.credentials.Username && claims.Issuer == "laxcode-proxy" && claims.Audience == "laxcode-admin" &&
		claims.IssuedAt <= now.Unix() && claims.ExpiresAt > now.Unix() && claims.ExpiresAt > claims.IssuedAt && claims.ExpiresAt-claims.IssuedAt <= int64(adminTokenLifetime/time.Second)
}

func (a *adminAuth) login(w http.ResponseWriter, r *http.Request) {
	// A single global budget also bounds password hashing behind a reverse proxy;
	// do not trust client-supplied forwarding headers for rate-limit identity.
	if !a.loginLimit.Allow() {
		w.Header().Set("Retry-After", "3")
		jsonError(w, 429, "登录尝试过于频繁，请稍后重试")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || len(input.Password) > 1024 {
		jsonError(w, 400, "invalid login request")
		return
	}
	hash, err := pbkdf2.Key(sha256.New, input.Password, a.credentials.Salt, adminPasswordIterations, 32)
	passwordOK := err == nil && hmac.Equal(hash, a.credentials.Hash)
	usernameOK := hmac.Equal([]byte(input.Username), []byte(a.credentials.Username))
	if !passwordOK || !usernameOK {
		jsonError(w, 401, "管理员账号或密码错误")
		return
	}
	writeAdminJSON(w, map[string]any{"token": a.token(time.Now()), "expires_in": int(adminTokenLifetime / time.Second)})
}

func (p *proxyServer) adminHandler(encoded string) (http.Handler, error) {
	if encoded == "" {
		return nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	var credentials adminCredentials
	if err != nil || json.Unmarshal(data, &credentials) != nil || credentials.Username == "" || len(credentials.Username) > 128 || len(credentials.Salt) != 16 || len(credentials.Hash) != 32 {
		return nil, errors.New("invalid compiled admin configuration")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	auth := &adminAuth{credentials: credentials, key: key, loginLimit: rate.NewLimiter(rate.Every(3*time.Second), 5)}
	protected := http.NewServeMux()
	protected.HandleFunc("GET /admin/api/me", func(w http.ResponseWriter, r *http.Request) {
		writeAdminJSON(w, map[string]string{"username": credentials.Username})
	})
	protected.HandleFunc("GET /admin/api/users", p.adminUsers)
	protected.HandleFunc("GET /admin/api/users/{user_id}/projects", p.adminWorkspace("projects"))
	protected.HandleFunc("GET /admin/api/users/{user_id}/sessions", p.adminWorkspace("sessions"))
	protected.HandleFunc("GET /admin/api/users/{user_id}/sessions/{session_id}/messages", p.adminWorkspace("messages"))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/api/login", auth.login)
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") || !auth.valid(strings.TrimPrefix(values[0], "Bearer "), time.Now()) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			jsonError(w, 401, "管理员登录已失效，请重新登录")
			return
		}
		protected.ServeHTTP(w, r)
	}))
	return mux, nil
}

func writeAdminJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}

// Require an existing real workplace. Admin reads must never provision an unknown user.
func (p *proxyServer) existingAdminUser(id string) bool {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed == uuid.Nil || parsed.String() != id {
		return false
	}
	root := filepath.Join(p.root, id)
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	marker, err := os.Lstat(filepath.Join(root, ".initialized"))
	return err == nil && marker.Mode().IsRegular()
}

func (p *proxyServer) adminUsers(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if value := r.URL.Query().Get("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 100 {
			jsonError(w, 400, "limit must be between 1 and 100")
			return
		}
	}
	after := r.URL.Query().Get("after_user_id")
	if after != "" {
		if id, err := uuid.Parse(after); err != nil || id == uuid.Nil || id.String() != after {
			jsonError(w, 400, "invalid user cursor")
			return
		}
	}
	entries, err := os.ReadDir(p.root)
	if err != nil {
		jsonError(w, 500, "cannot list users")
		return
	}
	type user struct {
		UserID string `json:"user_id"`
	}
	users := []user{}
	more := false
	for _, entry := range entries {
		if entry.Name() <= after || !entry.IsDir() || !p.existingAdminUser(entry.Name()) {
			continue
		}
		if len(users) == limit {
			more = true
			break
		}
		users = append(users, user{entry.Name()})
	}
	next := ""
	if more {
		next = users[len(users)-1].UserID
	}
	writeAdminJSON(w, struct {
		Users   []user `json:"users"`
		HasMore bool   `json:"has_more"`
		Next    string `json:"next_after_user_id,omitempty"`
	}{users, more, next})
}

func (p *proxyServer) adminWorkspace(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("user_id")
		if !p.existingAdminUser(id) {
			jsonError(w, 404, "user not found")
			return
		}
		if claimed := r.URL.Query().Get("user_id"); claimed != "" && claimed != id {
			jsonError(w, 400, "conflicting user identities")
			return
		}
		u := p.user(id)
		if err := u.lock(r.Context()); err != nil {
			return
		}
		defer u.unlock()
		if err := p.prepareWorkspace(id, u); err != nil {
			jsonError(w, 500, "workspace unavailable")
			return
		}
		api := u.api
		if kind == "sessions" || kind == "messages" {
			raw := r.URL.Query().Get("project_id")
			if kind == "messages" {
				raw = r.PathValue("session_id")
			}
			if parsed, err := uuid.Parse(raw); err != nil || parsed == uuid.Nil || parsed.String() != raw {
				jsonError(w, 400, "project_id or session_id must be a canonical nonzero UUID")
				return
			}
		}
		switch kind {
		case "projects":
			api.HandleListProjects(w, r)
		case "sessions":
			project, err := api.Projects.GetProject(r.Context(), r.URL.Query().Get("project_id"))
			if errors.Is(err, sessionrepo.ErrProjectNotFound) || err == nil && project.UserID != id {
				jsonError(w, 404, "project not found")
				return
			}
			if err != nil {
				jsonError(w, 500, "cannot load project")
				return
			}
			api.HandleListSessions(w, r)
		case "messages":
			session, err := api.Catalog.GetSession(r.Context(), r.PathValue("session_id"))
			if errors.Is(err, sessionrepo.ErrSessionNotFound) || err == nil && session.UserID != id {
				jsonError(w, 404, "session not found")
				return
			}
			if err != nil {
				jsonError(w, 500, "cannot load session")
				return
			}
			api.HandleHistory(w, r)
		}
	}
}
