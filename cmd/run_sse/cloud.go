package run_sse

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/infrastructure/ai_models"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

var cloudProjectName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func validateCloudProjectName(name string) error {
	if len(name) > 120 || !cloudProjectName.MatchString(name) {
		return errors.New("project_name must contain only letters, digits, hyphens and underscores (1-120 characters)")
	}
	if strings.EqualFold(name, "agenthome") || strings.EqualFold(name, "tmp") {
		return errors.New("project_name is reserved")
	}
	return nil
}

// One process owns one mounted workplace; routing and provisioning belong to the proxy.
type cloudService struct {
	server  *server
	repo    *sessionrepo.SqliteSessionRepo
	handler http.Handler
	userID  string
	active  atomic.Int32
}

func newCloudService(home, id string, conf config.Conf, tracer telemetry.Tracer) (*cloudService, error) {
	id, err := parseUserID(id)
	if err != nil {
		return nil, err
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return nil, err
	}
	if filepath.Base(home) != "agenthome" {
		return nil, errors.New("HOME must be workplace/agenthome")
	}
	root := filepath.Dir(home)
	for _, dir := range []string{layout.Root(home), filepath.Join(root, "tmp")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	if err := initializeDefaultSkills(os.Getenv("LAXCODE_DEFAULT_SKILLS"), layout.SkillsRoot(home)); err != nil {
		return nil, err
	}
	models, err := ai_models.New(home, conf)
	if err != nil {
		return nil, err
	}
	if err := models.LoadSelection(); err != nil {
		return nil, err
	}
	repo, err := sessionrepo.NewSqliteSessionRepo(layout.SessionDB(home), layout.SessionRoot(home))
	if err != nil {
		return nil, err
	}
	if err := repo.FailActiveEvaluationJobs(context.Background(), "evaluation interrupted by server restart"); err != nil {
		repo.Close()
		return nil, err
	}
	s := newServer(home, conf.Plan, models)
	s.userID = id
	s.userRoot, s.tempDir, s.tracer = root, filepath.Join(root, "tmp"), tracer
	s.history, s.catalog, s.projects, s.contextRepo, s.evaluations = repo, repo, repo, repo, repo
	return &cloudService{server: s, repo: repo, handler: s.routes(), userID: id}, nil
}
func (c *cloudService) Close() { c.server.stopEvaluations(); _ = c.repo.Close() }
func (c *cloudService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" && r.URL.Path == "/healthz" {
		w.WriteHeader(200)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/internal/activity" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int32{"active": c.active.Load() + c.server.activeEvaluations.Load()})
		return
	}
	id, err := parseUserID(r.Header.Get("X-USERID"))
	if err != nil || id != c.userID {
		writeJSONError(w, 403, "user does not own this sandbox")
		return
	}
	// Old clients may still supply a query ID; it must never select another identity.
	if q := r.URL.Query().Get("user_id"); q != "" && q != id {
		writeJSONError(w, 400, "conflicting user identities")
		return
	}
	c.active.Add(1)
	defer c.active.Add(-1)
	c.handler.ServeHTTP(w, r)
}
