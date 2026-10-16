package ai_models

import (
	"fmt"
	"slices"
)

// Only advertise efforts whose model contract is known. Unknown models retain
// the upstream default instead of receiving guessed reasoning parameters.
func ReasoningEfforts(model string) []string {
	if model == "gpt-6.1-sol" {
		return []string{"low", "medium", "high", "xhigh", "max"}
	}
	return nil
}

func ValidateEffort(model, effort string) error {
	if effort != "" && !slices.Contains(ReasoningEfforts(model), effort) {
		return fmt.Errorf("unsupported reasoning effort %q for %s", effort, model)
	}
	return nil
}
