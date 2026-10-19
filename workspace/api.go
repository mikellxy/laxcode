// Package workspace serves persisted user data without starting an agent or sandbox.
package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/internal/domain/evaluation"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/infrastructure/ai_models"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

type Catalog = config.Conf

// LoadCatalog reads the public catalog; credential references are deliberately unresolved.
func LoadCatalog(dir string) (Catalog, error) {
	if _, err := os.Stat(layout.SettingsFile(dir)); err != nil {
		return Catalog{}, err
	}
	return config.LoadCatalogFromDir(dir)
}

type Options struct {
	UserID, UserRoot, HomeDir, TempDir, RuntimeRoot string
	History                                         session.SessionHistoryRepository
	Catalog                                         session.SessionCatalogRepository
	Projects                                        session.ProjectRepository
	ContextRepo                                     session.SessionRepository
	Evaluations                                     evaluation.Repository
	Models                                          *ai_models.Manager
	DeleteGuard                                     func(context.Context, string) (func(), error)
}
type API struct {
	Options
	projectMu sync.Mutex
	repo      *sessionrepo.SqliteSessionRepo
}

// Open synchronizes platform files and opens the user's host-owned repository.
func Open(root, id string, catalog Catalog, skills string) (*API, error) {
	home := filepath.Join(root, "agenthome")
	for _, p := range []string{home, layout.Root(home), layout.SessionRoot(home)} {
		if err := realDir(p); err != nil {
			return nil, err
		}
	}
	if err := config.WriteCatalogAtomic(layout.UserSettings(home), catalog); err != nil {
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Lstat(layout.SessionDB(home) + suffix)
		if err == nil && !info.Mode().IsRegular() {
			return nil, errors.New("database must be a regular file")
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	marker := filepath.Join(layout.Root(home), ".skills-initialized")
	_, markerErr := os.Lstat(marker)
	_, skillsErr := os.Lstat(layout.SkillsRoot(home))
	if err := initializeDefaultSkills(skills, layout.SkillsRoot(home)); err != nil {
		return nil, err
	}
	if errors.Is(markerErr, fs.ErrNotExist) {
		if _, err := os.Lstat(marker); err == nil {
			if err := own(marker); err != nil {
				return nil, err
			}
		}
	}
	if errors.Is(skillsErr, fs.ErrNotExist) {
		if err := filepath.WalkDir(layout.SkillsRoot(home), func(p string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return own(p)
		}); err != nil {
			return nil, err
		}
	}

	models, err := ai_models.NewRooted(home, root, catalog)
	if err != nil {
		return nil, err
	}
	repo, err := sessionrepo.NewSqliteSessionRepo(layout.SessionDB(home), layout.SessionRoot(home))
	if err != nil {
		models.Close()
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := own(layout.SessionDB(home) + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			repo.Close()
			models.Close()
			return nil, err
		}
	}
	return &API{Options: Options{UserID: id, UserRoot: root, HomeDir: home, TempDir: filepath.Join(root, "tmp"), RuntimeRoot: "/workplace",
		History: repo, Catalog: repo, Projects: repo, ContextRepo: repo, Evaluations: repo, Models: models}, repo: repo}, nil
}
func (s *API) Close() error {
	if s.Models != nil {
		_ = s.Models.Close()
	}
	if s.repo != nil {
		return s.repo.Close()
	}
	return nil
}
func (s *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if q := r.URL.Query().Get("user_id"); q != "" && q != s.UserID {
		writeJSONError(w, 400, "conflicting user identities")
		return
	}
	if s.Models != nil && (r.URL.Path == "/api/models" || strings.HasSuffix(r.URL.Path, "/context")) {
		if err := s.Models.LoadSelection(); err != nil {
			writeJSONError(w, 500, "cannot load model selection")
			return
		}
	}
	s.Routes().ServeHTTP(w, r)
}
func (s *API) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /skills", s.HandleListSkills)
	mux.HandleFunc("GET /api/models", s.HandleListModels)
	mux.HandleFunc("POST /api/model", s.HandleSwitchModel)
	mux.HandleFunc("POST /api/projects", s.HandleCreateProject)
	mux.HandleFunc("GET /api/projects", s.HandleListProjects)
	mux.HandleFunc("POST /api/sessions", s.HandleCreateSession)
	mux.HandleFunc("GET /api/sessions", s.HandleListSessions)
	mux.HandleFunc("DELETE /api/sessions/{session_id}", s.HandleDeleteSession)
	mux.HandleFunc("GET /api/sessions/{session_id}/messages", s.HandleHistory)
	mux.HandleFunc("GET /api/sessions/{session_id}/context", s.HandleSessionContext)
	mux.HandleFunc("GET /api/sessions/{session_id}/performance", s.HandlePerformance)
	mux.HandleFunc("GET /api/sessions/{session_id}/chats/{chat_id}/files.zip", s.HandleDownloadChatFiles)
	mux.HandleFunc("GET /api/sessions/{session_id}/chats/{chat_id}/file", s.HandlePreviewChatFile)
	mux.HandleFunc("GET /api/evaluations", s.HandleListEvaluations)
	mux.HandleFunc("GET /api/sessions/{session_id}/evaluations/{job_id}/report", s.HandleDownloadEvaluationReport)
	return mux
}

const maxBodyBytes = 1 << 20
const defaultHistoryLimit, maxHistoryLimit, defaultSessionLimit, maxSessionLimit = 50, 100, 20, 100
const ErrorCodeInternal = "INTERNAL_ERROR"

type ContextData struct {
	WindowToken   sharedkernel.TokenStatistics `json:"window_token"`
	ContextWindow int                          `json:"context_window"`
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSONProtocolError(w, status, ErrorCodeInternal, message, "")
}
func writeJSONProtocolError(w http.ResponseWriter, status int, code, message, retry string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Retry   string `json:"retry_action,omitempty"`
	}{code, message, retry})
}
func ParseUserID(raw string) (string, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	return id.String(), nil
}
func (s *API) requestUserID(legacy string) (string, error) {
	if s.UserID == "" {
		return ParseUserID(legacy)
	}
	if legacy != "" && legacy != s.UserID {
		return "", errors.New("conflicting user identities")
	}
	return s.UserID, nil
}

var projectName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,120}$`)

func ValidateProjectName(name string) error {
	if !projectName.MatchString(name) || strings.EqualFold(name, "agenthome") || strings.EqualFold(name, "tmp") {
		return errors.New("invalid or reserved project name")
	}
	return nil
}
func validateCloudProjectName(name string) error { return ValidateProjectName(name) }
func (s *API) runtimeRoot() string {
	if s.RuntimeRoot != "" {
		return s.RuntimeRoot
	}
	return s.UserRoot
}
func (s *API) projectPath(path string) (string, error) {
	for _, root := range []string{s.runtimeRoot(), s.UserRoot} {
		rel, err := filepath.Rel(root, path)
		if err == nil && ValidateProjectName(rel) == nil {
			return rel, nil
		}
	}
	return "", errors.New("project is outside the workplace")
}
func realDir(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return errors.New("metadata directory must be a real directory")
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil {
		return err
	}
	return own(path)
}

// Only freshly created platform files are assigned to the Linux workload identity.
func own(path string) error {
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		return os.Lchown(path, 1000, 1000)
	}
	return nil
}

func (s *API) StoreHandler() http.Handler { return sessionrepo.RPCHandler(s.repo, s.UserID) }

func (s *API) publicError(err error) string {
	return strings.ReplaceAll(strings.ReplaceAll(err.Error(), s.UserRoot, ""), "/workplace", "")
}

func (s *API) InterruptEvaluations(ctx context.Context) error {
	return s.Evaluations.FailActiveEvaluationJobs(ctx, "evaluation interrupted by sandbox stop or deployment")
}
