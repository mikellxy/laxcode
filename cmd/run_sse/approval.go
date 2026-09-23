package run_sse

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/internal/application/reactservice"
)

// approvalBroker connects a still-open SSE request to a separate HTTP reply.
// Pending approvals are removed when answered or when their stream exits.
type approvalBroker struct {
	mu      sync.Mutex
	pending map[string]pendingApproval
}

type pendingApproval struct {
	sessionID string
	requestID string
	reply     chan<- string
}

func newApprovalBroker() *approvalBroker {
	return &approvalBroker{pending: make(map[string]pendingApproval)}
}

func (b *approvalBroker) register(sessionID, requestID string, reply chan<- string) string {
	id := uuid.NewString()
	b.mu.Lock()
	b.pending[id] = pendingApproval{sessionID: sessionID, requestID: requestID, reply: reply}
	b.mu.Unlock()
	return id
}

func (b *approvalBroker) resolve(sessionID, id string, approved bool) bool {
	b.mu.Lock()
	pending, ok := b.pending[id]
	if ok && pending.sessionID == sessionID {
		delete(b.pending, id)
	} else {
		ok = false
	}
	b.mu.Unlock()
	if !ok {
		return false
	}
	answer := "no"
	if approved {
		answer = "yes"
	}
	// ReActService creates a buffered channel, so a simultaneous disconnect
	// cannot leave this HTTP request blocked on a canceled stream.
	select {
	case pending.reply <- answer:
	default:
	}
	return true
}

func (b *approvalBroker) clearRequest(requestID string) {
	b.mu.Lock()
	for id, pending := range b.pending {
		if pending.requestID == requestID {
			delete(b.pending, id)
		}
	}
	b.mu.Unlock()
}

type approvalReply struct {
	Approved *bool `json:"approved"`
}

func (s *server) handleApproval(w http.ResponseWriter, r *http.Request) {
	var answer approvalReply
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&answer); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid approval: "+err.Error())
		return
	}
	if answer.Approved == nil {
		writeJSONError(w, http.StatusBadRequest, "approved is required")
		return
	}
	if !s.approvals.resolve(r.PathValue("session_id"), r.PathValue("approval_id"), *answer.Approved) {
		writeJSONError(w, http.StatusNotFound, "approval is no longer pending")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// budgetStates holds one checkpoint per code session for this SSE server run.
// A request restores it after loading the session and writes it back on exit.
type budgetStates struct {
	mu     sync.Mutex
	states map[string]reactservice.TokenBudgetState
}

func newBudgetStates() *budgetStates {
	return &budgetStates{states: make(map[string]reactservice.TokenBudgetState)}
}

func (b *budgetStates) get(sessionID string) (reactservice.TokenBudgetState, bool) {
	b.mu.Lock()
	state, ok := b.states[sessionID]
	b.mu.Unlock()
	return state, ok
}

func (b *budgetStates) put(sessionID string, state reactservice.TokenBudgetState) {
	b.mu.Lock()
	b.states[sessionID] = state
	b.mu.Unlock()
}
