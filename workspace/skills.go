package workspace

import (
	"encoding/json"
	"net/http"

	"github.com/mikellxy/laxcode/internal/domain/prompt"
	"github.com/mikellxy/laxcode/internal/infrastructure/skillrepo"
)

type skillDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *API) HandleListSkills(w http.ResponseWriter, _ *http.Request) {
	source := skillrepo.NewWithRoot(s.HomeDir, s.UserRoot)
	skills := make([]skillDTO, 0)
	for _, skill := range prompt.LoadSkills(source, "", nil) {
		skills = append(skills, skillDTO{Name: skill.Name, Description: skill.Description})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		Skills []skillDTO `json:"skills"`
	}{Skills: skills})
}
