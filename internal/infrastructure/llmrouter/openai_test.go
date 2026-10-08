package llmrouter

import (
	"context"
	"encoding/json"
	"github.com/mikellxy/laxcode/internal/infrastructure/chatgpt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3/option"
)

func TestOpenAIStreamClientUsesConfiguredUpstreamAndModel(t *testing.T) {
	received := make(chan map[string]any, 1)
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path = %q, want /v1/responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-secret" {
			t.Errorf("Authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		received <- body

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader("event: response.output_text.delta\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\",\"sequence_number\":1,\"item_id\":\"item-1\",\"output_index\":0,\"content_index\":0}\n\n")),
			Request: r,
		}, nil
	})
	httpClient := &http.Client{Transport: transport}

	client := newOpenAIStreamClient("upstream-secret", "https://upstream.example/v1", "configured-model",
		option.WithHTTPClient(httpClient))
	stream, err := client.GenerateStream(context.Background(), []byte(
		`{"model":"caller-model","input":[{"role":"user","content":"hi"}],"custom_provider":{"cache":true}}`))
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	defer stream.Close()

	if !stream.Next() {
		t.Fatalf("stream.Next=false: %v", stream.Err())
	}
	event := stream.Current()
	if event.Type != "response.output_text.delta" {
		t.Fatalf("event type = %q", event.Type)
	}
	if string(event.Data) != `{"type":"response.output_text.delta","delta":"hello","sequence_number":1,"item_id":"item-1","output_index":0,"content_index":0}` {
		t.Fatalf("event JSON was not preserved: %s", event.Data)
	}

	body := <-received
	if body["model"] != "configured-model" {
		t.Fatalf("model = %v, want configured-model", body["model"])
	}
	if body["stream"] != true {
		t.Fatalf("stream = %v, want true", body["stream"])
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("input was lost during proxying: %v", body)
	}
	custom, ok := body["custom_provider"].(map[string]any)
	if !ok || custom["cache"] != true {
		t.Fatalf("custom provider body was not preserved: %v", body)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestOpenAIStreamClientRejectsInvalidRequest(t *testing.T) {
	client := NewOpenAIStreamClient("key", "http://127.0.0.1:1", "model")
	if _, err := client.GenerateStream(context.Background(), []byte(`{"input":`)); err == nil {
		t.Fatal("invalid request must fail before calling upstream")
	}
}

func TestChatGPTRouterRefreshesAndUsesConfiguredModelAndEffort(t *testing.T) {
	home := t.TempDir()
	if err := chatgpt.NewStore(home).Save(context.Background(), chatgpt.CredentialRef, chatgpt.Credential{ClientID: "issued", Subject: "user", AccessToken: "expired", RefreshToken: "refresh", ExpiresAt: time.Now(), Scopes: []string{chatgpt.DirectScope}}); err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	refreshes, calls := 0, 0
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "auth.openai.com" {
			refreshes++
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"access_token":"fresh","refresh_token":"rotated","expires_in":3600,"scope":"chatgpt.tokens.use.direct","token_type":"Bearer"}`)), Request: r}, nil
		}
		calls++
		if r.URL.String() != chatgpt.BaseURL+"responses" || r.Header.Get("Authorization") != "Bearer fresh" {
			t.Fatal("OAuth router used stale credentials or wrong endpoint")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "gpt-6.1-sol" || body["reasoning"].(map[string]any)["effort"] != "high" || body["max_output_tokens"] != nil || body["store"] != false {
			t.Fatalf("OAuth request=%+v", body)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")), Request: r}, nil
	})
	client := NewChatGPTStreamClient(home, chatgpt.CredentialRef, "gpt-6.1-sol").WithReasoningEffort("high")
	for range 2 {
		stream, err := client.GenerateStream(context.Background(), []byte(`{"model":"caller-model","reasoning":{"effort":"low"},"input":[],"max_output_tokens":100}`))
		if err != nil {
			t.Fatal(err)
		}
		if !stream.Next() || stream.Current().Type != "response.output_text.delta" {
			t.Fatalf("stream=%v", stream.Err())
		}
		stream.Close()
	}
	if refreshes != 1 || calls != 2 {
		t.Fatalf("refreshes=%d calls=%d", refreshes, calls)
	}
}

func TestOpenAIStreamPreservesUpstreamHTTPError(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"error":{"message":"slow down","type":"rate_limit_error","code":"rate_limit","param":null}}`)),
			Request: r,
		}, nil
	})
	client := newOpenAIStreamClient("key", "https://upstream.example/v1", "model",
		option.WithHTTPClient(&http.Client{Transport: transport}))
	stream, err := client.GenerateStream(context.Background(), []byte(`{"input":"hi"}`))
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	defer stream.Close()

	if stream.Next() {
		t.Fatal("error response must not produce a stream event")
	}
	type httpError interface {
		HTTPStatusCode() int
		ResponseBody() []byte
	}
	got, ok := stream.Err().(httpError)
	if !ok {
		t.Fatalf("error does not expose upstream HTTP details: %T", stream.Err())
	}
	if got.HTTPStatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %d", got.HTTPStatusCode())
	}
	if string(got.ResponseBody()) != `{"error":{"message":"slow down","type":"rate_limit_error","code":"rate_limit","param":null}}` {
		t.Fatalf("body = %s", got.ResponseBody())
	}
}
