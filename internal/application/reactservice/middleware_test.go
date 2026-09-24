package reactservice

import (
	"context"
	"errors"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
)

type postReactTurnFunc func(context.Context, CompletedReactTurn) error

func (f postReactTurnFunc) Handle(ctx context.Context, turn CompletedReactTurn) error {
	return f(ctx, turn)
}

func TestBeforeUserQueryRunsInOrder(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("middleware-order", repo)
	msg := assistantMsg("answer")
	msg.FinishReason = sharedkernel.FinishReasonStop
	llm := &scriptedLLM{responses: []scriptedResp{{msg: msg}}}
	svc := NewReActService(sess, repo, llm, nil, tools.NewDefaultRegistry(nil), nil, nil)
	var order []string
	svc.UseBeforeUserQuery(
		beforeUserQueryFunc(func(_ context.Context, query UserQuery) (UserQuery, error) {
			order = append(order, "knowledge")
			query.ModelInput += "|knowledge"
			return query, nil
		}),
		beforeUserQueryFunc(func(_ context.Context, query UserQuery) (UserQuery, error) {
			order = append(order, "memory")
			if query.ModelInput != "question|knowledge" {
				t.Fatalf("second middleware saw %q", query.ModelInput)
			}
			query.ModelInput += "|memory"
			return query, nil
		}),
	)

	if _, err := svc.Chat(context.Background(), "question"); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "knowledge" || order[1] != "memory" {
		t.Fatalf("order = %v", order)
	}
	if got := llm.lastMsgs[1]; got.Content != "question" || got.WrappedContent != "question|knowledge|memory" {
		t.Fatalf("user message = %+v", got)
	}
}

func TestBeforeUserQueryCanAbort(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("middleware-abort", repo)
	llm := &scriptedLLM{}
	svc := NewReActService(sess, repo, llm, nil, tools.NewDefaultRegistry(nil), nil, nil)
	wantErr := errors.New("retrieval unavailable")
	laterCalled := false
	svc.UseBeforeUserQuery(
		beforeUserQueryFunc(func(context.Context, UserQuery) (UserQuery, error) {
			return UserQuery{}, wantErr
		}),
		beforeUserQueryFunc(func(_ context.Context, query UserQuery) (UserQuery, error) {
			laterCalled = true
			return query, nil
		}),
	)

	if _, err := svc.Chat(context.Background(), "question"); !errors.Is(err, wantErr) {
		t.Fatalf("Chat error = %v", err)
	}
	if laterCalled || llm.calls != 0 || len(sess.Messages) != 1 {
		t.Fatalf("later=%v llm_calls=%d messages=%d", laterCalled, llm.calls, len(sess.Messages))
	}
}

func TestBeforeUserQueryRejectsImmutableFieldChanges(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("middleware-immutable", repo)
	svc := NewReActService(sess, repo, &scriptedLLM{}, nil, tools.NewDefaultRegistry(nil), nil, nil)
	svc.UseBeforeUserQuery(beforeUserQueryFunc(func(_ context.Context, query UserQuery) (UserQuery, error) {
		query.SessionID = "other"
		return query, nil
	}))

	if _, err := svc.Chat(context.Background(), "question"); !errors.Is(err, ErrInvalidUserQueryMiddleware) {
		t.Fatalf("Chat error = %v", err)
	}
}

func TestPostReactTurnRunsAllAndDoesNotFailChat(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("middleware-post", repo)
	msg := assistantMsg("answer")
	msg.FinishReason = sharedkernel.FinishReasonStop
	svc := NewReActService(sess, repo, &scriptedLLM{responses: []scriptedResp{{msg: msg}}}, nil, tools.NewDefaultRegistry(nil), nil, nil)
	wantErr := errors.New("enqueue unavailable")
	var got []CompletedReactTurn
	svc.UsePostReactTurn(
		postReactTurnFunc(func(context.Context, CompletedReactTurn) error { return wantErr }),
		postReactTurnFunc(func(_ context.Context, turn CompletedReactTurn) error {
			got = append(got, turn)
			return nil
		}),
	)

	result, err := svc.Chat(context.Background(), "question")
	if err != nil || result.Content != "answer" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(got) != 1 || got[0].Turn != 1 || got[0].SessionID != sess.ID || got[0].AssistantSeq != result.Seq {
		t.Fatalf("post event = %+v, result=%+v", got, result)
	}
}
