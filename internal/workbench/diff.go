package workbench

import (
	"net/http"
	"os/exec"
)

func (wb *Server) handleValidation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectOpen := wb.project.Get() != nil
	providerConfigured := wb.provider.Get() != nil
	currentSession := wb.sessions.Current() != nil

	status := "blocked"
	if projectOpen && providerConfigured {
		status = "ready"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"project_open":        projectOpen,
		"provider_configured": providerConfigured,
		"current_session":     currentSession,
		"status":              status,
	})
}

func (wb *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	project := wb.project.Get()
	if project == nil {
		http.Error(w, "no project open", http.StatusConflict)
		return
	}
	if !project.Git {
		http.Error(w, "project is not a git repo", http.StatusConflict)
		return
	}

	wb.store.Append("diff.requested", "Requested project diff", nil)

	statOut, statErr := exec.Command("git", "-C", project.Path, "diff", "--stat").Output()
	if statErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok":    false,
			"error": truncateString("git diff --stat failed: "+statErr.Error(), 1000),
		})
		return
	}
	diffOut, diffErr := exec.Command("git", "-C", project.Path, "diff", "--no-ext-diff").Output()
	if diffErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok":    false,
			"error": truncateString("git diff --no-ext-diff failed: "+diffErr.Error(), 1000),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"stat": string(statOut),
		"diff": string(diffOut),
	})
}
