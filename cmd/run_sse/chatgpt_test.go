package run_sse

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/infrastructure/ai_models"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
)

func TestOAuthModelListAndEffortSwitch(t *testing.T) {
	previous := config.EnvAndFileConf
	t.Cleanup(func() { config.EnvAndFileConf = previous })
	config.EnvAndFileConf.Model = ""
	config.EnvAndFileConf.ProviderList = nil
	config.EnvAndFileConf.CompactionModel = ""
	home := t.TempDir()
	s := newTestServer(t, home, false)
	if err := s.models.SaveChatGPTModels([]ai_models.Model{{Slug: "gpt-6.1-sol", DisplayName: "GPT-6.1 Sol"}}); err != nil {
		t.Fatal(err)
	}
	router := &recordingModelRouter{}
	s.switcher = agentasm.NewModelSwitcher(router, s.models)
	list := httptest.NewRecorder()
	s.handleListModels(list, httptest.NewRequest("GET", "/api/models", nil))
	var catalog providerListModelDTO
	if err := json.Unmarshal(list.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.CurrentModel != "openai-chatgpt:gpt-6.1-sol" || catalog.CurrentReasoningEffort != "medium" || catalog.Providers[0].AuthType != "oauth" || len(catalog.Providers[0].ModelList[0].ReasoningEfforts) != 5 {
		t.Fatalf("catalog=%s", list.Body)
	}
	for _, secret := range []string{"access_token", "refresh_token", "credential_ref"} {
		if strings.Contains(list.Body.String(), secret) {
			t.Fatal("OAuth credential metadata leaked")
		}
	}
	for _, effort := range []string{"high", "ultra", ""} {
		response := httptest.NewRecorder()
		s.handleSwitchModel(response, httptest.NewRequest("POST", "/api/model", strings.NewReader(`{"provider":"openai-chatgpt","model":"gpt-6.1-sol","reasoning_effort":"`+effort+`"}`)))
		if effort == "ultra" {
			if response.Code != 400 || s.models.Active().ReasoningEffort != "high" {
				t.Fatal("invalid effort changed active config")
			}
			continue
		}
		if response.Code != 200 || s.models.Active().ReasoningEffort != effort || s.models.Compaction().ReasoningEffort != effort {
			t.Fatalf("effort %q status=%d body=%s", effort, response.Code, response.Body)
		}
	}
}

func TestOAuthRejectsCrossSiteLogin(t *testing.T) {
	s := newTestServer(t, t.TempDir(), false)
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8090/api/auth/chatgpt", nil)
	request.Header.Set("Origin", "https://evil.example")
	response := httptest.NewRecorder()
	s.handleChatGPTLogin(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatal("cross-site login accepted")
	}
}
