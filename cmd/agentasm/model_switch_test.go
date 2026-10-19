package agentasm

import (
	"context"
	"testing"

	"github.com/mikellxy/laxcode/internal/infrastructure/ai_models"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
)

func switchTestModels(t *testing.T) *ai_models.Manager {
	t.Helper()
	providers := []config.Provider{
		{ProviderName: "first", OpenaiApiKey: "key-1", OpenaiBaseUrl: "https://first.example/v1", ModelList: []config.Model{{ModelName: "model-1", Limit: &config.ModelLimit{Context: 128_000, Output: 16_384}}}},
		{ProviderName: "second", OpenaiApiKey: "key-2", OpenaiBaseUrl: "https://second.example/v1", ModelList: []config.Model{{ModelName: "model-2", Limit: &config.ModelLimit{Context: 1_048_576, Output: 131_072}}}},
		{ProviderName: "summary", OpenaiApiKey: "summary-key", OpenaiBaseUrl: "https://summary.example/v1", ModelList: []config.Model{{ModelName: "summary-model", Limit: &config.ModelLimit{Context: 128_000, Output: 4096}}}},
	}
	models, err := ai_models.New(t.TempDir(), config.Conf{ProviderList: providers, Model: "first:model-1", CompactionModel: "summary:summary-model"})
	if err != nil {
		t.Fatal(err)
	}
	return models
}

func TestModelSwitchAppliesToNextAssembly(t *testing.T) {
	models := switchTestModels(t)

	assembled, err := assembleTest(t, context.Background(), Input{Models: models, Mode: ModeCode, WorkDir: t.TempDir(), HomeDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer assembled.Cleanup()
	previousClient := assembled.Service.LLMClient
	if budget := previousClient.ContextBudget(); budget.ContextWindow != 128_000 || budget.ReservedOutputTokens != 16_384 {
		t.Fatalf("initial budget: %+v", budget)
	}
	if err := models.SetActiveModel("second:model-2"); err != nil {
		t.Fatal(err)
	}
	if assembled.Service.LLMClient != previousClient || previousClient.ContextBudget().ContextWindow != 128_000 {
		t.Fatal("existing request provider or limits changed")
	}
	next, err := assembleTest(t, context.Background(), Input{Models: models, Mode: ModeCode, WorkDir: t.TempDir(), HomeDir: t.TempDir()})
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

func TestModelSelectionDoesNotChangeActiveTask(t *testing.T) {
	models := switchTestModels(t)
	task, err := models.ForTask()
	if err != nil {
		t.Fatal(err)
	}
	before := task.Active()
	if err := models.SetActiveModel("second:model-2"); err != nil {
		t.Fatal(err)
	}
	next, err := models.ForTask()
	if err != nil {
		t.Fatal(err)
	}
	if task.Active().Ref != before.Ref || next.Active().Ref != "second:model-2" {
		t.Fatal("task model snapshot changed")
	}
}

func TestFailedModelSwitchKeepsSelection(t *testing.T) {
	models := switchTestModels(t)

	before := models.Active()
	for _, ref := range []string{"missing:model", "bad-reference"} {
		if err := models.SetActiveModel(ref); err == nil {
			t.Fatal("invalid switch succeeded")
		}
	}
	if models.Active() != before {
		t.Fatal("failed switch changed runtime state")
	}
}
