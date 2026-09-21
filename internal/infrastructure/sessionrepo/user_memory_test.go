package sessionrepo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func TestMemoryTurnsAtomicWindowsAndLeaseRecovery(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTestRepo(t)
	if _, err := repo.CreateSession(ctx, "memory", "user-1", ""); err != nil {
		t.Fatal(err)
	}
	s := createSystem(t, repo, "memory", "secret system")
	for turn := uint64(1); turn <= 6; turn++ {
		appendMessage(t, repo, s, &sharedkernel.Message{Role: "user", Content: "user fact"})
		msg := sharedkernel.Message{Role: "assistant", Content: "answer", ReasoningContent: "secret reasoning", FinishReason: "stop", ReactTurn: turn}
		candidate, err := s.WithAppendedMessage(&msg)
		if err != nil {
			t.Fatal(err)
		}
		candidate.ReactTurnCount = turn
		rev, err := repo.CommitCreateMessage(ctx, s.ID, candidate.Snapshot(), msg, msg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.CommitCreateMessage(ctx, s.ID, candidate.Snapshot(), msg, msg); err == nil {
			t.Fatal("duplicate commit accepted")
		}
		candidate.Revision = rev
		*s = *candidate
		restored, err := repo.GetRequestContext(ctx, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		if restored.ReactTurnCount != turn || restored.UserID != "user-1" {
			t.Fatalf("restore: %+v", restored)
		}
		if turn == 3 {
			next := s.Clone()
			next.AdvanceMemoryGeneration()
			rev, err := repo.CommitNextMemoryGeneration(ctx, s.ID, next.Snapshot())
			if err != nil {
				t.Fatal(err)
			}
			next.Revision = rev
			*s = *next
		}
	}
	var jobs []session.MemoryJob
	if err := repo.db.Order("end_turn").Find(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].StartTurn != 1 || jobs[1].StartTurn != 4 {
		t.Fatalf("jobs=%+v", jobs)
	}
	for _, job := range jobs {
		var source []session.MemorySource
		if err := json.Unmarshal([]byte(job.SourceMessages), &source); err != nil {
			t.Fatal(err)
		}
		if len(source) != 6 || strings.Contains(job.SourceMessages, "secret") {
			t.Fatalf("invalid sources: %s", job.SourceMessages)
		}
	}
	var count int64
	repo.db.Model(&messageModel{}).Where("react_turn IS NOT NULL AND message_type='original'").Count(&count)
	if count != 6 {
		t.Fatalf("markers=%d", count)
	}
	now := time.Now().UTC()
	first, err := repo.ClaimMemoryJob(ctx, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveMemorySummary(ctx, first, "cached"); err != nil {
		t.Fatal(err)
	}
	recovered, err := repo.ClaimMemoryJob(ctx, now.Add(2*time.Second), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ID != first.ID || recovered.Attempts != 2 || recovered.Summary == nil || *recovered.Summary != "cached" {
		t.Fatalf("lease recovery=%+v", recovered)
	}
	if err := repo.FinishMemoryJob(ctx, first, "succeeded", "", now); err == nil {
		t.Fatal("stale lease accepted")
	}
}

func TestMemoryCompletionRollbackAndChunkStorage(t *testing.T) {
	ctx := context.Background()
	repo, _ := newTestRepo(t)
	s := createSystem(t, repo, "rollback", "system")
	user := sharedkernel.Message{Role: "user", Content: "raw"}
	candidate, _ := s.WithAppendedMessage(&user)
	original := user.Clone()
	user.MemoryChunks = []sharedkernel.MemoryChunk{{ID: "1", Content: "recalled"}}
	user.RAGChunks = []sharedkernel.MemoryChunk{{ID: "2", Content: "retrieved"}}
	candidate.Messages[len(candidate.Messages)-1] = user
	rev, err := repo.CommitCreateMessage(ctx, s.ID, candidate.Snapshot(), original, user)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Revision = rev
	*s = *candidate
	restored, err := repo.GetRequestContext(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Messages[1].Content != "raw" || len(restored.Messages[1].MemoryChunks) != 1 ||
		len(restored.Messages[1].RAGChunks) != 1 || restored.Messages[1].RAGChunks[0].Content != "retrieved" {
		t.Fatal("memory not restored")
	}
	page, _, _ := repo.ListOriginalHistory(ctx, s.ID, 0, 10)
	if page.Messages[0].Content != "raw" {
		t.Fatal("original changed")
	}
	bad := sharedkernel.Message{Role: "assistant", Content: "truncated", FinishReason: "max_output_tokens", ReactTurn: 1}
	candidate, _ = s.WithAppendedMessage(&bad)
	candidate.ReactTurnCount = 1
	if _, err := repo.CommitCreateMessage(ctx, s.ID, candidate.Snapshot(), bad, bad); err == nil {
		t.Fatal("invalid turn accepted")
	}
	restored, _ = repo.GetRequestContext(ctx, s.ID)
	if restored.ReactTurnCount != 0 || restored.LastSeq != s.LastSeq {
		t.Fatal("partial commit")
	}
	appendMessage(t, repo, s, &sharedkernel.Message{Role: "user", Content: "next"})
	restored, _ = repo.GetRequestContext(ctx, s.ID)
	if len(restored.Messages[1].MemoryChunks) != 0 || len(restored.Messages[1].RAGChunks) != 0 {
		t.Fatal("old chunks retained")
	}
}
