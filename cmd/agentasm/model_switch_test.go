package agentasm

import (
	"context"
	"testing"

	domainrouter "github.com/mikellxy/laxcode/internal/domain/llmrouter"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
)

type recordingRouter struct {
	clients []domainrouter.StreamClient
}

func (r *recordingRouter) ReplaceClient(client domainrouter.StreamClient) {
	r.clients = append(r.clients, client)
}

func TestAssembledSwitchModelReplacesRouterAndProvider(t *testing.T) {
	previous := config.EnvAndFileConf
	t.Cleanup(func() { config.EnvAndFileConf = previous })
	config.EnvAndFileConf.ProviderList = []config.ProviderConfig{
		{
			ProviderName: "first", OpenaiApiKey: "key-1", OpenaiBaseUrl: "https://first.example/v1",
			ModelList: []config.ModelConfig{{ModelName: "model-1"}},
		},
		{
			ProviderName: "second", OpenaiApiKey: "key-2", OpenaiBaseUrl: "https://second.example/v1",
			ModelList: []config.ModelConfig{{ModelName: "model-2"}},
		},
	}
	if err := config.SetActiveModel("first:model-1"); err != nil {
		t.Fatal(err)
	}
	config.EnvAndFileConf.OpenaiContextWindow = 128_000
	config.EnvAndFileConf.OpenaiMaxOutputTokens = 16_384
	config.EnvAndFileConf.CompactionOpenaiApiKey = "summary-key"
	config.EnvAndFileConf.CompactionOpenaiBaseUrl = "https://summary.example/v1"
	config.EnvAndFileConf.CompactionOpenaiModel = "summary-model"
	config.EnvAndFileConf.CompactionOpenaiContextWindow = 128_000
	config.EnvAndFileConf.CompactionOpenaiMaxOutputTokens = 4_096
	config.EnvAndFileConf.LlmRouterURL = "http://127.0.0.1:1/openai/generate_stream"

	router := &recordingRouter{}
	assembled, err := Assemble(context.Background(), Input{WorkDir: t.TempDir(), Router: router})
	if err != nil {
		t.Fatal(err)
	}
	defer assembled.Cleanup()
	previousClient := assembled.Service.LLMClient

	if err := assembled.SwitchModel("second:model-2"); err != nil {
		t.Fatal(err)
	}
	if len(router.clients) != 1 {
		t.Fatalf("router replacements=%d，期望 1", len(router.clients))
	}
	if assembled.Service.LLMClient == previousClient {
		t.Fatal("主 ReAct provider 未替换")
	}
	if config.EnvAndFileConf.Model != "second:model-2" ||
		config.EnvAndFileConf.OpenaiApiKey != "key-2" ||
		config.EnvAndFileConf.OpenaiModel != "model-2" {
		t.Fatalf("运行时配置未切换：%+v", config.EnvAndFileConf)
	}
}
