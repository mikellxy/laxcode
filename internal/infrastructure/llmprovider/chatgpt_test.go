package llmprovider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/infrastructure/chatgpt"
)

func TestChatGPTSummaryAndToolContinuation(t *testing.T) {
	home := t.TempDir()
	if err := chatgpt.NewStore(home).Save(context.Background(), chatgpt.CredentialRef, chatgpt.Credential{ClientID: "issued", Subject: "user", AccessToken: "oauth-access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{chatgpt.DirectScope}}); err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	requests := []map[string]any{}
	http.DefaultTransport = providerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != chatgpt.BaseURL+"responses" || r.Header.Get("Authorization") != "Bearer oauth-access" {
			t.Fatalf("wrong OAuth destination or auth")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, body)
		if body["stream"] != true || body["store"] != false || body["max_output_tokens"] != nil || body["reasoning"].(map[string]any)["effort"] != "high" {
			t.Fatalf("OAuth body=%+v", body)
		}
		item := map[string]any{"type": "reasoning", "id": "reasoning-1", "summary": []any{}, "encrypted_content": "opaque-state"}
		tool := map[string]any{"type": "function_call", "call_id": "call-1", "name": "read_file", "namespace": "laxcode", "arguments": "{}"}
		items := []map[string]any{item, tool}
		var sse strings.Builder
		for _, i := range items {
			event, _ := json.Marshal(map[string]any{"type": "response.output_item.done", "item": i})
			sse.WriteString("data: " + string(event) + "\n\n")
		}
		sse.WriteString("data: {\"type\":\"response.output_text.delta\",\"delta\":\"summary\"}\n\n")
		sse.WriteString("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n\n")
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse.String())), Request: r}, nil
	})
	provider := NewOpenApiProvider("", "", "gpt-6.1-sol").WithChatGPT(home, chatgpt.CredentialRef).WithReasoningEffort("high")
	tools := []sharedkernel.ToolDefinition{{Name: "read_file", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}}
	messages := []sharedkernel.Message{{Role: sharedkernel.RoleSystem, Content: "rules"}, {Role: sharedkernel.RoleUser, Content: "question"}}
	message, err := provider.Generate(context.Background(), messages, tools)
	if err != nil || message.Content != "summary" || message.ReasoningEncryptedContent != "opaque-state" || len(message.ToolCalls) != 1 || message.ToolCalls[0].Name != "read_file" {
		t.Fatalf("summary result=%+v err=%v", message, err)
	}
	messages = append(messages, *message, sharedkernel.Message{Role: sharedkernel.RoleTool, ToolCallID: "call-1", Content: "file"})
	if _, err := provider.GenerateStream(context.Background(), messages, tools, func(sharedkernel.StreamChunk) {}); err != nil {
		t.Fatal(err)
	}
	input := requests[1]["input"].([]any)
	found := false
	for _, raw := range input {
		item := raw.(map[string]any)
		if item["type"] == "reasoning" && item["encrypted_content"] == "opaque-state" {
			found = true
		}
	}
	if !found {
		t.Fatal("tool continuation did not replay encrypted reasoning")
	}
	if count, err := provider.CountInputTokens(context.Background(), messages, tools); err != nil || count <= 0 {
		t.Fatalf("local token count=%d err=%v", count, err)
	}
	if len(requests) != 2 {
		t.Fatal("OAuth token counting made a remote request")
	}
}

func TestChatGPTReportsUpstreamErrorsAndTruncatedStreams(t *testing.T) {
	home := t.TempDir()
	if err := chatgpt.NewStore(home).Save(context.Background(), chatgpt.CredentialRef, chatgpt.Credential{ClientID: "issued", Subject: "user", AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{chatgpt.DirectScope}}); err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	for _, test := range []struct {
		status                  int
		body, contentType, want string
	}{
		{403, `{"error":{"code":"subscription_sharing_user_not_eligible","message":"plan unavailable","type":"permission_error"}}`, "application/json", "subscription_sharing_user_not_eligible"},
		{200, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n", "text/event-stream", "without a terminal"},
	} {
		http.DefaultTransport = providerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: test.status, Header: http.Header{"Content-Type": {test.contentType}}, Body: io.NopCloser(strings.NewReader(test.body)), Request: r}, nil
		})
		provider := NewOpenApiProvider("", "", "gpt-6.1-sol").WithChatGPT(home, chatgpt.CredentialRef)
		_, err := provider.Generate(context.Background(), []sharedkernel.Message{{Role: sharedkernel.RoleUser, Content: "test"}}, nil)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("error=%v want=%s", err, test.want)
		}
	}
}
