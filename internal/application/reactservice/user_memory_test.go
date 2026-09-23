package reactservice

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/llmprovider"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

type memoryTestLLM struct {
	reason  string
	fail    bool
	seen    []sharedkernel.Message
	release bool
}

func (m *memoryTestLLM) Generate(context.Context, []sharedkernel.Message, []sharedkernel.ToolDefinition) (*sharedkernel.Message, error) {
	panic("unused")
}
func (m *memoryTestLLM) GenerateStream(_ context.Context, msgs []sharedkernel.Message, defs []sharedkernel.ToolDefinition, _ func(sharedkernel.StreamChunk)) (*sharedkernel.Message, error) {
	if len(defs) > 0 {
		panic("SSE tools exposed")
	}
	m.seen = sharedkernel.CloneMessages(msgs)
	if m.fail {
		return nil, errors.New("LLM failed")
	}
	return &sharedkernel.Message{Role: "assistant", Content: "answer", FinishReason: m.reason}, nil
}
func (m *memoryTestLLM) CountInputTokens(_ context.Context, msgs []sharedkernel.Message, _ []sharedkernel.ToolDefinition) (int, error) {
	// release=true 模拟“旧历史仍挂着 chunks 导致占用超高”：
	// 当前轮（最后一条）保留 chunks 是新语义，只有两条以上消息带
	// chunks 才算超预算；回收旧 chunks 后立即回到低占用。
	if m.release {
		chunked := 0
		for _, msg := range msgs {
			if len(msg.MemoryChunks) > 0 {
				chunked++
			}
		}
		if chunked > 1 {
			return 850, nil
		}
	}
	return 100, nil
}
func (m *memoryTestLLM) ContextBudget() llmprovider.ContextBudget {
	return llmprovider.ContextBudget{ContextWindow: 1100, ReservedOutputTokens: 100}
}

type memoryTestRecall struct {
	calls int
	fail  bool
}

func (m *memoryTestRecall) Recall(_ context.Context, user, query string) ([]sharedkernel.MemoryChunk, error) {
	m.calls++
	if m.fail {
		return nil, errors.New("offline")
	}
	return []sharedkernel.MemoryChunk{{ID: "x", Content: "recalled preference"}}, nil
}

func TestUserMemoryChatResumeAndReclaim(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := sessionrepo.NewSqliteSessionRepo(filepath.Join(dir, "session.db"), filepath.Join(dir, "history"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	repo.CreateSession(ctx, "test", "user", "", "")
	llm := &memoryTestLLM{reason: "max_output_tokens"}
	recall := &memoryTestRecall{}
	service := NewReActService(session.NewSession("test"), repo, llm, nil, tools.NewDefaultRegistry(nil), nil, nil)
	service.EnableUserMemory(recall)
	if err := service.InitSession(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.InitSysPrompt(ctx, "system"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Chat(ctx, "raw query"); err != nil {
		t.Fatal(err)
	}
	if service.Session.ReactTurnCount != 0 {
		t.Fatal("incomplete counted")
	}
	if len(llm.seen[1].MemoryChunks) != 1 || llm.seen[1].Content != "raw query" {
		t.Fatal("structured recall lost")
	}
	llm.reason = "stop"
	if _, err := service.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	if recall.calls != 1 || service.Session.ReactTurnCount != 1 {
		t.Fatal("resume recalled or miscounted")
	}
	if _, err := service.Resume(ctx); !errors.Is(err, ErrNothingToResume) {
		t.Fatal("completed turn resumed")
	}
	llm.release = true
	if _, err := service.Chat(ctx, "next query"); err != nil {
		t.Fatal(err)
	}
	if service.Session.ReactTurnCount != 2 || service.Session.MemoryGeneration != 2 {
		t.Fatal("chunk-only compaction failed")
	}
	for i, m := range llm.seen {
		if i == len(llm.seen)-1 {
			if len(m.MemoryChunks) != 1 {
				t.Fatal("current turn chunks must be kept")
			}
			continue
		}
		if len(m.MemoryChunks) > 0 {
			t.Fatal("old chunks not reclaimed")
		}
	}
	recall.fail = true
	llm.release = false
	if _, err := service.Chat(ctx, "third query"); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimMemoryJob(ctx, time.Now().UTC(), time.Second)
	if err != nil || job == nil {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if strings.Contains(job.SourceMessages, "recalled preference") || job.EndTurn != 3 {
		t.Fatal("summary source polluted")
	}
	llm.fail = true
	if _, err := service.Chat(ctx, "failed query"); err == nil {
		t.Fatal("expected failure")
	}
	if service.Session.ReactTurnCount != 3 {
		t.Fatal("failed generation counted")
	}
}
