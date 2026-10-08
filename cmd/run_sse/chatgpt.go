package run_sse

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/mikellxy/laxcode/internal/infrastructure/ai_models"
)

// Browser mutations must originate from this backend or the configured Vite
// dev proxy. Other origins cannot start or cancel a local OAuth attempt.
func localAuthRequest(w http.ResponseWriter, r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
		writeJSONError(w, 403, "cross-site OAuth request rejected")
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		allowed := err == nil && (parsed.Host == r.Host || ((parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1") && parsed.Port() == "5173"))
		if !allowed {
			writeJSONError(w, 403, "OAuth origin rejected")
			return false
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	return true
}

func (s *server) handleChatGPTLogin(w http.ResponseWriter, r *http.Request) {
	if !localAuthRequest(w, r) {
		return
	}
	status, err := s.oauth.Start(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.Context().Err() != nil {
		s.oauth.Close()
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

func (s *server) handleChatGPTStatus(w http.ResponseWriter, r *http.Request) {
	if !localAuthRequest(w, r) {
		return
	}
	status, ok := s.oauth.Status(r.PathValue("login_id"))
	if !ok {
		writeJSONError(w, http.StatusNotFound, "ChatGPT sign-in not found")
		return
	}
	if status.State == "connected" {
		s.oauthInstallMu.Lock()
		defer s.oauthInstallMu.Unlock()
		if s.oauthInstalledID != status.ID {
			s.switcher.Lock()
			err := s.models.SaveChatGPTModels(status.Models)
			if err == nil {
				ref := s.models.Active().Ref
				if strings.HasPrefix(ref, ai_models.ChatGPTProvider+":") {
					err = s.switcher.SwitchModelLocked(ref)
				}
			}
			s.switcher.Unlock()
			if err != nil {
				writeJSONError(w, 500, "ChatGPT connected but model import failed: "+err.Error())
				return
			}
			s.oauthInstalledID = status.ID
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

func (s *server) handleChatGPTCancel(w http.ResponseWriter, r *http.Request) {
	if !localAuthRequest(w, r) {
		return
	}
	status, ok := s.oauth.Status(r.PathValue("login_id"))
	if !ok {
		writeJSONError(w, 404, "ChatGPT sign-in not found")
		return
	}
	if status.State == "pending" {
		s.oauth.Close()
	}
	w.WriteHeader(http.StatusNoContent)
}
