package memorypipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mikellxy/laxcode/internal/application/usermemory"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
)

// EnvironmentReady checks the embedding variables required by knowledge-pipeline.
// Blank values count as missing; never log credentials.
func EnvironmentReady() bool {
	return config.EmbeddingEnvironmentReady()
}

type CLI struct {
	Executable, DB, Model, BaseURL, APIKey string
	Dimensions                             int
}

func (c *CLI) Validate() error {
	if !filepath.IsAbs(c.Executable) || !filepath.IsAbs(c.DB) {
		return fmt.Errorf("memory executable and database must use absolute paths")
	}
	if c.Model == "" || c.BaseURL == "" || c.APIKey == "" {
		return fmt.Errorf("memory embedding configuration required")
	}
	if c.Dimensions <= 0 || c.Dimensions > 8192 {
		return fmt.Errorf("memory vector dimensions must be between 1 and 8192")
	}
	info, err := os.Stat(c.Executable)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Mode()&0111 == 0 {
		return fmt.Errorf("memory executable is not executable")
	}
	return nil
}

func (c *CLI) InitSchema(ctx context.Context) error {
	return c.run(ctx, nil, "--target", "user_memory", "--db", c.DB,
		"--dimensions", fmt.Sprint(c.Dimensions), "--init-schema")
}

// limitedBuffer drains output without retaining unbounded subprocess logs.
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < 65536 {
		b.Buffer.Write(p[:min(n, 65536-b.Len())])
	}
	return n, nil
}
func (c *CLI) Ingest(ctx context.Context, j *session.MemoryJob, summary string) error {
	data, err := json.Marshal(struct {
		UserID    string `json:"user_id"`
		SessionID string `json:"session_id"`
		SourceKey string `json:"source_key"`
		StartTurn uint64 `json:"start_turn"`
		EndTurn   uint64 `json:"end_turn"`
		Content   string `json:"content"`
	}{j.UserID, j.SessionID, j.SourceKey, j.StartTurn, j.EndTurn, summary})
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return fmt.Errorf("%w: memory input exceeds 1 MiB", usermemory.ErrPermanent)
	}
	return c.run(ctx, data, "--target", "user_memory", "--db", c.DB,
		"--dimensions", fmt.Sprint(c.Dimensions), "--stdin-json")
}

func (c *CLI) run(ctx context.Context, input []byte, args ...string) error {
	cmd := exec.CommandContext(ctx, c.Executable, args...)
	cmd.WaitDelay = 5 * time.Second
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	// Use precisely the same embedding provider as Go, overriding inherited values.
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "OPENAI_EMBEDDING_MODEL_NAME=") && !strings.HasPrefix(entry, "OPENAI_EMBEDDING_BASE_URL=") && !strings.HasPrefix(entry, "OPENAI_EMBEDDING_API_KEY=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "PYTHONDONTWRITEBYTECODE=1", "OPENAI_EMBEDDING_MODEL_NAME="+c.Model, "OPENAI_EMBEDDING_BASE_URL="+c.BaseURL, "OPENAI_EMBEDDING_API_KEY="+c.APIKey)
	var stdout, stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 2 {
			return fmt.Errorf("%w: %s", usermemory.ErrPermanent, stderr.String())
		}
		return fmt.Errorf("memory pipeline: %w: %s", err, stderr.String())
	}
	var result struct {
		Status     string `json:"status"`
		MemoryID   string `json:"memory_id"`
		ChunkCount int    `json:"chunk_count"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return fmt.Errorf("memory pipeline response: %w", err)
	}
	if result.Status == "initialized" {
		return nil
	}
	if result.Status != "succeeded" || result.MemoryID == "" || result.ChunkCount < 1 {
		return fmt.Errorf("invalid memory pipeline response")
	}
	return nil
}
