package reactservice

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func TestContextSummaryReceivesToolCompactContentAndOriginalArtifact(t *testing.T) {
	llm := &scriptedLLM{responses: []scriptedResp{{msg: assistantMsg(`{"objective":"continue","warnings":["execution timed out"]}`)}}}
	svc := &ReActService{ContextSummaryLLMClient: llm}
	ref := &sharedkernel.ArtifactRef{ID: "full-log", ByteSize: 20000}
	source := []sharedkernel.Message{{Seq: 3, OriginalSeq: []uint64{3}, Role: sharedkernel.RoleTool, ToolCallID: "c1",
		Content: "partial log preview", CompactContent: "status:timed_out\nexit_code:unknown\nstdout_truncated:true", Artifact: ref}}
	if _, _, err := svc.generateContextSummary(context.Background(), source, "", 500); err != nil {
		t.Fatal(err)
	}
	var req contextSummaryRequest
	if err := json.Unmarshal([]byte(llm.lastMsgs[1].Content), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 1 || req.Messages[0].CompactContent != source[0].CompactContent || req.Messages[0].Artifact == nil || *req.Messages[0].Artifact != *ref {
		t.Fatalf("summary source metadata lost: %+v", req.Messages)
	}
}
