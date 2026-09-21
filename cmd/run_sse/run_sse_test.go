package run_sse

import (
	"path/filepath"
	"testing"

	"github.com/mikellxy/laxcode/internal/infrastructure/config"
)

func TestCheckConfigCombinedSSEQADoesNotRequireVectorDimensions(t *testing.T) {
	savedCLI, savedConfig := config.CliConf, config.EnvAndFileConf
	t.Cleanup(func() {
		config.CliConf = savedCLI
		config.EnvAndFileConf = savedConfig
	})

	config.CliConf.SSE = true
	config.CliConf.QA = true
	config.CliConf.KB = filepath.Join(t.TempDir(), "kb.sqlite")
	config.CliConf.VectorDimensions = 0
	config.EnvAndFileConf.OpenaiApiKey = "chat-key"
	config.EnvAndFileConf.OpenaiBaseUrl = "https://chat.example/v1"
	config.EnvAndFileConf.OpenaiModel = "chat-model"
	config.EnvAndFileConf.EmbedOpenaiApiKey = "embedding-key"
	config.EnvAndFileConf.EmbedOpenaiBaseUrl = "https://embedding.example/v1"
	config.EnvAndFileConf.EmbedOpenaiModel = "embedding-model"

	if err := checkConfig(); err != nil {
		t.Fatalf("combined SSE QA configuration should be valid without -vector-dim: %v", err)
	}
}
