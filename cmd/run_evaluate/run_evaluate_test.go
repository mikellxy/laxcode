package run_evaluate

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestResolveHistoryPath(t *testing.T) {
	workDir := t.TempDir()
	got, err := resolveHistoryPath(workDir, "88a74c78-a5c4-4602-bb1e-8e4a4ce0256b")
	if err != nil {
		t.Fatalf("resolveHistoryPath: %v", err)
	}
	want := filepath.Join(workDir, ".laxcode", ".session", "88a74c78-a5c4-4602-bb1e-8e4a4ce0256b", "history.jsonl")
	if got != want {
		t.Fatalf("history path = %q, want %q", got, want)
	}
}

func TestHistoryPathCanBePresentedToReadFileAsRelative(t *testing.T) {
	workDir := t.TempDir()
	historyPath, err := resolveHistoryPath(workDir, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(workDir, historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(".laxcode", ".session", "sess-1", "history.jsonl"); rel != want {
		t.Fatalf("relative history path = %q, want %q", rel, want)
	}
}

func TestResolveHistoryPathRejectsInvalidSessionIDs(t *testing.T) {
	for _, id := range []string{"", ".", "..", "../escape", "nested/session", `nested\session`, "bad\x00id"} {
		t.Run(id, func(t *testing.T) {
			if _, err := resolveHistoryPath(t.TempDir(), id); err == nil {
				t.Fatalf("session ID %q should fail", id)
			}
		})
	}
}

func TestWriteResult(t *testing.T) {
	var out bytes.Buffer
	writeResult(&out, EvaluationResult{SessionID: "judge", EvalSessionID: "target", Result: "report"})
	var got EvaluationResult
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &got); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if got.SessionID != "judge" || got.EvalSessionID != "target" || got.Result != "report" || got.Error != nil {
		t.Fatalf("unexpected result: %+v", got)
	}
}
