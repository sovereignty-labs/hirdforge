package main

// TaskLB — intelligent agent selection for autonomous task dispatch.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type bifrostCandidate struct {
	Name    string
	Healthy bool
	Active  bool
	Tools   []string
	Fitness float64
}

type bifrostResult struct {
	Agent  string
	Reason string
}

func (g *gateway) selectAgent(taskLabels []string, taskBody string, preferredWarband string) bifrostResult {
	labelAgent := ""
	tierName := ""
	targetWarband := normalizeWarbandName(preferredWarband)
	for _, label := range taskLabels {
		if strings.HasPrefix(label, "agent/") && labelAgent == "" {
			labelAgent = strings.TrimPrefix(label, "agent/")
		}
		if strings.HasPrefix(label, "tier/") && tierName == "" {
			tierName = strings.TrimPrefix(label, "tier/")
		}
	}
	if tierName == "codex" {
		log.Printf("bifrost: tier/codex task rejected from autonomous routing")
		return bifrostResult{}
	}

	active := map[string]bool{}
	g.arMu.RLock()
	for name := range g.activeRequests {
		active[name] = true
	}
	g.arMu.RUnlock()

	all := g.snapshotAgents()
	capable := make([]bifrostCandidate, 0, len(all))
	idleCapable := make([]bifrostCandidate, 0, len(all))
	sameWarbandIdle := make([]bifrostCandidate, 0, len(all))
	for _, agent := range all {
		name := strings.TrimSpace(agent.Name)
		if name == "" {
			continue
		}
		if agent.Role != "builder" {
			log.Printf("bifrost: excluded %s from routing due to role %q", name, agent.Role)
			continue
		}
		if !agent.Healthy {
			log.Printf("bifrost: filtered unhealthy agent %s", name)
			continue
		}
		candidate := bifrostCandidate{
			Name:    name,
			Healthy: agent.Healthy,
			Active:  active[name],
			Tools:   append([]string(nil), agent.Tools...),
			Fitness: 0.5,
		}
		agentWarband := normalizeWarbandName(agent.Warband)
		if !bifrostCapable(candidate.Tools, taskLabels, taskBody) {
			log.Printf("bifrost: filtered incapable agent %s", name)
			continue
		}
		capable = append(capable, candidate)
		if !candidate.Active {
			idleCapable = append(idleCapable, candidate)
		}
		if agentWarband == targetWarband {
			if !candidate.Active {
				sameWarbandIdle = append(sameWarbandIdle, candidate)
			}
		}
	}

	if len(capable) == 0 {
		log.Printf("bifrost: no capable candidates")
		return bifrostResult{}
	}
	chosenPool := idleCapable
	if len(sameWarbandIdle) > 0 {
		chosenPool = sameWarbandIdle
	}
	if len(chosenPool) == 0 {
		if labelAgent != "" {
			log.Printf("bifrost: all capable agents busy, returning label fallback %s", labelAgent)
			return bifrostResult{
				Agent:  labelAgent,
				Reason: fmt.Sprintf("%s selected: all capable agents are busy, using task label preference", labelAgent),
			}
		}
		log.Printf("bifrost: all capable agents busy and no label fallback")
		return bifrostResult{}
	}

	var wg sync.WaitGroup
	for i := range chosenPool {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			chosenPool[idx].Fitness = g.queryAgentFitness(chosenPool[idx].Name, 3*time.Second)
		}(i)
	}
	wg.Wait()

	sort.Slice(chosenPool, func(i, j int) bool {
		if chosenPool[i].Fitness == chosenPool[j].Fitness {
			return chosenPool[i].Name < chosenPool[j].Name
		}
		return chosenPool[i].Fitness > chosenPool[j].Fitness
	})

	chosen := chosenPool[0]
	reason := fmt.Sprintf("%s selected: idle, capable, %.0f%% success rate", chosen.Name, chosen.Fitness*100)
	log.Printf("bifrost: %s", reason)
	return bifrostResult{
		Agent:  chosen.Name,
		Reason: reason,
	}
}

func (g *gateway) selectAgentForTask(task webhookIssue, preferredWarband string) (string, string) {
	result := g.selectAgent(task.Labels, task.Body, preferredWarband)
	if result.Agent != "" {
		return result.Agent, result.Reason
	}
	for _, label := range task.Labels {
		if strings.HasPrefix(label, "agent/") {
			agentName := strings.TrimPrefix(label, "agent/")
			if agentName != "" {
				return agentName, "bifrost: no candidates, using label fallback"
			}
		}
	}
	return "", ""
}

func (g *gateway) queryAgentFitness(agentName string, timeout time.Duration) float64 {
	baseURL := strings.TrimSpace(g.seidrURL)
	if baseURL == "" {
		log.Printf("bifrost: seidr unavailable for %s, using neutral fitness", agentName)
		return 0.5
	}
	payload, err := json.Marshal(map[string]interface{}{
		"query":     "task success PR_CREATED",
		"agent":     agentName,
		"n_results": 20,
		"limit":     20,
	})
	if err != nil {
		log.Printf("bifrost: failed to marshal fitness query for %s: %v", agentName, err)
		return 0.5
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(baseURL, "/")+"/query", bytes.NewReader(payload))
	if err != nil {
		log.Printf("bifrost: failed to build fitness request for %s: %v", agentName, err)
		return 0.5
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("bifrost: seidr query failed for %s: %v", agentName, err)
		return 0.5
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("bifrost: seidr query returned %d for %s", resp.StatusCode, agentName)
		return 0.5
	}
	var out struct {
		Results []struct {
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		log.Printf("bifrost: failed to decode fitness response for %s: %v", agentName, err)
		return 0.5
	}
	if len(out.Results) == 0 {
		log.Printf("bifrost: no fitness history for %s, using neutral fitness", agentName)
		return 0.5
	}
	total := 0
	successes := 0
	for _, item := range out.Results {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		total++
		if strings.Contains(content, "PR_CREATED") {
			successes++
		}
	}
	if total == 0 {
		return 0.5
	}
	score := float64(successes) / float64(total)
	log.Printf("bifrost: fitness for %s = %.2f (%d/%d)", agentName, score, successes, total)
	return score
}

func bifrostCapable(tools []string, taskLabels []string, taskBody string) bool {
	toolSet := map[string]bool{}
	for _, tool := range tools {
		toolSet[strings.TrimSpace(tool)] = true
	}
	body := strings.ToLower(taskBody)

	for _, label := range taskLabels {
		if strings.TrimSpace(label) == "tier/codex" {
			return false
		}
	}

	needsEdit := strings.Contains(body, " edit ") ||
		strings.Contains(body, "modify") ||
		strings.Contains(body, "change") ||
		strings.Contains(body, "replace") ||
		strings.Contains(body, "update") ||
		strings.Contains(body, ".go") ||
		strings.Contains(body, ".py") ||
		strings.Contains(body, ".yaml") ||
		strings.Contains(body, ".yml") ||
		strings.Contains(body, ".js") ||
		strings.Contains(body, ".ts") ||
		strings.Contains(body, "file ") ||
		strings.Contains(body, "path ")
	if needsEdit && !toolSet["edit"] {
		return false
	}

	needsExec := strings.Contains(body, "exec:") ||
		strings.Contains(body, "run ") ||
		strings.Contains(body, "command") ||
		strings.Contains(body, "shell")
	if needsExec && !toolSet["exec"] {
		return false
	}

	needsGitea := strings.Contains(body, "pull request") ||
		strings.Contains(body, "pr ") ||
		strings.Contains(body, "issue") ||
		strings.Contains(body, "label") ||
		strings.Contains(body, "gitea")
	if needsGitea && !toolSet["gitea"] {
		return false
	}

	needsGit := strings.Contains(body, "git clone") ||
		strings.Contains(body, "branch") ||
		strings.Contains(body, "commit") ||
		strings.Contains(body, "repo")
	if needsGit && !(toolSet["git-clone"] || toolSet["git-commit"] || toolSet["exec"]) {
		return false
	}

	return true
}
