package memorypipeline

import "testing"

func TestEnvironmentReadyRequiresEveryNonblankVariable(t *testing.T) {
	keys := []string{"OPENAI_EMBEDDING_MODEL_NAME", "OPENAI_EMBEDDING_BASE_URL", "OPENAI_EMBEDDING_API_KEY"}
	for _, key := range keys {
		t.Setenv(key, "configured")
	}
	if !EnvironmentReady() {
		t.Fatal("complete configuration disabled")
	}
	for _, key := range keys {
		for _, missing := range []string{"", " \t\n"} {
			t.Setenv(key, missing)
			if EnvironmentReady() {
				t.Fatalf("missing %s accepted", key)
			}
			t.Setenv(key, "configured")
		}
	}
}
