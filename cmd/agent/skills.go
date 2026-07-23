package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	toolpkg "git.hirdforge.com/kit/hirdforge/pkg/tools"
)

func soulHasLearnedTool(soulContent, toolName string) bool {
	if strings.TrimSpace(soulContent) == "" || strings.TrimSpace(toolName) == "" {
		return false
	}
	needle := strings.ToLower(toolName)
	for _, line := range strings.Split(soulContent, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "## Learned:") {
			continue
		}
		if strings.Contains(strings.ToLower(line), needle) {
			return true
		}
	}
	return false
}

func fetchRecentToolLessons(memoryURL, agentName, soulContent string) (string, []string) {
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(agentName) == "" {
		return "", nil
	}

	body, _ := json.Marshal(map[string]interface{}{
		"agent": agentName,
		"query": "tool_failure",
		"limit": 5,
	})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(memoryURL, "/")+"/query", bytes.NewReader(body))
	if err != nil {
		return "", nil
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil
	}

	var out struct {
		Results []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
			Text    string `json:"text"`
		} `json:"results"`
		Memories []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
			Text    string `json:"text"`
		} `json:"memories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", nil
	}

	rawItems := out.Results
	if len(rawItems) == 0 {
		rawItems = out.Memories
	}
	if len(rawItems) == 0 {
		return "", nil
	}

	const maxLessonChars = 2000
	lessons := make([]string, 0, len(rawItems))
	ids := make([]string, 0, len(rawItems))
	totalChars := 0
	for _, item := range rawItems {
		text := strings.TrimSpace(item.Content)
		if text == "" {
			text = strings.TrimSpace(item.Text)
		}
		if text == "" {
			continue
		}
		if !strings.HasPrefix(text, "[FAILURE:") && !strings.HasPrefix(text, "[RECOVERY:") {
			continue
		}
		if toolName, _, _, ok := parseToolFailureMemory(text); ok && soulHasLearnedTool(soulContent, toolName) {
			continue
		}
		entryLen := len(text)
		if len(lessons) > 0 {
			entryLen++
		}
		if totalChars+entryLen > maxLessonChars {
			break
		}
		lessons = append(lessons, text)
		if id := strings.TrimSpace(item.ID); id != "" {
			ids = append(ids, id)
		}
		totalChars += entryLen
	}
	if len(lessons) == 0 {
		return "", ids
	}

	return "## Recent Tool Lessons\nThe following are recent tool failures and recoveries from your past sessions. Use these to avoid repeating mistakes:\n<lessons>\n" +
		strings.Join(lessons, "\n") + "\n</lessons>", ids
}

func truncateWords(s string, maxWords int) string {
	if maxWords <= 0 {
		return ""
	}
	parts := strings.Fields(strings.TrimSpace(s))
	if len(parts) <= maxWords {
		return strings.Join(parts, " ")
	}
	return strings.Join(parts[:maxWords], " ") + "..."
}

func fetchReflectionContext(memoryURL, agentName string) string {
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(agentName) == "" {
		return ""
	}
	body, _ := json.Marshal(map[string]string{"agent": agentName})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(memoryURL, "/")+"/reflect", bytes.NewReader(body))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	var out struct {
		Clusters []struct {
			Size    int `json:"size"`
			Members []struct {
				Content string `json:"content"`
			} `json:"members"`
		} `json:"clusters"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Clusters) == 0 {
		return ""
	}
	maxClusters := len(out.Clusters)
	if maxClusters > 2 {
		maxClusters = 2
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Memory reflection: You have %d experience clusters ready for synthesis.\n", len(out.Clusters))
	for i := 0; i < maxClusters; i++ {
		c := out.Clusters[i]
		preview := ""
		if len(c.Members) > 0 {
			preview = truncateWords(c.Members[0].Content, 32)
		}
		fmt.Fprintf(&b, "Cluster %d (%d experiences): %s Consider synthesizing lessons from these patterns using the remember tool with type=lesson.\n", i+1, c.Size, preview)
	}
	return truncateWords(b.String(), 500)
}

func fetchIntuitiveContext(memoryURL, agentName, messageContent string) string {
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(agentName) == "" || len(strings.TrimSpace(messageContent)) < 20 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	query := strings.TrimSpace(messageContent)
	if len(query) > 500 {
		query = query[:500]
	}
	agentResults := querySeidr(ctx, memoryURL, map[string]interface{}{"query": query, "agent": agentName, "limit": 3, "collections": []string{agentName, "warband_shared"}})
	warbandResults := querySeidr(ctx, memoryURL, map[string]interface{}{"query": query, "agent": "warband", "n_results": 5})
	merged := append([]intuitiveResult{}, agentResults...)
	seenPrefixes := make([]string, 0, len(agentResults))
	for _, r := range agentResults {
		if prefix := intuitiveContentPrefix(r.Content); prefix != "" {
			seenPrefixes = append(seenPrefixes, prefix)
		}
	}
	for _, r := range warbandResults {
		prefix := intuitiveContentPrefix(r.Content)
		if prefix != "" {
			duplicate := false
			for _, existing := range seenPrefixes {
				if prefix == existing {
					duplicate = true
					break
				}
			}
			if duplicate {
				continue
			}
		}
		merged = append(merged, r)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].Similarity > merged[j].Similarity
	})
	var lines []string
	for _, r := range merged {
		if r.Similarity < 0.55 {
			continue
		}
		c := strings.TrimSpace(r.Content)
		if c == "" || len(c) < 10 {
			continue
		}
		if len(c) > 200 {
			c = c[:200] + "..."
		}
		lines = append(lines, "- "+c)
		if len(lines) >= 3 {
			break
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "[INTUITION] Relevant past experience:\n" + strings.Join(lines, "\n")
}

type intuitiveResult struct {
	Content    string
	Similarity float64
}

func querySeidr(ctx context.Context, memoryURL string, payload map[string]interface{}) []intuitiveResult {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(memoryURL, "/")+"/query", bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	var result struct {
		Results []struct {
			Content    string  `json:"content"`
			Similarity float64 `json:"similarity"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}
	out := make([]intuitiveResult, 0, len(result.Results))
	for _, r := range result.Results {
		out = append(out, intuitiveResult{
			Content:    r.Content,
			Similarity: r.Similarity,
		})
	}
	return out
}

func intuitiveContentPrefix(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	if len(content) > 50 {
		return content[:50]
	}
	return content
}

func mapOutcomeToSeidr(outcome string) string {
	if outcome == "contradiction" {
		return "contradicted"
	}
	return "confirmed"
}

func validateContextMemoriesAsync(memoryURL, sessionID, outcome string) {
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(sessionID) == "" {
		return
	}
	outcome = strings.TrimSpace(outcome)
	if outcome != "success" && outcome != "contradiction" {
		return
	}
	seidrOutcome := mapOutcomeToSeidr(outcome)
	ids := snapshotSessionContextMemoryIDs(sessionID)
	if len(ids) == 0 {
		return
	}
	for _, memoryID := range ids {
		if !markSessionMemoryValidated(sessionID, memoryID) {
			continue
		}
		go func(mid string) {
			payload, _ := json.Marshal(map[string]string{
				"memory_id": mid,
				"outcome":   seidrOutcome,
			})
			req, err := http.NewRequest(http.MethodPost, strings.TrimRight(memoryURL, "/")+"/validate", bytes.NewReader(payload))
			if err != nil {
				logJSON("warn", "memory validation request build failed", map[string]interface{}{"error": err.Error(), "memory_id": mid})
				return
			}
			req.Header.Set("Content-Type", "application/json")
			client := &http.Client{Timeout: 3 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				logJSON("warn", "memory validation request failed", map[string]interface{}{"error": err.Error(), "memory_id": mid})
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
				logJSON("warn", "memory validation returned non-2xx", map[string]interface{}{"memory_id": mid, "status": resp.StatusCode, "body": strings.TrimSpace(string(b))})
			}
		}(memoryID)
	}
}

func validateSkillAmendmentsAsync(memoryURL, agentName string) {
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(agentName) == "" {
		return
	}

	amendments, err := querySeidrMemories(memoryURL, map[string]interface{}{
		"agent":  agentName,
		"query":  "skill_amendment",
		"limit":  10,
		"filter": map[string]interface{}{"type": "skill_amendment"},
	}, 3*time.Second)
	if err != nil || len(amendments) == 0 {
		return
	}

	filtered := make([]recalledMemory, 0, len(amendments))
	for _, memory := range amendments {
		if metadataString(memory.Metadata, "type") != "skill_amendment" {
			continue
		}
		filtered = append(filtered, memory)
	}
	if len(filtered) == 0 {
		return
	}

	sort.Slice(filtered, func(i, j int) bool {
		return recalledMemoryTime(filtered[i]).After(recalledMemoryTime(filtered[j]))
	})
	if len(filtered) > 10 {
		filtered = filtered[:10]
	}

	for _, amendment := range filtered {
		status := strings.ToLower(strings.TrimSpace(metadataString(amendment.Metadata, "status")))
		if status == "contradicted" {
			continue
		}
		validationChecks := metadataInt(amendment.Metadata, "validation_count")
		if validationChecks >= 3 {
			continue
		}

		reqBody, err := json.Marshal(map[string]interface{}{
			"memory_id": amendment.ID,
			"outcome":   "confirmed",
		})
		if err != nil {
			logJSON("warn", "skill amendment validation request marshal failed", map[string]interface{}{
				"amendment_id": amendment.ID,
				"error":        err.Error(),
			})
			continue
		}

		req, err := http.NewRequest(http.MethodPost, strings.TrimRight(memoryURL, "/")+"/validate", bytes.NewReader(reqBody))
		if err != nil {
			logJSON("warn", "skill amendment validation request build failed", map[string]interface{}{
				"amendment_id": amendment.ID,
				"error":        err.Error(),
			})
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			logJSON("warn", "skill amendment validation request failed", map[string]interface{}{
				"amendment_id": amendment.ID,
				"error":        err.Error(),
			})
			continue
		}
		func() {
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
				logJSON("warn", "skill amendment validation returned non-2xx", map[string]interface{}{
					"amendment_id": amendment.ID,
					"status":       resp.StatusCode,
					"body":         strings.TrimSpace(string(b)),
				})
			}
		}()
	}
}

type recalledMemory struct {
	ID         string
	Text       string
	Similarity float64
	Metadata   map[string]interface{}
	CreatedAt  time.Time
}

func querySeidrMemories(memoryURL string, payload map[string]interface{}, timeout time.Duration) ([]recalledMemory, error) {
	if strings.TrimSpace(memoryURL) == "" {
		return nil, nil
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(memoryURL, "/")+"/query", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if strings.TrimSpace(string(b)) == "" {
			return nil, fmt.Errorf("query returned status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("query returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	type recallItem struct {
		ID         string                 `json:"id"`
		Content    string                 `json:"content"`
		Text       string                 `json:"text"`
		Similarity float64                `json:"similarity"`
		Metadata   map[string]interface{} `json:"metadata"`
		CreatedAt  string                 `json:"created_at"`
		Timestamp  string                 `json:"timestamp"`
	}
	var out struct {
		Results  []recallItem `json:"results"`
		Memories []recallItem `json:"memories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}

	items := out.Results
	if len(items) == 0 {
		items = out.Memories
	}
	memories := make([]recalledMemory, 0, len(items))
	for _, item := range items {
		text := strings.TrimSpace(item.Content)
		if text == "" {
			text = strings.TrimSpace(item.Text)
		}
		if text == "" {
			continue
		}
		memories = append(memories, recalledMemory{
			ID:         strings.TrimSpace(item.ID),
			Text:       text,
			Similarity: item.Similarity,
			Metadata:   item.Metadata,
			CreatedAt:  memoryRecordTime(item.CreatedAt, item.Timestamp, item.Metadata),
		})
	}
	return memories, nil
}

func recallMemories(memoryURL string, payload map[string]interface{}, timeout time.Duration) []recalledMemory {
	memories, err := querySeidrMemories(memoryURL, payload, timeout)
	if err != nil {
		return nil
	}
	return memories
}

func metadataString(meta map[string]interface{}, key string) string {
	if meta == nil {
		return ""
	}
	raw, ok := meta[key]
	if !ok || raw == nil {
		return ""
	}
	if s, ok := raw.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(fmt.Sprint(raw))
}

func metadataInt(meta map[string]interface{}, key string) int {
	if meta == nil {
		return 0
	}
	raw, ok := meta[key]
	if !ok || raw == nil {
		return 0
	}
	switch v := raw.(type) {
	case int:
		return v
	case int8:
		return int(v)
	case int16:
		return int(v)
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float32:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, err := v.Int64()
		if err == nil {
			return int(n)
		}
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n
		}
	}
	return 0
}

func metadataTime(meta map[string]interface{}, keys ...string) time.Time {
	for _, key := range keys {
		if ts := parseRFC3339Time(metadataString(meta, key)); !ts.IsZero() {
			return ts
		}
	}
	return time.Time{}
}

func parseRFC3339Time(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return ts.UTC()
}

func memoryRecordTime(createdAt, timestamp string, meta map[string]interface{}) time.Time {
	if ts := parseRFC3339Time(createdAt); !ts.IsZero() {
		return ts
	}
	if ts := parseRFC3339Time(timestamp); !ts.IsZero() {
		return ts
	}
	return metadataTime(meta, "created_at", "amendment_date", "timestamp")
}

func recalledMemoryTime(memory recalledMemory) time.Time {
	if !memory.CreatedAt.IsZero() {
		return memory.CreatedAt
	}
	return metadataTime(memory.Metadata, "created_at", "amendment_date", "timestamp")
}

func appendCloneMemoryContext(memoryURL, agentName, repoName string, result toolpkg.ToolResult) toolpkg.ToolResult {
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(agentName) == "" || strings.TrimSpace(repoName) == "" || result.Error != "" {
		return result
	}
	memories := recallMemories(memoryURL, map[string]interface{}{
		"agent": agentName,
		"query": repoName,
		"limit": 3,
	}, 3*time.Second)
	if len(memories) == 0 {
		return result
	}
	var contextLines []string
	for _, memory := range memories {
		if memory.Similarity <= 0.5 {
			continue
		}
		content := strings.TrimSpace(memory.Text)
		if content == "" {
			continue
		}
		contextLines = append(contextLines, "- "+content)
	}
	if len(contextLines) == 0 {
		return result
	}
	result.Output += "\n\n[MEMORY CONTEXT for " + repoName + "]\n" + strings.Join(contextLines, "\n") + "\n"
	return result
}

func selfImprovementAllowed(agentName string, now time.Time) bool {
	if strings.TrimSpace(agentName) == "" {
		return false
	}
	selfImproveMu.Lock()
	defer selfImproveMu.Unlock()
	last := selfImproveLastRun[agentName]
	if !last.IsZero() && now.Sub(last) < time.Hour {
		return false
	}
	return true
}

func markSelfImprovementTriggered(agentName string, now time.Time) {
	if strings.TrimSpace(agentName) == "" {
		return
	}
	selfImproveMu.Lock()
	selfImproveLastRun[agentName] = now
	selfImproveMu.Unlock()
}

func parseToolFailureMemory(text string) (toolName, failureType, errText string, ok bool) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "[FAILURE:") {
		if end := strings.Index(text, "]"); end > len("[FAILURE:") {
			failureType = text[len("[FAILURE:"):end]
		}
	}
	toolMatch := regexp.MustCompile(`\btool=([^ ]+)`).FindStringSubmatch(text)
	if len(toolMatch) < 2 {
		return "", "", "", false
	}
	toolName = strings.TrimSpace(toolMatch[1])
	if toolName == "" {
		return "", "", "", false
	}
	if idx := strings.Index(text, " error="); idx >= 0 {
		errText = strings.TrimSpace(text[idx+len(" error="):])
	}
	return toolName, failureType, errText, true
}

func parseToolRecoveryMemory(text string) (toolName string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "[RECOVERY") {
		return "", false
	}
	toolMatch := regexp.MustCompile(`\btool=([^ ]+)`).FindStringSubmatch(text)
	if len(toolMatch) < 2 {
		return "", false
	}
	toolName = strings.TrimSpace(toolMatch[1])
	return toolName, toolName != ""
}

func mostCommonValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	counts := make(map[string]int, len(values))
	best := values[0]
	bestCount := 0
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		counts[value]++
		if counts[value] > bestCount {
			best = value
			bestCount = counts[value]
		}
	}
	return best
}

func recallTimestamp(meta map[string]interface{}) time.Time {
	if meta == nil {
		return time.Time{}
	}
	raw, _ := meta["timestamp"].(string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return ts.UTC()
}

type soulSection struct {
	Heading string
	Body    string
	Raw     string
	Learned bool
	Tool    string
}

func parseLearnedToolFromHeading(heading string) string {
	heading = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(heading), "## Learned:"))
	if heading == "" {
		return ""
	}
	fields := strings.Fields(heading)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func parseSoulSections(content string) []soulSection {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	sections := make([]soulSection, 0)
	current := make([]string, 0)
	flush := func() {
		if len(current) == 0 {
			return
		}
		raw := strings.TrimSpace(strings.Join(current, "\n"))
		if raw == "" {
			current = current[:0]
			return
		}
		heading := ""
		if idx := strings.IndexByte(raw, '\n'); idx >= 0 {
			heading = strings.TrimSpace(raw[:idx])
		} else {
			heading = strings.TrimSpace(raw)
		}
		body := ""
		if idx := strings.IndexByte(raw, '\n'); idx >= 0 {
			body = strings.TrimSpace(raw[idx+1:])
		}
		learned := strings.HasPrefix(heading, "## Learned:")
		sections = append(sections, soulSection{
			Heading: heading,
			Body:    body,
			Raw:     raw,
			Learned: learned,
			Tool:    parseLearnedToolFromHeading(heading),
		})
		current = current[:0]
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") && len(current) > 0 {
			flush()
		}
		current = append(current, line)
	}
	flush()
	return sections
}

func renderSoulSections(sections []soulSection) string {
	parts := make([]string, 0, len(sections))
	for _, sec := range sections {
		raw := strings.TrimSpace(sec.Raw)
		if raw == "" {
			continue
		}
		parts = append(parts, raw)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n") + "\n"
}

func parseSoulAllowedTools(soulContent string) []string {
	trimmed := strings.TrimSpace(soulContent)
	if !strings.HasPrefix(trimmed, "---") {
		return nil
	}
	parts := strings.SplitN(trimmed, "---", 3)
	if len(parts) < 3 {
		return nil
	}
	for _, line := range strings.Split(parts[1], "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "allowed_tools:") {
			value := strings.TrimPrefix(line, "allowed_tools:")
			value = strings.Trim(strings.TrimSpace(value), "[]")
			var tools []string
			for _, t := range strings.Split(value, ",") {
				if t = strings.TrimSpace(t); t != "" {
					tools = append(tools, t)
				}
			}
			return tools
		}
	}
	return nil
}

func countSoulLines(content string) int {
	trimmed := strings.TrimRight(content, "\n")
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

func compactSoul(agentName, content string, maxLines int) string {
	if maxLines <= 0 || countSoulLines(content) <= maxLines {
		return content
	}

	sections := parseSoulSections(content)
	if len(sections) == 0 {
		return content
	}

	type learnedGroup struct {
		latestIdx int
		heading   string
		bodies    []string
		titles    []string
	}
	groups := map[string]*learnedGroup{}
	skipIdx := map[int]bool{}
	for idx, sec := range sections {
		if !sec.Learned || sec.Tool == "" {
			continue
		}
		group, ok := groups[sec.Tool]
		if !ok {
			groups[sec.Tool] = &learnedGroup{
				latestIdx: idx,
				heading:   sec.Heading,
				bodies:    []string{sec.Body},
				titles:    []string{sec.Heading},
			}
			continue
		}
		log.Printf("[COMPACTION] agent=%s removed learned section: %s", agentName, sec.Heading)
		skipIdx[idx] = true
		group.latestIdx = idx
		group.heading = sec.Heading
		group.bodies = append(group.bodies, sec.Body)
		group.titles = append(group.titles, sec.Heading)
	}
	for tool, group := range groups {
		if len(group.bodies) < 2 {
			continue
		}
		latest := sections[group.latestIdx]
		combinedBodies := make([]string, 0, len(group.bodies))
		for _, body := range group.bodies {
			body = strings.TrimSpace(body)
			if body != "" {
				combinedBodies = append(combinedBodies, body)
			}
		}
		latest.Heading = group.heading
		latest.Body = strings.Join(combinedBodies, "\n")
		if latest.Body != "" {
			latest.Raw = latest.Heading + "\n" + latest.Body
		} else {
			latest.Raw = latest.Heading
		}
		sections[group.latestIdx] = latest
		_ = tool
	}

	compacted := make([]soulSection, 0, len(sections))
	for idx, sec := range sections {
		if skipIdx[idx] {
			continue
		}
		compacted = append(compacted, sec)
	}

	rendered := renderSoulSections(compacted)
	if countSoulLines(rendered) <= maxLines {
		return rendered
	}

	for countSoulLines(rendered) > maxLines {
		removeIdx := -1
		removeHeading := ""
		for idx, sec := range compacted {
			if sec.Learned {
				removeIdx = idx
				removeHeading = sec.Heading
				break
			}
		}
		if removeIdx < 0 {
			break
		}
		log.Printf("[COMPACTION] agent=%s removed learned section: %s", agentName, removeHeading)
		compacted = append(compacted[:removeIdx], compacted[removeIdx+1:]...)
		rendered = renderSoulSections(compacted)
	}

	return rendered
}

func executeRegistryTool(reg *toolpkg.Registry, name string, args map[string]interface{}) toolpkg.ToolResult {
	t, ok := reg.Get(name)
	if !ok {
		return toolpkg.ToolResult{Error: "tool not registered: " + name}
	}
	result := t.Execute(args)
	if verifyErr := reg.VerifyResult(name, args, result); verifyErr != nil {
		result = toolpkg.ToolResult{
			Output: result.Output,
			Error:  "verification failed: " + verifyErr.Error(),
		}
	}
	return result
}

// openSelfImprovementPR opens a PR with the soul change, preferring the
// standalone create-pr tool that builder-tier agents have post-hirdforge#249
// and falling back to the gitea umbrella's action=create-pr for agents that
// still register the full suite.
func openSelfImprovementPR(reg *toolpkg.Registry, repoSlug, agentName, shortDescription, body, branch string) toolpkg.ToolResult {
	title := fmt.Sprintf("soul: %s learned — %s", agentName, shortDescription)
	if _, ok := reg.Get("create-pr"); ok {
		return executeRegistryTool(reg, "create-pr", map[string]interface{}{
			"repo":  repoSlug,
			"title": title,
			"body":  body,
			"head":  branch,
			"base":  "main",
		})
	}
	return executeRegistryTool(reg, "gitea", map[string]interface{}{
		"action": "create-pr",
		"repo":   repoSlug,
		"title":  title,
		"body":   body,
		"head":   branch,
		"base":   "main",
	})
}

func checkSelfImprovementTrigger(memoryURL, agentName string, reg *toolpkg.Registry, soulMaxLines int) {
	now := time.Now().UTC()
	if strings.TrimSpace(memoryURL) == "" || strings.TrimSpace(agentName) == "" || reg == nil {
		return
	}
	if !selfImprovementAllowed(agentName, now) {
		return
	}
	for _, toolName := range []string{"git-clone", "read", "write", "git-commit"} {
		if _, ok := reg.Get(toolName); !ok {
			logJSON("warn", "self improvement skipped; required tool missing", map[string]interface{}{"tool": toolName, "agent": agentName})
			return
		}
	}
	// Post-hirdforge#249, builders register `create-pr` standalone while
	// architects/coordinators keep the full `gitea` umbrella. Self-improvement
	// only needs to open one PR, so accept either path: prefer create-pr when
	// present, fall back to gitea-with-action=create-pr for the umbrella case.
	_, hasCreatePR := reg.Get("create-pr")
	_, hasGitea := reg.Get("gitea")
	if !hasCreatePR && !hasGitea {
		logJSON("warn", "self improvement skipped; required tool missing", map[string]interface{}{"tool": "create-pr or gitea", "agent": agentName})
		return
	}

	triggerTool := ""
	triggerCount := 0
	failuresByTool := map[string][]string{}
	failureTextsByTool := map[string][]string{}
	recoveriesByTool := map[string][]string{}
	cutoff := now.Add(-7 * 24 * time.Hour)
	for _, toolName := range []string{"git-clone", "read", "write", "git-commit", "gitea"} {
		memories := recallMemories(memoryURL, map[string]interface{}{
			"agent":  agentName,
			"query":  toolName,
			"limit":  20,
			"filter": map[string]interface{}{"type": "tool_failure", "tool": toolName},
		}, 3*time.Second)
		if len(memories) == 0 {
			continue
		}
		recentCount := 0
		for _, memory := range memories {
			text := memory.Text
			ts := recallTimestamp(memory.Metadata)
			if ts.IsZero() || ts.Before(cutoff) {
				continue
			}
			failureTool, failureType, errText, ok := parseToolFailureMemory(text)
			if !ok || failureTool != toolName {
				continue
			}
			if failureType != "" && failureType != "retry_exhausted" {
				continue
			}
			recentCount++
			failuresByTool[toolName] = append(failuresByTool[toolName], errText)
			failureTextsByTool[toolName] = append(failureTextsByTool[toolName], text)
		}
		if recentCount >= 3 && recentCount > triggerCount {
			triggerTool = toolName
			triggerCount = recentCount
		}
	}
	if triggerTool == "" {
		return
	}
	for _, memory := range recallMemories(memoryURL, map[string]interface{}{
		"agent": agentName,
		"query": triggerTool,
		"limit": 20,
	}, 3*time.Second) {
		if toolName, ok := parseToolRecoveryMemory(memory.Text); ok && toolName == triggerTool {
			recoveriesByTool[toolName] = append(recoveriesByTool[toolName], memory.Text)
		}
	}
	if !selfImprovementAllowed(agentName, now) {
		return
	}
	markSelfImprovementTriggered(agentName, now)

	commonErr := mostCommonValue(failuresByTool[triggerTool])
	if commonErr == "" {
		commonErr = "repeated operational failure"
	}
	recoveryHint := ""
	if recoveries := recoveriesByTool[triggerTool]; len(recoveries) > 0 {
		recoveryHint = recoveries[0]
	}

	repoSlug := "kit/hirdforge-personas"
	repoDir := "hirdforge-personas"
	soulRelPath := filepath.ToSlash(filepath.Join(repoDir, agentName, "soul.md"))
	readRes := executeRegistryTool(reg, "git-clone", map[string]interface{}{"repo": repoSlug})
	if readRes.Error != "" {
		logJSON("warn", "self improvement clone failed", map[string]interface{}{"agent": agentName, "error": readRes.Error})
		return
	}
	soulRes := executeRegistryTool(reg, "read", map[string]interface{}{"path": soulRelPath})
	if soulRes.Error != "" {
		logJSON("warn", "self improvement read failed", map[string]interface{}{"agent": agentName, "path": soulRelPath, "error": soulRes.Error})
		return
	}

	shortDescription := fmt.Sprintf("%s failure guard for %s", triggerTool, agentName)
	heading := "## Learned: " + shortDescription
	if strings.Contains(soulRes.Output, heading) {
		return
	}

	amendmentLines := []string{
		heading,
		fmt.Sprintf("Repeated failures with `%s` were observed. Most common error: %s. Before declaring success, verify prerequisites and confirm the expected side effect.", triggerTool, commonErr),
	}
	if recoveryHint != "" {
		amendmentLines = append(amendmentLines, "Known recovery signal: "+recoveryHint)
	}
	amendment := strings.Join(amendmentLines, "\n")
	updatedSoul := strings.TrimRight(soulRes.Output, "\n") + "\n\n" + amendment + "\n"
	if countSoulLines(updatedSoul) > soulMaxLines {
		compactedSoul := compactSoul(agentName, updatedSoul, soulMaxLines)
		if compactedSoul != updatedSoul {
			updatedSoul = compactedSoul
		}
	}

	writeRes := executeRegistryTool(reg, "write", map[string]interface{}{
		"path":    soulRelPath,
		"content": updatedSoul,
	})
	if writeRes.Error != "" {
		logJSON("warn", "self improvement write failed", map[string]interface{}{"agent": agentName, "path": soulRelPath, "error": writeRes.Error})
		return
	}

	branch := fmt.Sprintf("%s/self-improvement-%s", agentName, now.Format("20060102-150405"))
	commitMsg := fmt.Sprintf("soul: %s learned %s", agentName, shortDescription)
	commitRes := executeRegistryTool(reg, "git-commit", map[string]interface{}{
		"repo":    repoDir,
		"message": commitMsg,
		"branch":  branch,
	})
	if commitRes.Error != "" {
		logJSON("warn", "self improvement commit failed", map[string]interface{}{"agent": agentName, "error": commitRes.Error})
		return
	}

	failures := failureTextsByTool[triggerTool]
	if len(failures) > 5 {
		failures = failures[:5]
	}
	prBody := "Triggered by repeated recent failures:\n\n- " + strings.Join(failures, "\n- ")
	if recoveryHint != "" {
		prBody += "\n\nRecovery observed:\n- " + recoveryHint
	}
	prRes := openSelfImprovementPR(reg, repoSlug, agentName, shortDescription, prBody, branch)
	if prRes.Error != "" {
		logJSON("warn", "self improvement PR failed", map[string]interface{}{"agent": agentName, "error": prRes.Error})
		return
	}
	logJSON("info", "self improvement PR created", map[string]interface{}{"agent": agentName, "tool": triggerTool, "branch": branch})
}

func agentNameFromSoulPath(path string) string {
	base := filepath.Base(strings.TrimSpace(path))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.TrimSuffix(base, "-soul")
	base = strings.TrimSpace(base)
	if base == "" || strings.EqualFold(base, "soul") {
		return ""
	}
	return base
}

type personaRepo struct {
	URL          string
	Root         string
	AgentName    string
	SharedSkills []string
	LoadedFiles  []string
}

func syncPersonaRepo(repoURL, dst string) error {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "-C", dst, "pull", "--ff-only")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git pull failed: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	_ = os.RemoveAll(dst)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", repoURL, dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func initPersonaRepo(repoURL, agentName string) (*personaRepo, error) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return nil, nil
	}
	if strings.TrimSpace(agentName) == "" {
		return nil, fmt.Errorf("--agent-name is required when --persona-repo is set")
	}
	dst := filepath.Join(os.TempDir(), "valhalla-personas")
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return nil, err
	}
	if err := syncPersonaRepo(repoURL, dst); err != nil {
		return nil, err
	}
	return &personaRepo{URL: repoURL, Root: dst, AgentName: agentName}, nil
}

func parseFrontmatterSharedSkills(content string) []string {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "---\n") && !strings.HasPrefix(trimmed, "---\r\n") && trimmed != "---" {
		return nil
	}
	end := -1
	start := 3
	if len(trimmed) > start && trimmed[start] == '\r' {
		start++
	}
	for i := start; i < len(trimmed)-2; i++ {
		if trimmed[i] == '-' && trimmed[i+1] == '-' && trimmed[i+2] == '-' {
			end = i
			break
		}
	}
	if end < 0 {
		return nil
	}
	fm := strings.TrimSpace(trimmed[start:end])
	var skills []string
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "shared_skills:") {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(line, "shared_skills:"))
		val = strings.TrimPrefix(val, "[")
		val = strings.TrimSuffix(val, "]")
		for _, item := range strings.Split(val, ",") {
			item = strings.TrimSpace(item)
			item = strings.Trim(item, "\"'")
			if item != "" {
				skills = append(skills, item)
			}
		}
	}
	return skills
}

func loadPersonaSoul(repo *personaRepo) (string, error) {
	if repo == nil {
		return "", fmt.Errorf("persona repo not configured")
	}
	p := filepath.Join(repo.Root, repo.AgentName, "soul.md")
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", fmt.Errorf("empty soul at %s", p)
	}
	repo.SharedSkills = parseFrontmatterSharedSkills(content)
	return content, nil
}

func discoverPersonaFiles(repo *personaRepo) []string {
	if repo == nil {
		return nil
	}
	agentDir := filepath.Join(repo.Root, repo.AgentName)
	var files []string

	agentSkillsDir := filepath.Join(agentDir, "skills")
	if entries, err := os.ReadDir(agentSkillsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
				files = append(files, filepath.Join(agentSkillsDir, e.Name()))
			}
		}
	}
	sort.Strings(files)

	for _, skill := range repo.SharedSkills {
		skill = strings.TrimSpace(skill)
		if skill == "" {
			continue
		}
		p := filepath.Join(repo.Root, "shared", "skills", skill+".md")
		if _, err := os.Stat(p); err == nil {
			files = append(files, p)
		}
	}

	return files
}

func loadPersonaSessionContext(repo *personaRepo) string {
	if repo == nil {
		return ""
	}
	agentDir := filepath.Join(repo.Root, repo.AgentName)
	var loadedFiles []string
	var blocks []string

	if b := readContextFileBlock("## Tools Reference", filepath.Join(agentDir, "tools.md")); b != "" {
		blocks = append(blocks, b)
		loadedFiles = append(loadedFiles, filepath.ToSlash(filepath.Join(repo.AgentName, "tools.md")))
	}
	if b := readContextFileBlock("## Playbook Reference", filepath.Join(agentDir, "playbook.md")); b != "" {
		blocks = append(blocks, b)
		loadedFiles = append(loadedFiles, filepath.ToSlash(filepath.Join(repo.AgentName, "playbook.md")))
	}

	personaFiles := discoverPersonaFiles(repo)
	if len(personaFiles) > 0 {
		var fileBlocks []string
		for _, p := range personaFiles {
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				continue
			}
			rel := p
			if r, err := filepath.Rel(repo.Root, p); err == nil {
				rel = filepath.ToSlash(r)
			} else {
				rel = filepath.ToSlash(p)
			}
			fileBlocks = append(fileBlocks, "### "+rel+"\n"+content)
			loadedFiles = append(loadedFiles, rel)
		}
		if len(fileBlocks) > 0 {
			blocks = append(blocks, "## Shared Context\n"+strings.Join(fileBlocks, "\n\n"))
		}
	}

	repo.LoadedFiles = loadedFiles
	logJSON("info", "persona_loaded", map[string]interface{}{
		"event": "persona_loaded",
		"agent": repo.AgentName,
		"files": loadedFiles,
		"count": len(loadedFiles),
	})

	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, "\n\n")
}

func agentNameFromSoul(soul string) string {
	line := strings.TrimSpace(strings.SplitN(soul, "\n", 2)[0])
	line = strings.TrimPrefix(line, "# ")
	line = strings.TrimPrefix(line, "#")
	line = strings.TrimSpace(line)
	if line == "" {
		return "valhalla-agent"
	}
	return line
}
