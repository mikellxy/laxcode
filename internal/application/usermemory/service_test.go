package usermemory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/llmprovider"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

type testRepo struct {
	lastError  string
	status     string
	saved      int
	reconciled int
	enqueued   []reactservice.CompletedReactTurn
}

func (r *testRepo) ReconcileMemoryJobs(context.Context) error { r.reconciled++; return nil }
func (r *testRepo) EnqueueMemoryJob(_ context.Context, sessionID, userID string, endTurn, assistantSeq uint64) error {
	r.enqueued = append(r.enqueued, reactservice.CompletedReactTurn{SessionID: sessionID, UserID: userID, Turn: endTurn, AssistantSeq: assistantSeq})
	return nil
}

func (*testRepo) ClaimMemoryJob(context.Context, time.Time, time.Duration) (*session.MemoryJob, error) {
	return nil, nil
}
func (r *testRepo) SaveMemorySummary(_ context.Context, j *session.MemoryJob, s string) error {
	r.saved++
	j.Summary = &s
	return nil
}
func (r *testRepo) FinishMemoryJob(_ context.Context, _ *session.MemoryJob, s, e string, _ time.Time) error {
	r.status = s
	r.lastError = e
	return nil
}

type testPipeline struct {
	calls int
	err   error
}

func (p *testPipeline) Ingest(context.Context, *session.MemoryJob, string) error {
	p.calls++
	return p.err
}

type testLLM struct {
	calls int
	text  string
}

func (m *testLLM) Generate(context.Context, []sharedkernel.Message, []sharedkernel.ToolDefinition) (*sharedkernel.Message, error) {
	m.calls++
	return &sharedkernel.Message{Content: m.text, FinishReason: "stop"}, nil
}
func (*testLLM) GenerateStream(context.Context, []sharedkernel.Message, []sharedkernel.ToolDefinition, func(sharedkernel.StreamChunk)) (*sharedkernel.Message, error) {
	panic("unexpected")
}
func (*testLLM) CountInputTokens(context.Context, []sharedkernel.Message, []sharedkernel.ToolDefinition) (int, error) {
	return 10, nil
}
func (*testLLM) ContextBudget() llmprovider.ContextBudget {
	return llmprovider.ContextBudget{ContextWindow: 1000, ReservedOutputTokens: 100}
}
func TestWorkerCachesSummaryAcrossPipelineRetry(t *testing.T) {
	repo := &testRepo{}
	llm := &testLLM{text: `["用户偏好中文"]`}
	pipeline := &testPipeline{err: fmt.Errorf("temporary")}
	w := &Worker{Repo: repo, LLM: llm, Pipeline: pipeline, Timeout: time.Second}
	job := &session.MemoryJob{Attempts: 1, SourceMessages: `[{"role":"user","content":"用中文"}]`}
	w.process(context.Background(), job)
	if repo.status != "retry" || job.Summary == nil || llm.calls != 1 {
		t.Fatal("summary not checkpointed")
	}
	pipeline.err = nil
	job.Attempts++
	w.process(context.Background(), job)
	if repo.status != "succeeded" || llm.calls != 1 || pipeline.calls != 2 {
		t.Fatal("cached summary not reused")
	}
}
func TestWorkerSkipsEmptyAndBoundsFailures(t *testing.T) {
	repo := &testRepo{}
	llm := &testLLM{text: `[]`}
	pipeline := &testPipeline{}
	w := &Worker{Repo: repo, LLM: llm, Pipeline: pipeline, Timeout: time.Second}
	w.process(context.Background(), &session.MemoryJob{Attempts: 1})
	if repo.status != "skipped" || pipeline.calls != 0 {
		t.Fatal("empty summary embedded")
	}
	llm.text = `invalid`
	w.process(context.Background(), &session.MemoryJob{Attempts: 5})
	if repo.status != "failed" {
		t.Fatal("unbounded retry")
	}
	llm.text = `["fact"]`
	pipeline.err = ErrPermanent
	w.process(context.Background(), &session.MemoryJob{Attempts: 1})
	if repo.status != "failed" {
		t.Fatal("permanent error retried")
	}
}

func TestWorkerSkipsDisabledJobsBeforeAnyModelCall(t *testing.T) {
	for _, cached := range []bool{false, true} {
		repo := &testRepo{}
		// Nil model and pipeline would panic if the skip guard were too late.
		w := &Worker{Repo: repo, Timeout: time.Second, Ready: func() bool { return false }}
		job := &session.MemoryJob{Attempts: 1}
		if cached {
			summary := "cached summary"
			job.Summary = &summary
		}
		w.process(context.Background(), job)
		if repo.status != "skipped" || repo.saved != 0 || repo.lastError != "" {
			t.Fatalf("disabled job: %+v", repo)
		}
	}
}

func TestWorkerRechecksReadinessForEachJob(t *testing.T) {
	ready := true
	repo := &testRepo{}
	llm := &testLLM{text: `[]`}
	w := &Worker{Repo: repo, LLM: llm, Timeout: time.Second, Ready: func() bool { return ready }}
	w.process(context.Background(), &session.MemoryJob{Attempts: 1})
	ready = false
	w.process(context.Background(), &session.MemoryJob{Attempts: 1})
	if llm.calls != 1 || repo.saved != 1 || repo.status != "skipped" {
		t.Fatal("readiness was not checked before each summary")
	}
}

func TestPostTurnSchedulerOnlyEnqueuesEachThirdTurn(t *testing.T) {
	repo := &testRepo{}
	scheduler := &PostTurnScheduler{Repo: repo}
	for _, turn := range []reactservice.CompletedReactTurn{
		{SessionID: "s", UserID: "u", Turn: 2, AssistantSeq: 4},
		{SessionID: "s", UserID: "u", Turn: 3, AssistantSeq: 6},
		{SessionID: "s", Turn: 6, AssistantSeq: 12},
	} {
		if err := scheduler.Handle(context.Background(), turn); err != nil {
			t.Fatal(err)
		}
	}
	if len(repo.enqueued) != 1 || repo.enqueued[0].Turn != 3 || repo.enqueued[0].AssistantSeq != 6 {
		t.Fatalf("enqueued = %+v", repo.enqueued)
	}
}
