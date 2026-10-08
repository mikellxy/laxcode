package chatgpt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
)

type Model struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Visibility  string `json:"visibility"`
}

func (s *Store) Models(ctx context.Context, ref string) ([]Model, error) {
	token, err := s.AccessToken(ctx, ref)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, BaseURL+"models", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, errors.New("ChatGPT model catalog unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ChatGPT model catalog failed (HTTP %d)", resp.StatusCode)
	}
	var catalog struct {
		Models []Model `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&catalog); err != nil {
		return nil, errors.New("invalid ChatGPT model catalog")
	}
	models := make([]Model, 0, len(catalog.Models))
	for _, model := range catalog.Models {
		if model.Visibility == "list" && model.Slug != "" {
			models = append(models, model)
		}
	}
	if len(models) == 0 {
		return nil, errors.New("no ChatGPT models available to this account")
	}
	return models, nil
}

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

func HTTPClient(homeDir, ref string) *http.Client {
	store := NewStore(homeDir)
	return &http.Client{Transport: &transport{store: store, ref: ref, base: store.http.Transport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type transport struct {
	store *Store
	ref   string
	base  http.RoundTripper
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != "api.openai.com" || req.URL.Path != "/v1/responses" {
		return nil, errors.New("ChatGPT credentials may only be sent to the public Responses API")
	}
	token, err := t.store.AccessToken(req.Context(), t.ref)
	if err != nil {
		return nil, err
	}
	request := req.Clone(req.Context())
	request.Header.Set("Authorization", "Bearer "+token)
	data, err := io.ReadAll(io.LimitReader(req.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	defer req.Body.Close()
	data, err = NormalizeRequest(data)
	if err != nil {
		return nil, err
	}
	request.Body = io.NopCloser(bytes.NewReader(data))
	request.ContentLength = int64(len(data))
	request.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	return t.base.RoundTrip(request)
}

// NormalizeRequest is shared by the router and direct summary requests.
func NormalizeRequest(data []byte) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil || body == nil {
		return nil, errors.New("invalid ChatGPT Responses request")
	}
	body["store"], body["stream"] = false, true
	for _, key := range strings.Fields("background conversation max_output_tokens max_tool_calls metadata moderation multi_agent prompt prompt_cache_retention prompt_cache_options safety_identifier temperature top_logprobs top_p truncation user previous_response_id") {
		delete(body, key)
	}
	input, ok := body["input"].([]any)
	if !ok {
		return nil, errors.New("ChatGPT Responses input must be an array")
	}
	filtered := make([]any, 0, len(input))
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("invalid ChatGPT input item")
		}
		if item["role"] == "system" {
			item["role"] = "developer"
		}
		if item["type"] == "reasoning" {
			// Visible summaries are not replayable reasoning. Retain only opaque
			// encrypted state; switching from an API-key model may leave none.
			if content, _ := item["encrypted_content"].(string); content == "" {
				continue
			}
			delete(item, "content")
			if item["summary"] == nil {
				item["summary"] = []any{}
			}
		}
		if item["type"] == "function_call" {
			item["namespace"] = "laxcode"
		}
		filtered = append(filtered, item)
	}
	body["input"] = filtered
	if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
		functions := []any{}
		other := []any{}
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				return nil, errors.New("invalid ChatGPT tool")
			}
			if tool["type"] == "function" || tool["type"] == "custom" {
				if tool["type"] == "function" {
					tool["strict"] = false
				}
				functions = append(functions, tool)
			} else {
				other = append(other, tool)
			}
		}
		if len(functions) > 0 {
			other = append(other, map[string]any{"type": "namespace", "name": "laxcode", "description": "LaxCode local tools", "tools": functions})
		}
		body["tools"] = other
	}
	include, _ := body["include"].([]any)
	found := false
	for _, item := range include {
		if item == "reasoning.encrypted_content" {
			found = true
		}
	}
	if !found {
		include = append(include, "reasoning.encrypted_content")
	}
	body["include"] = include
	return json.Marshal(body)
}
