package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type streamError struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	RetryAction string `json:"retry_action,omitempty"`
}

// prepare commits the response; upstream headers and status must stay inside the proxy.
type preparedStream struct {
	http.ResponseWriter
	header http.Header
}

func (s *preparedStream) Header() http.Header         { return s.header }
func (s *preparedStream) WriteHeader(int)             {}
func (s *preparedStream) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *preparedStream) send(event string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s, "event: %s\ndata: %s\n\n", event, payload); err != nil {
		return err
	}
	return http.NewResponseController(s).Flush()
}

func (s *preparedStream) adaptResponse(response *http.Response) error {
	if response.StatusCode == http.StatusOK && strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		return nil
	}
	payload := streamError{Code: "UPSTREAM_ERROR", Message: fmt.Sprintf("sandbox request failed (HTTP %d)", response.StatusCode)}
	if response.StatusCode == http.StatusOK {
		payload.Message = "sandbox did not return an SSE stream"
	}
	var upstream streamError
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&upstream); err == nil {
		if upstream.Code != "" {
			payload.Code = upstream.Code
		}
		if upstream.Message != "" {
			payload.Message = upstream.Message
		}
		payload.RetryAction = upstream.RetryAction
	}
	_ = response.Body.Close()
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	response.Body = io.NopCloser(strings.NewReader("event: error\ndata: " + string(data) + "\n\n"))
	response.StatusCode = http.StatusOK
	response.ContentLength = -1
	response.Header.Del("Content-Length")
	response.Header.Del("Content-Encoding")
	response.Header.Set("Content-Type", "text/event-stream; charset=utf-8")
	return nil
}
