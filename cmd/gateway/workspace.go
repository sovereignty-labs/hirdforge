package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	taskspkg "git.hirdforge.com/kit/hirdforge/pkg/tasks"
	workspacepkg "git.hirdforge.com/kit/hirdforge/pkg/workspace"
)

func (g *gateway) applyWorkspaceEvent(agentName string, evt map[string]interface{}) {
	if g == nil || g.projector == nil {
		return
	}
	eventType, _ := evt["type"].(string)
	if !workspacepkg.IsTypedEvent(eventType) {
		return
	}
	typed := make(map[string]interface{}, len(evt)+2)
	// For delegate events with no explicit session_id (Comms-originated
	// architect work has no taskID to carry), fall back to the architect's
	// active Comms session. This mutation cascades through projector.Apply,
	// addDelegationTimelineEvent, and SetArchitectContext, all of which read
	// session_id from the same evt map.
	if eventType == "delegate" {
		if strings.TrimSpace(asString(evt["session_id"])) == "" {
			if fallback := g.activeSessionID(agentName); fallback != "" {
				evt["session_id"] = fallback
				typed["session_id"] = fallback
			}
		}
	}
	g.projector.Apply(agentName, eventType, evt)
	if eventType == "delegate" {
		sessionID := strings.TrimSpace(asString(evt["session_id"]))
		if sessionID != "" {
			objective := strings.TrimSpace(asString(evt["objective_summary"]))
			if sess, ok := g.sessionStore.get(sessionID); ok {
				if derived := objectiveFromSession(sess); derived != "" {
					objective = derived
				}
			}
			g.projector.SetArchitectContext(agentName, sessionID, objective)
		}
	}
	now := time.Now().Format(time.RFC3339)
	for k, v := range evt {
		typed[k] = v
	}
	typed["agent"] = agentName
	typed["timestamp"] = now
	if sessionID := strings.TrimSpace(asString(evt["session_id"])); sessionID != "" {
		g.addDelegationTimelineEvent(sessionID, delegationTimelineEvent{
			Type:      eventType,
			Agent:     agentName,
			Metadata:  typed,
			Timestamp: now,
		})
	}
	g.broadcastPayload(typed)
	workspace := g.projector.Get(agentName)
	if eventType == "delegate" {
		workspace = g.refreshArchitectWorkspace(agentName)
	}
	g.broadcastPayload(map[string]interface{}{
		"type":      "workspace_update",
		"agent":     agentName,
		"timestamp": now,
		"workspace": workspace,
	})
}

func collectGatewayTaskText(task taskspkg.Task) string {
	parts := []string{task.Content, task.Result}
	for _, tool := range task.Tools {
		parts = append(parts, tool.Input, tool.Output)
	}
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, "\n")
}

func gatewayTaskPRNumber(task taskspkg.Task) int {
	raw := collectGatewayTaskText(task)
	if match := taskPRURLRE.FindStringSubmatch(raw); len(match) == 2 {
		n, _ := strconv.Atoi(match[1])
		return n
	}
	if match := taskPRRefRE.FindStringSubmatch(raw); len(match) == 2 {
		n, _ := strconv.Atoi(match[1])
		return n
	}
	if match := taskPullRefRE.FindStringSubmatch(raw); len(match) == 2 {
		n, _ := strconv.Atoi(match[1])
		return n
	}
	return 0
}

func workspaceTaskRef(task taskspkg.Task) workspacepkg.TaskRef {
	return workspacepkg.TaskRef{
		ID:        task.ID,
		Agent:     task.Agent,
		Status:    task.Status,
		Content:   task.Content,
		SessionID: task.SessionID,
		PRNumber:  gatewayTaskPRNumber(task),
		CreatedAt: task.CreatedAt,
		UpdatedAt: task.UpdatedAt,
	}
}

func workspaceTimelineEvents(events []delegationTimelineEvent) []workspacepkg.TimelineEvent {
	if len(events) == 0 {
		return nil
	}
	out := make([]workspacepkg.TimelineEvent, 0, len(events))
	for _, evt := range events {
		metadata := map[string]interface{}{}
		for k, v := range evt.Metadata {
			metadata[k] = v
		}
		out = append(out, workspacepkg.TimelineEvent{
			Type:      evt.Type,
			Agent:     evt.Agent,
			Metadata:  metadata,
			Timestamp: evt.Timestamp,
		})
	}
	return out
}

func (g *gateway) fetchTasks(ctx context.Context, status, from, agentFilter string) []taskspkg.Task {
	if g == nil {
		return nil
	}
	agentsToQuery := g.snapshotAgents()
	tasksOut := make([]taskspkg.Task, 0)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, a := range agentsToQuery {
		if agentFilter != "" && a.Name != agentFilter {
			continue
		}
		wg.Add(1)
		go func(agent Agent) {
			defer wg.Done()
			tasks := g.fetchTasksFromAgent(ctx, agent, status, from)
			mu.Lock()
			tasksOut = append(tasksOut, tasks...)
			mu.Unlock()
		}(a)
	}
	wg.Wait()
	sort.Slice(tasksOut, func(i, j int) bool { return tasksOut[i].CreatedAt.After(tasksOut[j].CreatedAt) })
	return tasksOut
}

// fetchTasksFromAgent fetches tasks from a single agent. This is more efficient
// than fetchTasks when the target agent is already known, avoiding N-1 unnecessary
// HTTP calls when only one agent's task list is needed.
func (g *gateway) fetchTasksFromAgent(ctx context.Context, agent Agent, status, from string) []taskspkg.Task {
	if g == nil {
		return nil
	}
	u := strings.TrimRight(agent.URL, "/") + "/tasks"
	params := url.Values{}
	if status != "" {
		params.Set("status", status)
	}
	if q := params.Encode(); q != "" {
		u += "?" + q
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	client := &http.Client{Timeout: agentRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	var tasks []taskspkg.Task
	if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
		return nil
	}
	if from != "" {
		filtered := tasks[:0]
		for _, t := range tasks {
			if t.From == from {
				filtered = append(filtered, t)
			}
		}
		tasks = filtered
	}
	return tasks
}

func (g *gateway) refreshArchitectWorkspace(agentName string) workspacepkg.AgentWorkspace {
	if g == nil || g.projector == nil {
		return workspacepkg.AgentWorkspace{AgentName: agentName, FilesTouched: map[string]workspacepkg.FileState{}}
	}
	ws := g.projector.Get(agentName)
	sessionID := strings.TrimSpace(ws.CurrentSessionID)
	if sessionID == "" {
		return g.projector.SetArchitectSessionData(agentName, "", nil, nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tasks := g.fetchTasks(ctx, "", agentName, "")
	refs := make([]workspacepkg.TaskRef, 0, len(tasks))
	for _, task := range tasks {
		if strings.TrimSpace(task.SessionID) != sessionID {
			continue
		}
		refs = append(refs, workspaceTaskRef(task))
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].CreatedAt.Before(refs[j].CreatedAt) })
	return g.projector.SetArchitectSessionData(agentName, sessionID, refs, workspaceTimelineEvents(g.delegationTimeline(sessionID)))
}

func (g *gateway) refreshArchitectWorkspacesBySession(sessionID string) {
	if g == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	now := time.Now().Format(time.RFC3339)
	for _, agent := range g.snapshotAgents() {
		ws := g.projector.Get(agent.Name)
		if strings.TrimSpace(ws.CurrentSessionID) != sessionID {
			continue
		}
		updated := g.refreshArchitectWorkspace(agent.Name)
		g.broadcastPayload(map[string]interface{}{
			"type":      "workspace_update",
			"agent":     agent.Name,
			"timestamp": now,
			"workspace": updated,
		})
	}
}
