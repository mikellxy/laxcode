package reactservice

import (
	"context"
	"errors"
	"fmt"
)

// UserQuery carries the immutable user input and the model-facing form through
// the ordered BeforeUserQuery middleware pipeline.
type UserQuery struct {
	SessionID  string
	UserID     string
	Original   string
	ModelInput string
}

// BeforeUserQuery runs synchronously before a user message is persisted. Each
// middleware may transform ModelInput. Implementations own their failure policy:
// returning an error aborts Chat, while fail-open middleware logs internally and
// returns the unchanged query.
type BeforeUserQuery interface {
	Handle(context.Context, UserQuery) (UserQuery, error)
}

// CompletedReactTurn identifies one durably committed, normally completed turn.
// AssistantSeq is the stable boundary used by post-processors to load immutable
// source messages without retaining the whole assistant message.
type CompletedReactTurn struct {
	Turn         uint64
	SessionID    string
	UserID       string
	AssistantSeq uint64
}

// PostReactTurn runs after the completed assistant message has been committed.
// Failures are collected and logged by ReActService and never turn a successful
// Chat into an error.
type PostReactTurn interface {
	Handle(context.Context, CompletedReactTurn) error
}

func (r *ReActService) UseBeforeUserQuery(middleware ...BeforeUserQuery) {
	r.beforeUserQuery = append(r.beforeUserQuery, middleware...)
}

func (r *ReActService) UsePostReactTurn(middleware ...PostReactTurn) {
	r.postReactTurn = append(r.postReactTurn, middleware...)
}

func (r *ReActService) prepareUserQuery(ctx context.Context, raw string) (UserQuery, error) {
	query := UserQuery{
		SessionID:  r.Session.ID,
		UserID:     r.Session.UserID,
		Original:   raw,
		ModelInput: raw,
	}
	for index, middleware := range r.beforeUserQuery {
		next, err := middleware.Handle(ctx, query)
		if err != nil {
			return UserQuery{}, fmt.Errorf("before user query middleware %d (%T): %w", index, middleware, err)
		}
		if next.SessionID != query.SessionID || next.UserID != query.UserID || next.Original != query.Original {
			return UserQuery{}, ErrInvalidUserQueryMiddleware
		}
		query = next
	}
	return query, nil
}

func (r *ReActService) runPostReactTurn(ctx context.Context, turn CompletedReactTurn) error {
	var errs []error
	for index, middleware := range r.postReactTurn {
		if err := middleware.Handle(ctx, turn); err != nil {
			errs = append(errs, fmt.Errorf("post react turn middleware %d (%T): %w", index, middleware, err))
		}
	}
	return errors.Join(errs...)
}
