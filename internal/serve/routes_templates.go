package serve

import (
	"net/http"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/prompttemplate"
)

// templateInfo is one prompt template as the phone's dispatch sheet reads it.
type templateInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Scope       string `json:"scope"`
	Agent       string `json:"agent,omitempty"`
	Model       string `json:"model,omitempty"`
	// Dir is the template's project dir with "~/" expanded.
	Dir    string `json:"dir,omitempty"`
	Effort string `json:"effort,omitempty"`
	Prompt string `json:"prompt"`
}

// handleTemplates answers GET /v1/templates?dir=<project>: the templates
// visible from the project (project shadows global), the same set
// `agentctl template list` shows there. dir, when set, must be a fleet
// project's dir; without it only global templates are listed. No templates
// directory is an empty list.
func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir != "" {
		g, out, ok := s.projectGroup(dir, nil)
		if !ok {
			writeError(w, out.status, out.code, out.msg)
			return
		}
		dir = g.Dir
	}
	list, problems, err := prompttemplate.List(dir)
	if err != nil {
		logging.Warn("serve: listing templates: %v", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "reading templates")
		return
	}
	for _, p := range problems {
		logging.Debug("serve: template %s: %s", p.Path, p.Err)
	}
	out := make([]templateInfo, 0, len(list))
	for _, t := range list {
		out = append(out, templateInfo{
			Name: t.Name, Description: t.Description, Scope: string(t.Scope),
			Agent: t.Agent, Model: t.Model, Dir: t.ExpandedDir(), Effort: t.Effort, Prompt: t.Prompt,
		})
	}
	writeJSON(w, http.StatusOK, struct {
		Templates []templateInfo `json:"templates"`
	}{out})
}
