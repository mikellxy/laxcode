package run_sse

import (
	"path/filepath"
	"testing"

	"github.com/mikellxy/laxcode/internal/infrastructure/config"
)

func TestCheckConfigSSERAG(t *testing.T) {
	savedCLI, savedConfig := config.CliConf, config.EnvAndFileConf
	t.Cleanup(func() {
		config.CliConf = savedCLI
		config.EnvAndFileConf = savedConfig
	})

	config.CliConf.SSE = true
	config.CliConf.Mode = config.SSEModeRAG
	config.CliConf.KB = filepath.Join(t.TempDir(), "kb.sqlite")
	config.EnvAndFileConf.OpenaiApiKey = "chat-key"
	config.EnvAndFileConf.OpenaiBaseUrl = "https://chat.example/v1"
	config.EnvAndFileConf.OpenaiModel = "chat-model"
	config.EnvAndFileConf.EmbedOpenaiApiKey = "embedding-key"
	config.EnvAndFileConf.EmbedOpenaiBaseUrl = "https://embedding.example/v1"
	config.EnvAndFileConf.EmbedOpenaiModel = "embedding-model"

	if err := checkConfig(); err != nil {
		t.Fatalf("SSE RAG configuration should be valid: %v", err)
	}
}
