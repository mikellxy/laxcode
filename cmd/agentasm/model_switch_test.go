package agentasm

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	domainrouter "github.com/mikellxy/laxcode/internal/domain/llmrouter"
	"github.com/mikellxy/laxcode/internal/infrastructure/ai_models"
)

type recordingRouter struct{ clients []domainrouter.StreamClient }

func (r *recordingRouter) ReplaceClient(client domainrouter.StreamClient) {
	r.clients = append(r.clients, client)
}

func switchTestModels(t *testing.T) *ai_models.Manager {
	t.Helper()
	raw, err := json.Marshal([]ai_models.ProviderConfig{
		{ProviderName: "first", OpenaiApiKey: "key-1", OpenaiBaseUrl: "https://first.example/v1", ModelList: []ai_models.ModelConfig{{ModelName: "model-1"}}},
		{ProviderName: "second", OpenaiApiKey: "key-2", OpenaiBaseUrl: "https://second.example/v1", ModelList: []ai_models.ModelConfig{{ModelName: "model-2", Limit: &ai_models.ModelLimit{Context: 1_048_576, Output: 131_072}}}},
		{ProviderName: "summary", OpenaiApiKey: "summary-key", OpenaiBaseUrl: "https://summary.example/v1", ModelList: []ai_models.ModelConfig{{ModelName: "summary-model", Limit: &ai_models.ModelLimit{Context: 128_000, Output: 4096}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	models, err := ai_models.New(t.TempDir(), raw, ai_models.Options{Model: "first:model-1", CompactionModel: "summary:summary-model", ContextWindow: 128_000, MaxOutputTokens: 16_384})
	if err != nil {
		t.Fatal(err)
	}
	return models
}

func TestModelSwitchAppliesToNextAssembly(t *testing.T) {
	models := switchTestModels(t)
	router := &recordingRouter{}
	assembled, err := Assemble(context.Background(), Input{Models: models, Mode: ModeCode, WorkDir: t.TempDir(), HomeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer assembled.Cleanup()
	previousClient := assembled.Service.LLMClient
	if budget := previousClient.ContextBudget(); budget.ContextWindow != 128_000 || budget.ReservedOutputTokens != 16_384 {
		t.Fatalf("initial budget: %+v", budget)
	}
	if err := NewModelSwitcher(router, models).SwitchModel("second:model-2"); err != nil {
		t.Fatal(err)
	}
	if len(router.clients) != 1 {
		t.Fatalf("router replacements=%d", len(router.clients))
	}
	if assembled.Service.LLMClient != previousClient || previousClient.ContextBudget().ContextWindow != 128_000 {
		t.Fatal("existing request provider or limits changed")
	}
	next, err := Assemble(context.Background(), Input{Models: models, Mode: ModeCode, WorkDir: t.TempDir(), HomeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Cleanup()
	if budget := next.Service.LLMClient.ContextBudget(); budget.ContextWindow != 1_048_576 || budget.ReservedOutputTokens != 131_072 {
		t.Fatalf("next budget: %+v", budget)
	}
	if budget := next.Service.ContextSummaryLLMClient.ContextBudget(); budget.ContextWindow != 128_000 || budget.ReservedOutputTokens != 4096 {
		t.Fatalf("explicit summary model changed: %+v", budget)
	}
	if model := models.Active(); model.Ref != "second:model-2" || model.OpenaiApiKey != "key-2" || model.UpstreamModel != "model-2" {
		t.Fatalf("active model: %+v", model)
	}
}

func TestModelSwitcherWaitsForActiveRequest(t *testing.T) {
	models := switchTestModels(t)
	router := &recordingRouter{}
	switcher := NewModelSwitcher(router, models)
	switcher.RLock()
	done := make(chan error, 1)
	go func() { done <- switcher.SwitchModel("second:model-2") }()
	select {
	case err := <-done:
		switcher.RUnlock()
		t.Fatalf("switch completed while request was active: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	switcher.RUnlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("switch did not complete after request ended")
	}
}

func TestFailedModelSwitchKeepsSelectionAndRouter(t *testing.T) {
	models := switchTestModels(t)
	router := &recordingRouter{}
	before := models.Active()
	for _, ref := range []string{"missing:model", "bad-reference"} {
		if err := NewModelSwitcher(router, models).SwitchModel(ref); err == nil {
			t.Fatal("invalid switch succeeded")
		}
	}
	if models.Active() != before || len(router.clients) != 0 {
		t.Fatal("failed switch changed runtime state")
	}
}
