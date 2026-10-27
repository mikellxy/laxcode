package workspace

import (
	"encoding/json"
	"io"
	"net/http"
)

func (s *API) HandleGetUserConfig(w http.ResponseWriter, r *http.Request) {
	if s.repo == nil || s.GuideVersion == "" {
		writeJSONError(w, http.StatusServiceUnavailable, "user config unavailable")
		return
	}
	values, err := s.repo.GetUserConfig(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "load user config failed")
		return
	}
	values["user_guide_version"], _ = json.Marshal(s.GuideVersion)
	if _, ok := values["user_guide_seen_version"]; !ok {
		values["user_guide_seen_version"] = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(values)
}

func (s *API) HandlePutUserConfig(w http.ResponseWriter, r *http.Request) {
	if s.repo == nil || s.GuideVersion == "" {
		writeJSONError(w, http.StatusServiceUnavailable, "user config unavailable")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	var values map[string]json.RawMessage
	if err != nil || json.Unmarshal(body, &values) != nil || values == nil {
		writeJSONError(w, http.StatusBadRequest, "expected a user config object")
		return
	}
	for key, value := range values {
		switch key {
		case "user_guide_seen_version":
			var version string
			if json.Unmarshal(value, &version) != nil || version == "" || len(version) > 64 {
				writeJSONError(w, http.StatusBadRequest, "user_guide_seen_version must be a nonempty string of at most 64 characters")
				return
			}
			if version != s.GuideVersion {
				writeJSONProtocolError(w, http.StatusConflict, "GUIDE_VERSION_CHANGED", "引导页已更新，请刷新页面后查看", "")
				return
			}
		default:
			writeJSONError(w, http.StatusBadRequest, "unknown or read-only user config key")
			return
		}
	}
	if err := s.repo.UpdateUserConfig(r.Context(), values); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "save user config failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
