package sessionrepo

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func TestTaskProjectUniquenessAndHistorySnapshot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo, err := NewSqliteSessionRepo(filepath.Join(root, "sessions.db"), filepath.Join(root, "history"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.CreateProject(ctx, "p", "u", "demo", root); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if _, err := repo.CreateSession(ctx, id, "u", "p", "", root, "code"); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := repo.CreateTask(ctx, "task-"+id, id); results <- err }()
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrTaskConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
	active, err := repo.GetActiveProjectTask(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteSession(ctx, active.SessionID, "u"); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("deleted active session: %v", err)
	}
	if err := repo.StartTask(ctx, active.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishTask(ctx, active.ID, "fail", "TEST", "interrupted", 7); err != nil {
		t.Fatal(err)
	}
	page, _, err := repo.ListOriginalHistory(ctx, active.SessionID, 0, 50)
	if err != nil || page.LatestTask == nil || page.LatestTask.Status != "fail" || page.LatestTask.FinalEventID != 7 {
		t.Fatalf("%+v %v", page, err)
	}
	next, err := repo.CreateTask(ctx, "next", active.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if next.Order <= active.Order {
		t.Fatal("task ordering not monotonic")
	}
	old, _ := repo.GetTask(ctx, active.ID)
	if old.Status != "fail" {
		t.Fatal("old task was reused")
	}
}
func TestReservedMessageSequenceSurvivesReopenAndGap(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sessions.db")
	ctx := context.Background()
	repo, err := NewSqliteSessionRepo(path, root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.CreateProject(ctx, "p", "u", "demo", root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.CreateSession(ctx, "s", "u", "p", "", root, "code")
	if err != nil {
		t.Fatal(err)
	}
	initial := session.NewSession("s", root)
	initial.Mode = "code"
	sys := initial.UpsertSysMessage("system")
	if _, err := repo.CommitCreateMessage(ctx, "s", initial.Snapshot(), sys, sys); err != nil {
		t.Fatal(err)
	}
	// Reserve without committing: a failed LLM can leave this sequence in MQ.
	abandoned, err := repo.ReserveMessageSequence(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	repo.Close()
	repo, err = NewSqliteSessionRepo(path, root)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	seq, err := repo.ReserveMessageSequence(ctx, "s")
	if err != nil || seq <= abandoned {
		t.Fatalf("%d %d %v", seq, abandoned, err)
	}
	state, err := repo.GetRequestContext(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	sess := session.NewSession("s", root)
	sess.RequestContext = state
	msg := sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "after interruption", Seq: seq}
	candidate, err := sess.WithAppendedMessage(&msg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitCreateMessage(ctx, "s", candidate.Snapshot(), msg, msg); err != nil {
		t.Fatal(err)
	}
}
