package run_sse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/infrastructure/ai_models"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

var cloudProjectName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// N100 NAS: 4 CPUs and 8 GB RAM; each Bash sandbox may use 2 CPUs and 1 GiB.
const cloudMaxInFlightChats int32 = 2

func validateCloudProjectName(name string) error {
	if len(name) > 120 || !cloudProjectName.MatchString(name) {
		return errors.New("project_name must contain only letters, digits, hyphens and underscores (1-120 characters)")
	}
	if strings.EqualFold(name, "agenthome") || strings.EqualFold(name, "tmp") {
		return errors.New("project_name is reserved")
	}
	return nil
}

type cloudUser struct {
	server  *server
	handler http.Handler
	repo    *sessionrepo.SqliteSessionRepo
}

// The guest UUID is prototype identity, not authentication. Never share a server's
// mutable home, model selection or repository between different UUIDs.
type cloudUsers struct {
	home    string
	copyDir string
	tracer  telemetry.Tracer
	mu      sync.Mutex
	users   map[string]*cloudUser
	// Shared across all users; held through SSE completion and file-scan cleanup.
	inFlightChats atomic.Int32
}

func (c *cloudUsers) acquireChat() bool {
	for {
		active := c.inFlightChats.Load()
		if active >= cloudMaxInFlightChats {
			return false
		}
		if c.inFlightChats.CompareAndSwap(active, active+1) {
			return true
		}
	}
}

func newCloudUsers(home string, tracer telemetry.Tracer) *cloudUsers {
	copyDir := config.Current.ConfCopyDir
	if copyDir == "" {
		copyDir = layout.Root(home)
	}
	return &cloudUsers{home: home, copyDir: copyDir, tracer: tracer, users: make(map[string]*cloudUser)}
}

func cloudUserID(w http.ResponseWriter, r *http.Request) (string, error) {
	values := []string{r.Header.Get("X-Laxcode-User-ID"), r.URL.Query().Get("user_id")}
	if cookie, err := r.Cookie("laxcode_guest_id"); err == nil {
		values = append(values, cookie.Value)
	}
	if r.Method == http.MethodPost && (r.URL.Path == "/api/projects" || r.URL.Path == "/api/sessions") {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			return "", err
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
		var body struct {
			UserID string `json:"user_id"`
		}
		if err := json.Unmarshal(data, &body); err != nil {
			return "", err
		}
		values = append(values, body.UserID)
	}
	var id string
	for _, value := range values {
		if value == "" {
			continue
		}
		parsed, err := parseUserID(value)
		if err != nil {
			return "", errors.New("user_id must be a UUID")
		}
		if id != "" && id != parsed {
			return "", errors.New("conflicting user identities")
		}
		id = parsed
	}
	if id == "" {
		return "", errors.New("user_id is required (guest cookie or X-Laxcode-User-ID)")
	}
	return id, nil
}

func (c *cloudUsers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/api/config" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"cloud": true})
		return
	}
	if r.URL.Path == "/api/directory-picker" || strings.HasPrefix(r.URL.Path, "/api/auth/") || strings.Contains(r.URL.Path, "/approvals/") {
		writeJSONError(w, http.StatusForbidden, "this endpoint is unavailable in cloud mode")
		return
	}
	id, err := cloudUserID(w, r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	resume := strings.HasPrefix(r.URL.Path, "/api/sessions/") && strings.HasSuffix(r.URL.Path, "/resume")
	if r.Method == http.MethodPost && (r.URL.Path == "/chat" || resume) {
		if !c.acquireChat() {
			w.Header().Set("Retry-After", "5")
			retryAction := RetryActionResend
			if resume {
				retryAction = RetryActionResume
			}
			writeJSONProtocolError(w, http.StatusTooManyRequests, ErrorCodeServerBusy,
				fmt.Sprintf("服务繁忙：最多同时处理 %d 个对话请求，请稍后重试。", cloudMaxInFlightChats), retryAction)
			return
		}
		defer c.inFlightChats.Add(-1)
	}
	create := r.Method == http.MethodPost && r.URL.Path == "/api/projects"
	if create {
		var body createProjectRequest
		data, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSONError(w, 400, err.Error())
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
		if err := json.Unmarshal(data, &body); err != nil {
			writeJSONError(w, 400, err.Error())
			return
		}
		if err := validateCloudProjectName(strings.TrimSpace(body.Name)); err != nil {
			writeJSONError(w, 400, err.Error())
			return
		}
	}
	user, err := c.user(id, create)
	if err != nil {
		writeJSONError(w, 500, "initialize user workspace: "+err.Error())
		return
	}
	if user == nil {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			_, _ = io.WriteString(w, `{"projects":[]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/models":
			_, _ = io.WriteString(w, `{"current_model":"","providers":[]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/skills":
			_, _ = io.WriteString(w, `{"skills":[]}`)
		default:
			writeJSONError(w, http.StatusNotFound, "create a project before using this workspace")
		}
		return
	}
	user.handler.ServeHTTP(w, r)
}

func (c *cloudUsers) user(id string, create bool) (*cloudUser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if user := c.users[id]; user != nil {
		return user, nil
	}
	userRoot := filepath.Join(c.home, "laxcode", id)
	homeDir := filepath.Join(userRoot, "agenthome")
	if !create {
		if _, err := os.Stat(layout.SessionDB(homeDir)); errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
	}
	if err := initializeCloudHome(c.home, id, c.copyDir); err != nil {
		return nil, err
	}
	// Resolve macOS /tmp aliases once; every tool and persisted workdir uses this root.
	userRoot, err := filepath.EvalSymlinks(userRoot)
	if err != nil {
		return nil, err
	}
	homeDir = filepath.Join(userRoot, "agenthome")
	conf, err := config.LoadSettings(homeDir, nil)
	if err != nil {
		return nil, err
	}
	models, err := ai_models.New(homeDir, conf)
	if err != nil {
		return nil, err
	}
	repo, err := sessionrepo.NewSqliteSessionRepo(layout.SessionDB(homeDir), layout.SessionRoot(homeDir))
	if err != nil {
		return nil, err
	}
	if err := repo.FailActiveEvaluationJobs(context.Background(), "evaluation interrupted by server restart"); err != nil {
		_ = repo.Close()
		return nil, err
	}
	s := newServer(homeDir, config.Current.Plan, models)
	s.userRoot, s.tempDir, s.tracer = userRoot, filepath.Join(userRoot, "tmp"), c.tracer
	s.switcher = agentasm.NewModelSwitcher(models)
	s.history, s.catalog, s.projects, s.contextRepo, s.evaluations = repo, repo, repo, repo, repo
	user := &cloudUser{server: s, handler: s.routes(), repo: repo}
	c.users[id] = user
	return user, nil
}

func (c *cloudUsers) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, user := range c.users {
		user.server.stopEvaluations()
		_ = user.repo.Close()
		delete(c.users, id)
	}
}

func initializeCloudHome(serviceHome, id, copyDir string) error {
	base := filepath.Join(serviceHome, "laxcode")
	if err := os.MkdirAll(base, 0700); err != nil {
		return err
	}
	baseRoot, err := os.OpenRoot(base)
	if err != nil {
		return err
	}
	defer baseRoot.Close()
	if err := baseRoot.Mkdir(id, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := baseRoot.Lstat(id)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return errors.New("user root must be a real directory")
	}
	root, err := baseRoot.OpenRoot(id)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll("tmp", 0700); err != nil {
		return err
	}
	for _, name := range []string{"tmp", "agenthome"} {
		if info, err := root.Lstat(name); err == nil && (!info.IsDir() || info.Mode()&fs.ModeSymlink != 0) {
			return fmt.Errorf("%s must be a real directory", name)
		}
	}
	marker := filepath.Join("agenthome", ".initialized")
	if _, err := root.Stat(marker); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	stage := ".agenthome-init-" + uuid.NewString()
	if err := root.Mkdir(stage, 0700); err != nil {
		return err
	}
	defer root.RemoveAll(stage)
	stageHome := filepath.Join(base, id, stage)
	conf, err := config.LoadSettingsFromDir(copyDir, nil)
	if err != nil {
		return err
	}
	// Copy model defaults and skills; API keys go to the host configs.db, never agenthome.
	providers := make([]config.Provider, 0, len(conf.ProviderList))
	for _, provider := range conf.ProviderList {
		if provider.AuthType != "oauth" {
			providers = append(providers, provider)
		}
	}
	hasRef := func(ref string) bool {
		for _, p := range providers {
			for _, m := range p.ModelList {
				if p.ProviderName+":"+m.ModelName == ref {
					return true
				}
			}
		}
		return false
	}
	if !hasRef(conf.Model) {
		conf.Model = ""
	}
	if !hasRef(conf.CompactionModel) {
		conf.CompactionModel = ""
	}
	data, err := json.MarshalIndent(map[string]any{"model": conf.Model, "compaction_model": conf.CompactionModel, "provider_list": config.WithoutProviderAPIKeys(providers)}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(layout.Root(stageHome), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(layout.UserSettings(stageHome), append(data, '\n'), 0600); err != nil {
		return err
	}
	if err := copyCloudSkills(layout.SkillsDir(copyDir), layout.SkillsRoot(stageHome)); err != nil {
		return err
	}
	if err := root.WriteFile(filepath.Join(stage, ".initialized"), []byte("v1\n"), 0600); err != nil {
		return err
	}
	// Failed initialization never publishes a partially copied home.
	if _, err := root.Lstat("agenthome"); err == nil {
		return errors.New("agenthome exists without an initialization marker")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// Use the final configuration scope, not the temporary initialization directory.
	if err := config.SaveProviderAPIKeys(layout.Root(filepath.Join(base, id, "agenthome")), providers); err != nil {
		return err
	}
	return root.Rename(stage, "agenthome")
}

func copyCloudSkills(source, destination string) error {
	if err := os.MkdirAll(destination, 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(source)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("default skill contains symbolic link: %s", name)
		}
		target := filepath.Join(destination, name)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("default skill contains non-regular file: %s", name)
		}
		data, err := root.ReadFile(name)
		if err != nil {
			return err
		}
		perm := fs.FileMode(0600)
		if info.Mode().Perm()&0111 != 0 {
			perm = 0700
		}
		return os.WriteFile(target, data, perm)
	})
}
