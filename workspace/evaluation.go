package workspace

import (
	"encoding/json"
	"github.com/mikellxy/laxcode/internal/domain/evaluation"
	"net/http"
	"strings"
	"time"
)

type EvaluationJobDTO struct {
	ID              string     `json:"id"`
	SourceSessionID string     `json:"source_session_id"`
	Requirement     string     `json:"requirement"`
	Status          string     `json:"status"`
	Error           string     `json:"error,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

type EvaluationJobListDTO struct {
	Jobs []EvaluationJobDTO `json:"jobs"`
}

func EvaluationToDTO(job evaluation.Job, root string) EvaluationJobDTO {
	dto := EvaluationJobDTO{
		ID: job.ID, SourceSessionID: job.SourceSessionID, Requirement: job.Requirement,
		Status: job.Status, Error: strings.ReplaceAll(strings.ReplaceAll(job.Error, root, ""), "/workplace", ""),
		CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt, CompletedAt: job.CompletedAt,
	}
	return dto
}

func (s *API) HandleListEvaluations(w http.ResponseWriter, r *http.Request) {
	if s.Evaluations == nil {
		writeJSONError(w, http.StatusInternalServerError, "evaluation repository is unavailable")
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		writeJSONError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	jobs, err := s.Evaluations.ListEvaluationJobs(r.Context(), sessionID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "list evaluation jobs failed: "+s.publicError(err))
		return
	}
	response := EvaluationJobListDTO{Jobs: make([]EvaluationJobDTO, len(jobs))}
	for i := range jobs {
		response.Jobs[i] = EvaluationToDTO(jobs[i], s.UserRoot)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}
