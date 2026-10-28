package sessionrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mikellxy/laxcode/internal/domain/evaluation"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"io"
	"net/http"
	"strings"
	"time"
)

// Repository is the shared persistence contract; only the host opens SQLite.
type Repository interface {
	session.TaskRepository
	CreateSession(ctx context.Context, id, userID, projectID, title, workDir, mode string) (session.Summary, error)
	GetSession(ctx context.Context, id string) (session.Summary, error)
	RenameSession(ctx context.Context, id, userID, title string) (session.Summary, error)
	DeleteSession(ctx context.Context, id, userID string) error
	ListSessions(ctx context.Context, userID, projectID, beforeSessionID string, limit int) (session.SummaryPage, error)
	CreateProject(ctx context.Context, id, userID, name, workDir string) (session.Project, error)
	GetProject(ctx context.Context, id string) (session.Project, error)
	ListProjects(ctx context.Context, userID string) ([]session.Project, error)
	ListOriginalHistory(ctx context.Context, id string, beforeSeq uint64, limit int) (session.HistoryPage, bool, error)
	GetRequestContext(ctx context.Context, id string) (session.RequestContext, error)
	CommitCreateMessage(ctx context.Context, id string, snapshot session.RequestContext, original, memory sharedkernel.Message) (uint64, error)
	CommitUpdateMessage(ctx context.Context, id string, snapshot session.RequestContext, memory sharedkernel.Message) (uint64, error)
	CommitNextMemoryGeneration(ctx context.Context, id string, snapshot session.RequestContext) (uint64, error)
	UpsertChatFiles(ctx context.Context, sessionID, chatID string, files []session.ChatFile) error
	ListChatFiles(ctx context.Context, sessionID, chatID string) ([]session.ChatFile, error)
	CreateEvaluationJob(ctx context.Context, job evaluation.Job) (evaluation.Job, error)
	UpdateEvaluationJob(ctx context.Context, id, status, reportPath, errorMessage string) error
	ListEvaluationJobs(ctx context.Context, sourceSessionID string) ([]evaluation.Job, error)
	FailActiveEvaluationJobs(ctx context.Context, errorMessage string) error
	Close() error
}
type CreateSessionRequest struct {
	Id        string
	UserID    string
	ProjectID string
	Title     string
	WorkDir   string
	Mode      string
}
type CreateSessionResponse struct{ V0 session.Summary }
type GetSessionRequest struct{ Id string }
type GetSessionResponse struct{ V0 session.Summary }
type RenameSessionRequest struct{ Id, UserID, Title string }
type RenameSessionResponse struct{ V0 session.Summary }
type DeleteSessionRequest struct {
	Id     string
	UserID string
}
type ListSessionsRequest struct {
	UserID          string
	ProjectID       string
	BeforeSessionID string
	Limit           int
}
type ListSessionsResponse struct{ V0 session.SummaryPage }
type CreateProjectRequest struct {
	Id      string
	UserID  string
	Name    string
	WorkDir string
}
type CreateProjectResponse struct{ V0 session.Project }
type GetProjectRequest struct{ Id string }
type GetProjectResponse struct{ V0 session.Project }
type ListProjectsRequest struct{ UserID string }
type ListProjectsResponse struct{ V0 []session.Project }
type ListOriginalHistoryRequest struct {
	Id        string
	BeforeSeq uint64
	Limit     int
}
type ListOriginalHistoryResponse struct {
	V0 session.HistoryPage
	V1 bool
}
type GetRequestContextRequest struct{ Id string }
type GetRequestContextResponse struct{ V0 contextWire }
type CommitCreateMessageRequest struct {
	Id       string
	Snapshot contextWire
	Original messageWire
	Memory   messageWire
}
type CommitCreateMessageResponse struct{ V0 uint64 }
type CommitUpdateMessageRequest struct {
	Id       string
	Snapshot contextWire
	Memory   messageWire
}
type CommitUpdateMessageResponse struct{ V0 uint64 }
type CommitNextMemoryGenerationRequest struct {
	Id       string
	Snapshot contextWire
}
type CommitNextMemoryGenerationResponse struct{ V0 uint64 }
type UpsertChatFilesRequest struct {
	SessionID string
	ChatID    string
	Files     []session.ChatFile
}
type ListChatFilesRequest struct {
	SessionID string
	ChatID    string
}
type ListChatFilesResponse struct{ V0 []session.ChatFile }
type CreateEvaluationJobRequest struct{ Job evaluation.Job }
type CreateEvaluationJobResponse struct{ V0 evaluation.Job }
type UpdateEvaluationJobRequest struct {
	Id           string
	Status       string
	ReportPath   string
	ErrorMessage string
}
type ListEvaluationJobsRequest struct{ SourceSessionID string }
type ListEvaluationJobsResponse struct{ V0 []evaluation.Job }
type FailActiveEvaluationJobsRequest struct{ ErrorMessage string }

type CreateTaskRequest struct {
	Id        string
	SessionID string
}
type CreateTaskResponse struct{ V0 session.Task }
type GetTaskRequest struct{ Id string }
type GetTaskResponse struct{ V0 session.Task }
type GetLatestTaskRequest struct{ Id string }
type GetLatestTaskResponse struct{ V0 *session.Task }
type GetActiveProjectTaskRequest struct{ Id string }
type GetActiveProjectTaskResponse struct{ V0 *session.Task }
type ListTasksRequest struct{}
type ListTasksResponse struct{ V0 []session.Task }
type StartTaskRequest struct{ Id string }
type StartTaskResponse struct{}
type FinishTaskRequest struct {
	Id      string
	Status  string
	Code    string
	Message string
	FinalID uint64
}
type FinishTaskResponse struct{}
type ReserveMessageSequenceRequest struct{ Id string }
type ReserveMessageSequenceResponse struct{ V0 uint64 }

var rpcErrors = []error{ErrContextConflict, ErrStaleSequence, ErrStaleGeneration, ErrSessionNotFound, ErrSessionBusy, ErrProjectNotFound, ErrChatNotFound, ErrEvaluationJobNotFound, ErrTaskNotFound, ErrTaskConflict}

type rpcResponse struct {
	Result json.RawMessage
	Error  string
	Code   int
}

func RPCHandler(repo Repository, userID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
		if err != nil {
			http.Error(w, "invalid storage request", 400)
			return
		}
		var result any
		switch r.URL.Query().Get("method") {
		case "CreateSession":
			var in CreateSessionRequest
			if err = json.Unmarshal(body, &in); err == nil {
				if in.UserID != userID {
					http.Error(w, "invalid identity", 403)
					return
				}
				var out CreateSessionResponse
				out.V0, err = repo.CreateSession(r.Context(), in.Id, in.UserID, in.ProjectID, in.Title, in.WorkDir, in.Mode)
				result = out
			}
		case "GetSession":
			var in GetSessionRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out GetSessionResponse
				out.V0, err = repo.GetSession(r.Context(), in.Id)
				result = out
			}
		case "RenameSession":
			var in RenameSessionRequest
			if err = json.Unmarshal(body, &in); err == nil {
				if in.UserID != userID {
					http.Error(w, "invalid identity", 403)
					return
				}
				var out RenameSessionResponse
				out.V0, err = repo.RenameSession(r.Context(), in.Id, in.UserID, in.Title)
				result = out
			}
		case "DeleteSession":
			var in DeleteSessionRequest
			if err = json.Unmarshal(body, &in); err == nil {
				if in.UserID != userID {
					http.Error(w, "invalid identity", 403)
					return
				}
				var out struct{}
				err = repo.DeleteSession(r.Context(), in.Id, in.UserID)
				result = out
			}
		case "ListSessions":
			var in ListSessionsRequest
			if err = json.Unmarshal(body, &in); err == nil {
				if in.UserID != userID {
					http.Error(w, "invalid identity", 403)
					return
				}
				var out ListSessionsResponse
				out.V0, err = repo.ListSessions(r.Context(), in.UserID, in.ProjectID, in.BeforeSessionID, in.Limit)
				result = out
			}
		case "CreateProject":
			var in CreateProjectRequest
			if err = json.Unmarshal(body, &in); err == nil {
				if in.UserID != userID {
					http.Error(w, "invalid identity", 403)
					return
				}
				var out CreateProjectResponse
				out.V0, err = repo.CreateProject(r.Context(), in.Id, in.UserID, in.Name, in.WorkDir)
				result = out
			}
		case "GetProject":
			var in GetProjectRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out GetProjectResponse
				out.V0, err = repo.GetProject(r.Context(), in.Id)
				result = out
			}
		case "ListProjects":
			var in ListProjectsRequest
			if err = json.Unmarshal(body, &in); err == nil {
				if in.UserID != userID {
					http.Error(w, "invalid identity", 403)
					return
				}
				var out ListProjectsResponse
				out.V0, err = repo.ListProjects(r.Context(), in.UserID)
				result = out
			}
		case "ListOriginalHistory":
			var in ListOriginalHistoryRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out ListOriginalHistoryResponse
				out.V0, out.V1, err = repo.ListOriginalHistory(r.Context(), in.Id, in.BeforeSeq, in.Limit)
				result = out
			}
		case "GetRequestContext":
			var in GetRequestContextRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out GetRequestContextResponse
				state, loadErr := repo.GetRequestContext(r.Context(), in.Id)
				out.V0, err = packContext(state), loadErr
				result = out
			}
		case "CommitCreateMessage":
			var in CommitCreateMessageRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out CommitCreateMessageResponse
				out.V0, err = repo.CommitCreateMessage(r.Context(), in.Id, in.Snapshot.context(), in.Original.message(), in.Memory.message())
				result = out
			}
		case "CommitUpdateMessage":
			var in CommitUpdateMessageRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out CommitUpdateMessageResponse
				out.V0, err = repo.CommitUpdateMessage(r.Context(), in.Id, in.Snapshot.context(), in.Memory.message())
				result = out
			}
		case "CommitNextMemoryGeneration":
			var in CommitNextMemoryGenerationRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out CommitNextMemoryGenerationResponse
				out.V0, err = repo.CommitNextMemoryGeneration(r.Context(), in.Id, in.Snapshot.context())
				result = out
			}
		case "UpsertChatFiles":
			var in UpsertChatFilesRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out struct{}
				err = repo.UpsertChatFiles(r.Context(), in.SessionID, in.ChatID, in.Files)
				result = out
			}
		case "ListChatFiles":
			var in ListChatFilesRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out ListChatFilesResponse
				out.V0, err = repo.ListChatFiles(r.Context(), in.SessionID, in.ChatID)
				result = out
			}
		case "CreateEvaluationJob":
			var in CreateEvaluationJobRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out CreateEvaluationJobResponse
				out.V0, err = repo.CreateEvaluationJob(r.Context(), in.Job)
				result = out
			}
		case "UpdateEvaluationJob":
			var in UpdateEvaluationJobRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out struct{}
				err = repo.UpdateEvaluationJob(r.Context(), in.Id, in.Status, in.ReportPath, in.ErrorMessage)
				result = out
			}
		case "ListEvaluationJobs":
			var in ListEvaluationJobsRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out ListEvaluationJobsResponse
				out.V0, err = repo.ListEvaluationJobs(r.Context(), in.SourceSessionID)
				result = out
			}
		case "FailActiveEvaluationJobs":
			var in FailActiveEvaluationJobsRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out struct{}
				err = repo.FailActiveEvaluationJobs(r.Context(), in.ErrorMessage)
				result = out
			}
		case "CreateTask":
			var in CreateTaskRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out CreateTaskResponse
				out.V0, err = repo.CreateTask(r.Context(), in.Id, in.SessionID)
				result = out
			}
		case "GetTask":
			var in GetTaskRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out GetTaskResponse
				out.V0, err = repo.GetTask(r.Context(), in.Id)
				result = out
			}
		case "GetLatestTask":
			var in GetLatestTaskRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out GetLatestTaskResponse
				out.V0, err = repo.GetLatestTask(r.Context(), in.Id)
				result = out
			}
		case "GetActiveProjectTask":
			var in GetActiveProjectTaskRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out GetActiveProjectTaskResponse
				out.V0, err = repo.GetActiveProjectTask(r.Context(), in.Id)
				result = out
			}
		case "ListTasks":
			var in ListTasksRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out ListTasksResponse
				out.V0, err = repo.ListTasks(r.Context())
				result = out
			}
		case "StartTask":
			var in StartTaskRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out StartTaskResponse
				err = repo.StartTask(r.Context(), in.Id)
				result = out
			}
		case "FinishTask":
			var in FinishTaskRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out FinishTaskResponse
				err = repo.FinishTask(r.Context(), in.Id, in.Status, in.Code, in.Message, in.FinalID)
				result = out
			}
		case "ReserveMessageSequence":
			var in ReserveMessageSequenceRequest
			if err = json.Unmarshal(body, &in); err == nil {
				var out ReserveMessageSequenceResponse
				out.V0, err = repo.ReserveMessageSequence(r.Context(), in.Id)
				result = out
			}

		default:
			http.NotFound(w, r)
			return
		}
		out := rpcResponse{}
		if err != nil {
			out.Error = err.Error()
			for i, e := range rpcErrors {
				if errors.Is(err, e) {
					out.Code = i + 1
					break
				}
			}
		} else {
			out.Result, _ = json.Marshal(result)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}

type Remote struct {
	URL, UserID, Token string
	Client             *http.Client
}

func NewRemote(url, userID, token string) *Remote {
	return &Remote{URL: url, UserID: userID, Token: token, Client: &http.Client{Timeout: 30 * time.Second}}
}
func (r *Remote) Close() error { r.Client.CloseIdleConnections(); return nil }

var ErrWriteUncertain = errors.New("storage write result is unknown; reload persisted state before retrying")

func (r *Remote) call(ctx context.Context, method string, in, out any) (err error) {
	pending := false
	write := !strings.HasPrefix(method, "Get") && !strings.HasPrefix(method, "List")
	defer func() {
		if err != nil && write && pending {
			err = fmt.Errorf("%w: %v", ErrWriteUncertain, err)
		}
	}()
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", r.URL+"?method="+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-USERID", r.UserID)
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Content-Type", "application/json")
	pending = true
	resp, err := r.Client.Do(req)
	if err != nil {
		return errors.New("storage temporarily unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 408 {
			pending = false
		}
		return fmt.Errorf("storage request failed (%d)", resp.StatusCode)
	}
	var result rpcResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&result); err != nil {
		return err
	}
	if result.Error != "" {
		pending = false
		if result.Code > 0 && result.Code <= len(rpcErrors) {
			return fmt.Errorf("%w: %s", rpcErrors[result.Code-1], result.Error)
		}
		return errors.New(result.Error)
	}
	if out != nil {
		if err := json.Unmarshal(result.Result, out); err != nil {
			return err
		}
	}
	pending = false
	return nil
}
func (r *Remote) CreateSession(ctx context.Context, id, userID, projectID, title, workDir, mode string) (session.Summary, error) {
	var out CreateSessionResponse
	err := r.call(ctx, "CreateSession", CreateSessionRequest{Id: id, UserID: userID, ProjectID: projectID, Title: title, WorkDir: workDir, Mode: mode}, &out)
	return out.V0, err
}
func (r *Remote) GetSession(ctx context.Context, id string) (session.Summary, error) {
	var out GetSessionResponse
	err := r.call(ctx, "GetSession", GetSessionRequest{Id: id}, &out)
	return out.V0, err
}
func (r *Remote) DeleteSession(ctx context.Context, id, userID string) error {
	return r.call(ctx, "DeleteSession", DeleteSessionRequest{Id: id, UserID: userID}, nil)
}
func (r *Remote) RenameSession(ctx context.Context, id, userID, title string) (session.Summary, error) {
	var out RenameSessionResponse
	err := r.call(ctx, "RenameSession", RenameSessionRequest{Id: id, UserID: userID, Title: title}, &out)
	return out.V0, err
}
func (r *Remote) ListSessions(ctx context.Context, userID, projectID, beforeSessionID string, limit int) (session.SummaryPage, error) {
	var out ListSessionsResponse
	err := r.call(ctx, "ListSessions", ListSessionsRequest{UserID: userID, ProjectID: projectID, BeforeSessionID: beforeSessionID, Limit: limit}, &out)
	return out.V0, err
}
func (r *Remote) CreateProject(ctx context.Context, id, userID, name, workDir string) (session.Project, error) {
	var out CreateProjectResponse
	err := r.call(ctx, "CreateProject", CreateProjectRequest{Id: id, UserID: userID, Name: name, WorkDir: workDir}, &out)
	return out.V0, err
}
func (r *Remote) GetProject(ctx context.Context, id string) (session.Project, error) {
	var out GetProjectResponse
	err := r.call(ctx, "GetProject", GetProjectRequest{Id: id}, &out)
	return out.V0, err
}
func (r *Remote) ListProjects(ctx context.Context, userID string) ([]session.Project, error) {
	var out ListProjectsResponse
	err := r.call(ctx, "ListProjects", ListProjectsRequest{UserID: userID}, &out)
	return out.V0, err
}
func (r *Remote) ListOriginalHistory(ctx context.Context, id string, beforeSeq uint64, limit int) (session.HistoryPage, bool, error) {
	var out ListOriginalHistoryResponse
	err := r.call(ctx, "ListOriginalHistory", ListOriginalHistoryRequest{Id: id, BeforeSeq: beforeSeq, Limit: limit}, &out)
	return out.V0, out.V1, err
}
func (r *Remote) GetRequestContext(ctx context.Context, id string) (session.RequestContext, error) {
	var out GetRequestContextResponse
	err := r.call(ctx, "GetRequestContext", GetRequestContextRequest{Id: id}, &out)
	return out.V0.context(), err
}
func (r *Remote) CommitCreateMessage(ctx context.Context, id string, snapshot session.RequestContext, original, memory sharedkernel.Message) (uint64, error) {
	var out CommitCreateMessageResponse
	err := r.call(ctx, "CommitCreateMessage", CommitCreateMessageRequest{Id: id, Snapshot: packContext(snapshot), Original: packMessage(original), Memory: packMessage(memory)}, &out)
	return out.V0, err
}
func (r *Remote) CommitUpdateMessage(ctx context.Context, id string, snapshot session.RequestContext, memory sharedkernel.Message) (uint64, error) {
	var out CommitUpdateMessageResponse
	err := r.call(ctx, "CommitUpdateMessage", CommitUpdateMessageRequest{Id: id, Snapshot: packContext(snapshot), Memory: packMessage(memory)}, &out)
	return out.V0, err
}
func (r *Remote) CommitNextMemoryGeneration(ctx context.Context, id string, snapshot session.RequestContext) (uint64, error) {
	var out CommitNextMemoryGenerationResponse
	err := r.call(ctx, "CommitNextMemoryGeneration", CommitNextMemoryGenerationRequest{Id: id, Snapshot: packContext(snapshot)}, &out)
	return out.V0, err
}
func (r *Remote) UpsertChatFiles(ctx context.Context, sessionID, chatID string, files []session.ChatFile) error {
	return r.call(ctx, "UpsertChatFiles", UpsertChatFilesRequest{SessionID: sessionID, ChatID: chatID, Files: files}, nil)
}
func (r *Remote) ListChatFiles(ctx context.Context, sessionID, chatID string) ([]session.ChatFile, error) {
	var out ListChatFilesResponse
	err := r.call(ctx, "ListChatFiles", ListChatFilesRequest{SessionID: sessionID, ChatID: chatID}, &out)
	return out.V0, err
}
func (r *Remote) CreateEvaluationJob(ctx context.Context, job evaluation.Job) (evaluation.Job, error) {
	var out CreateEvaluationJobResponse
	err := r.call(ctx, "CreateEvaluationJob", CreateEvaluationJobRequest{Job: job}, &out)
	return out.V0, err
}
func (r *Remote) UpdateEvaluationJob(ctx context.Context, id, status, reportPath, errorMessage string) error {
	return r.call(ctx, "UpdateEvaluationJob", UpdateEvaluationJobRequest{Id: id, Status: status, ReportPath: reportPath, ErrorMessage: errorMessage}, nil)
}
func (r *Remote) ListEvaluationJobs(ctx context.Context, sourceSessionID string) ([]evaluation.Job, error) {
	var out ListEvaluationJobsResponse
	err := r.call(ctx, "ListEvaluationJobs", ListEvaluationJobsRequest{SourceSessionID: sourceSessionID}, &out)
	return out.V0, err
}
func (r *Remote) FailActiveEvaluationJobs(ctx context.Context, errorMessage string) error {
	return r.call(ctx, "FailActiveEvaluationJobs", FailActiveEvaluationJobsRequest{ErrorMessage: errorMessage}, nil)
}

// Persistence-only fields are intentionally absent from public JSON/JSONL.
// Carry them explicitly over the internal storage protocol.
type messageWire struct {
	State   sharedkernel.Message
	Display string
}

func packMessage(m sharedkernel.Message) messageWire { return messageWire{m, m.DisplayContent} }
func (m messageWire) message() sharedkernel.Message {
	m.State.DisplayContent = m.Display
	return m.State
}

type contextWire struct {
	State    session.RequestContext
	Revision uint64
	Displays []string
}

func packContext(s session.RequestContext) contextWire {
	displays := make([]string, len(s.Messages))
	for i, m := range s.Messages {
		displays[i] = m.DisplayContent
	}
	return contextWire{s, s.Revision, displays}
}
func (s contextWire) context() session.RequestContext {
	s.State.Revision = s.Revision
	for i, display := range s.Displays {
		if i < len(s.State.Messages) {
			s.State.Messages[i].DisplayContent = display
		}
	}
	return s.State
}
func (r *Remote) CreateTask(ctx context.Context, id string, sessionid string) (session.Task, error) {
	var out CreateTaskResponse
	err := r.call(ctx, "CreateTask", CreateTaskRequest{Id: id, SessionID: sessionid}, &out)
	return out.V0, err
}
func (r *Remote) GetTask(ctx context.Context, id string) (session.Task, error) {
	var out GetTaskResponse
	err := r.call(ctx, "GetTask", GetTaskRequest{Id: id}, &out)
	return out.V0, err
}
func (r *Remote) GetLatestTask(ctx context.Context, id string) (*session.Task, error) {
	var out GetLatestTaskResponse
	err := r.call(ctx, "GetLatestTask", GetLatestTaskRequest{Id: id}, &out)
	return out.V0, err
}
func (r *Remote) GetActiveProjectTask(ctx context.Context, id string) (*session.Task, error) {
	var out GetActiveProjectTaskResponse
	err := r.call(ctx, "GetActiveProjectTask", GetActiveProjectTaskRequest{Id: id}, &out)
	return out.V0, err
}
func (r *Remote) ListTasks(ctx context.Context) ([]session.Task, error) {
	var out ListTasksResponse
	err := r.call(ctx, "ListTasks", ListTasksRequest{}, &out)
	return out.V0, err
}
func (r *Remote) StartTask(ctx context.Context, id string) error {
	var out StartTaskResponse
	err := r.call(ctx, "StartTask", StartTaskRequest{Id: id}, &out)
	return err
}
func (r *Remote) FinishTask(ctx context.Context, id string, status string, code string, message string, finalid uint64) error {
	var out FinishTaskResponse
	err := r.call(ctx, "FinishTask", FinishTaskRequest{Id: id, Status: status, Code: code, Message: message, FinalID: finalid}, &out)
	return err
}
func (r *Remote) ReserveMessageSequence(ctx context.Context, id string) (uint64, error) {
	var out ReserveMessageSequenceResponse
	err := r.call(ctx, "ReserveMessageSequence", ReserveMessageSequenceRequest{Id: id}, &out)
	return out.V0, err
}
