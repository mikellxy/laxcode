package memorypipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mikellxy/laxcode/internal/application/usermemory"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/infrastructure/knowledgebase"
)

func TestCLIRealPythonWithFakeEmbedding(t *testing.T) {
	python := os.Getenv("LAX_MEMORY_TEST_PYTHON")
	if python == "" {
		t.Skip("set LAX_MEMORY_TEST_PYTHON")
	}
	for _, key := range []string{"ALL_PROXY", "all_proxy", "HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"} {
		t.Setenv(key, "")
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("wrong embedding credentials")
		}
		var req struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if req.Model != "test-model" {
			t.Error("wrong model")
		}
		calls.Add(1)
		data := []map[string]any{}
		for i := range req.Input {
			v := make([]float32, 1024)
			v[0] = 1
			data = append(data, map[string]any{"index": i, "embedding": v, "object": "embedding"})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "model": "test-model", "usage": map[string]int{"prompt_tokens": 1, "total_tokens": 1}})
	}))
	defer server.Close()
	db := filepath.Join(t.TempDir(), "kb.sqlite")
	executable := filepath.Join(t.TempDir(), "laxcode-knowledge")
	script := "#!/bin/sh\nexec \"$LAX_PIPELINE_TEST_PYTHON\" -c 'from laxcode_knowledge.cmd import main; main()' \"$@\"\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	sourceRoot, err := filepath.Abs("../../../knowledge-pipeline/src")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAX_PIPELINE_TEST_PYTHON", python)
	t.Setenv("PYTHONPATH", sourceRoot)
	cli := &CLI{Executable: executable, DB: db, Model: "test-model", APIKey: "test-key", BaseURL: server.URL + "/v1", Dimensions: 1024}
	if err := cli.Validate(); err != nil {
		t.Fatal(err)
	}
	job := &session.MemoryJob{UserID: "11111111-1111-4111-8111-111111111111", SessionID: "cli", SourceKey: "cli:react:3", StartTurn: 1, EndTurn: 3}
	ctx := context.Background()
	if err := cli.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cli.Ingest(ctx, job, "# 偏好\n用户喜欢中文"); err != nil {
		t.Fatal(err)
	}
	if err := cli.Ingest(ctx, job, "# 偏好\n用户喜欢中文"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("idempotent retry called embedding %d times", calls.Load())
	}
	if err := cli.Ingest(ctx, job, "different"); !errors.Is(err, usermemory.ErrPermanent) {
		t.Fatalf("conflict error=%v", err)
	}
	r, err := knowledgebase.NewUserMemoryRetriever(db, "test-model", 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	chunks, err := r.SearchUser(ctx, job.UserID, make([]float32, 1024))
	if err != nil || len(chunks) != 1 {
		t.Fatalf("chunks=%v err=%v", chunks, err)
	}
}
