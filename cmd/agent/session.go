package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	taskspkg "git.hirdforge.com/kit/hirdforge/pkg/tasks"
	toolpkg "git.hirdforge.com/kit/hirdforge/pkg/tools"
)

type reviewContextTracker struct {
	mu          sync.Mutex
	agentName   string
	gatewayURL  string
	idleTimeout time.Duration
	state       agentWorkspaceState
	lastPRTouch time.Time
	stopCh      chan struct{}
	prLookup    func(*prRef) bool
}

func newReviewContextTracker(agentName, gatewayURL string, idleTimeout time.Duration) *reviewContextTracker {
	tracker := &reviewContextTracker{
		agentName:   agentName,
		gatewayURL:  strings.TrimSpace(gatewayURL),
		idleTimeout: idleTimeout,
		stopCh:      make(chan struct{}),
		prLookup:    func(*prRef) bool { return false },
	}
	go tracker.runIdleLoop()
	return tracker
}

func (t *reviewContextTracker) Stop() {
	if t == nil {
		return
	}
	close(t.stopCh)
}

func (t *reviewContextTracker) SetPRLookup(fn func(*prRef) bool) {
	if t == nil || fn == nil {
		return
	}
	t.mu.Lock()
	t.prLookup = fn
	t.mu.Unlock()
}

func (t *reviewContextTracker) UpdateFromTool(toolName string, args map[string]interface{}, result toolpkg.ToolResult) {
	if t == nil || result.Error != "" {
		return
	}
	ref, reviewFile, matched := t.capture(toolName, args)
	if !matched {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastPRTouch = time.Now()
	changed := false
	if ref != nil {
		if t.state.CurrentPR == nil || *t.state.CurrentPR != *ref {
			refCopy := *ref
			t.state.CurrentPR = &refCopy
			t.state.CurrentReviewFile = ""
			changed = true
		}
	}
	if reviewFile != "" && strings.TrimSpace(t.state.CurrentReviewFile) != reviewFile {
		t.state.CurrentReviewFile = reviewFile
		changed = true
	}
	if changed {
		t.emitLocked()
	}
}

func (t *reviewContextTracker) ClearIfTaskWithoutPR(content string) {
	if t == nil || taskMentionsPR(content) {
		return
	}
	t.clear()
}

func (t *reviewContextTracker) shouldClearForIdle() bool {
	if t == nil || t.idleTimeout <= 0 {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state.CurrentPR == nil || t.lastPRTouch.IsZero() {
		return false
	}
	return time.Since(t.lastPRTouch) >= t.idleTimeout
}

func (t *reviewContextTracker) runIdleLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if t.shouldClearForIdle() {
				t.clear()
			}
		case <-t.stopCh:
			return
		}
	}
}

func (t *reviewContextTracker) clear() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state.CurrentPR == nil && t.state.CurrentReviewFile == "" {
		return
	}
	t.state.CurrentPR = nil
	t.state.CurrentReviewFile = ""
	t.lastPRTouch = time.Time{}
	t.emitLocked()
}

func (t *reviewContextTracker) capture(toolName string, args map[string]interface{}) (*prRef, string, bool) {
	toolName = strings.TrimSpace(strings.ToLower(toolName))
	if toolName == "" {
		return nil, "", false
	}
	if toolName == "http" {
		return parseGatewayPRRequest(args)
	}
	if ref, reviewFile, ok := directPRToolContext(toolName, args); ok {
		return ref, reviewFile, true
	}
	if !ambiguousPRTool(toolName) {
		return nil, "", false
	}
	ref, ok := prRefFromArgs(args)
	if !ok {
		return nil, "", false
	}
	t.mu.Lock()
	lookup := t.prLookup
	t.mu.Unlock()
	if lookup != nil && lookup(ref) {
		return ref, "", true
	}
	return nil, "", false
}

func (t *reviewContextTracker) emitLocked() {
	workspaceCopy := map[string]interface{}{
		"current_review_file": t.state.CurrentReviewFile,
	}
	if t.state.CurrentPR != nil {
		workspaceCopy["current_pr"] = map[string]interface{}{
			"owner": t.state.CurrentPR.Owner,
			"repo":  t.state.CurrentPR.Repo,
			"index": t.state.CurrentPR.Index,
		}
	}
	notifyGateway(t.gatewayURL, "workspace_update", t.agentName, map[string]interface{}{
		"workspace": workspaceCopy,
	})
}

func taskMentionsPR(content string) bool {
	text := strings.TrimSpace(content)
	if text == "" {
		return false
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bpr\s*#\d+\b`),
		regexp.MustCompile(`(?i)\bpull request\s*#?\d+\b`),
		regexp.MustCompile(`(?i)/pulls/\d+\b`),
		regexp.MustCompile(`(?i)\b[\w.-]+/[\w.-]+#\d+\b`),
	}
	for _, pattern := range patterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

func directPRToolContext(toolName string, args map[string]interface{}) (*prRef, string, bool) {
	ref, ok := prRefFromArgs(args)
	if !ok {
		return nil, "", false
	}
	knownPRTools := map[string]bool{
		"create-pr":     true,
		"create-review": true,
		"list-pr-files": true,
		"merge-pr":      true,
	}
	if strings.Contains(toolName, "pr") || strings.Contains(toolName, "pull") || knownPRTools[toolName] {
		reviewFile := strings.TrimSpace(fmt.Sprint(args["file"]))
		if reviewFile == "" || reviewFile == "<nil>" {
			reviewFile = strings.TrimSpace(fmt.Sprint(args["path"]))
			if reviewFile == "<nil>" {
				reviewFile = ""
			}
		}
		return ref, reviewFile, true
	}
	return nil, "", false
}

func ambiguousPRTool(toolName string) bool {
	knownAmbiguousPRTools := map[string]bool{
		"close-issue": true,
		"comment":     true,
		"get-issue":   true,
	}
	return knownAmbiguousPRTools[toolName]
}

func parseGatewayPRRequest(args map[string]interface{}) (*prRef, string, bool) {
	rawURL := strings.TrimSpace(fmt.Sprint(args["url"]))
	if rawURL == "" || rawURL == "<nil>" {
		return nil, "", false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i+7 < len(parts); i++ {
		if parts[i] != "api" || parts[i+1] != "v1" || parts[i+2] != "gitea" || parts[i+3] != "prs" {
			continue
		}
		index, err := strconv.Atoi(parts[i+6])
		if err != nil || index <= 0 {
			return nil, "", false
		}
		ref := &prRef{
			Owner: strings.TrimSpace(parts[i+4]),
			Repo:  strings.TrimSpace(parts[i+5]),
			Index: index,
		}
		if ref.Owner == "" || ref.Repo == "" {
			return nil, "", false
		}
		reviewFile := ""
		if i+7 < len(parts) && parts[i+7] == "diff" {
			reviewFile = strings.TrimSpace(parsed.Query().Get("file"))
		}
		return ref, reviewFile, true
	}
	return nil, "", false
}

func prRefFromArgs(args map[string]interface{}) (*prRef, bool) {
	repoArg := strings.TrimSpace(fmt.Sprint(args["repo"]))
	if repoArg == "" || repoArg == "<nil>" {
		return nil, false
	}
	owner := strings.TrimSpace(fmt.Sprint(args["owner"]))
	repo := repoArg
	if strings.Contains(repoArg, "/") {
		parts := strings.SplitN(repoArg, "/", 2)
		owner = strings.TrimSpace(parts[0])
		repo = strings.TrimSpace(parts[1])
	}
	if owner == "" {
		owner = strings.TrimSpace(os.Getenv("GITEA_DEFAULT_OWNER"))
		if owner == "" {
			owner = "gitea_admin"
		}
	}
	index := firstPositiveIntArg(args, "index", "number", "pull_number")
	if repo == "" || index <= 0 {
		return nil, false
	}
	return &prRef{Owner: owner, Repo: repo, Index: index}, true
}

func firstPositiveIntArg(args map[string]interface{}, keys ...string) int {
	for _, key := range keys {
		switch value := args[key].(type) {
		case int:
			if value > 0 {
				return value
			}
		case int64:
			if value > 0 {
				return int(value)
			}
		case float64:
			if value > 0 {
				return int(value)
			}
		case string:
			text := strings.TrimSpace(strings.TrimPrefix(value, "#"))
			if text == "" {
				continue
			}
			if n, err := strconv.Atoi(text); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

// buildPeerSystemBlock renders the "## Your Peers" block injected between the
// soul and the bootstrap context. The model only learns peer names through
// this block (and the delegate tool's agent-param hint); without it the soul
// would be the only source of peer knowledge. Returns "" when no peers are
// configured so the block is omitted entirely rather than rendered empty.
func buildPeerSystemBlock(peers, roles map[string]string) string {
	if len(peers) == 0 {
		return ""
	}
	names := make([]string, 0, len(peers))
	for name := range peers {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("## Your Peers\n")
	b.WriteString("Use the delegate tool to send tasks to these agents. Each one runs autonomously and reports back.\n\n")
	for _, name := range names {
		if role := strings.TrimSpace(roles[name]); role != "" {
			fmt.Fprintf(&b, "- %s (%s)\n", name, role)
			continue
		}
		fmt.Fprintf(&b, "- %s\n", name)
	}
	return strings.TrimRight(b.String(), "\n")
}

const maxCompletionNudges = 2

var prRequestPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)create\s+(a\s+)?PR`),
	regexp.MustCompile(`(?i)PR\s+URL`),
	regexp.MustCompile(`(?i)include\s+(the\s+)?PR\s+URL`),
	regexp.MustCompile(`(?i)Done\s+only\s+when\s+(the\s+)?PR`),
	regexp.MustCompile(`(?i)branch:.*(?:pr|pull\s*request)`),
	regexp.MustCompile(`(?i)title:.*(?:pr|pull\s*request)`),
}

// Completion-signal patterns: the single source of truth for what counts as a
// terminal agent result. They carry capture groups (the PR number, the FAILED /
// NOOP reason text) so the result-extraction seam (classifyRunOutcome in
// result_outcome.go) extracts from the exact same patterns that detection uses —
// preventing drift between detection and extraction. Capture groups do not
// affect MatchString, so detection behavior is unchanged.
var (
	completionPRURLPattern  = regexp.MustCompile(`(?i)https?://[^\s]+/[^/]+/[^/]+/pulls/(\d+)`)
	completionPRNumPattern  = regexp.MustCompile(`(?i)\bPR\s+#(\d+)\b`)
	completionFailedPattern = regexp.MustCompile(`(?i)\bFAILED:\s*(.*)`)
	completionNoopPattern   = regexp.MustCompile(`(?i)\bNOOP:\s*(.*)`)
)

// completionSignals is the detection view of the patterns above, consumed by
// contentHasCompletionSignal.
var completionSignals = []*regexp.Regexp{
	completionPRURLPattern,
	completionPRNumPattern,
	completionFailedPattern,
	completionNoopPattern,
}

type completionNudgeState struct {
	mu           sync.Mutex
	nudgeCounts  map[string]int
	recentErrors map[string]bool
}

var completionNudges = &completionNudgeState{
	nudgeCounts:  make(map[string]int),
	recentErrors: make(map[string]bool),
}

func (s *completionNudgeState) increment(sessionID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nudgeCounts[sessionID]++
	return s.nudgeCounts[sessionID]
}

func (s *completionNudgeState) hasExhausted(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nudgeCounts[sessionID] >= maxCompletionNudges
}

func (s *completionNudgeState) recordError(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recentErrors[sessionID] = true
}

func (s *completionNudgeState) hasRecentError(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recentErrors[sessionID]
}

func requestRequiresCompletionSignal(userContent string) bool {
	lower := strings.ToLower(userContent)
	for _, pat := range prRequestPatterns {
		if pat.MatchString(lower) {
			return true
		}
	}
	return false
}

func contentHasCompletionSignal(content string) bool {
	for _, pat := range completionSignals {
		if pat.MatchString(content) {
			return true
		}
	}
	return false
}

type conversationProcessor func(ctx context.Context, sessionID, taskID, content string, emit func(interface{}) bool, logTool func(taskspkg.ToolLog)) (string, error)

type conversationDeps struct {
	workspace        string
	inferenceTimeout int
	memoryURL        string
	bootstrap        bool
	intuition        bool
	episodic         bool
	maxContext       int
	model            string
	apiKey           string
	maxToolRetries   int
	maxToolRounds    int
	gatewayURL       string
	giteaURL         string
	inferenceURL     string
	soulMaxLines     int
	toolsFile        string
	playbookFile     string
	agentName        string
	soul             string
	modelTemplate    string
	peers            map[string]string
	peerRoles        map[string]string
	persona          *personaRepo
	reg              *toolpkg.Registry
	toolDefs         []toolDef
	giteaTool        *toolpkg.GiteaAPITool
	reviewTracker    *reviewContextTracker
}

func newConversationProcessor(deps conversationDeps) conversationProcessor {
	emitNoop := func(interface{}) bool { return true }
	return func(ctx context.Context, sessionID, taskID, content string, emit func(interface{}) bool, logTool func(taskspkg.ToolLog)) (string, error) {
		if emit == nil {
			emit = emitNoop
		}
		withInferenceTimeout := func(parent context.Context) (context.Context, context.CancelFunc) {
			return context.WithTimeout(parent, time.Duration(deps.inferenceTimeout)*time.Second)
		}
		hadToolCalls := false
		// M6: the externalized plan is session-scoped; drop it when the session
		// ends so the in-memory store does not accumulate stale lists.
		defer sessionTodos.clear(sessionID)
		// M4: same for the read-before-edit tracking.
		defer toolpkg.ClearReadState(sessionID)
		defer func() {
			if !hadToolCalls {
				return
			}
			go checkSelfImprovementTrigger(deps.memoryURL, deps.agentName, deps.reg, deps.soulMaxLines)
		}()
		defer func() {
			sessionsMu.Lock()
			ttPtr := trackedTasks[sessionID]
			var tt trackedTask
			if ttPtr != nil {
				tt = *ttPtr
				tt.FilesWritten = append([]string(nil), ttPtr.FilesWritten...)
			}
			delete(trackedTasks, sessionID)
			sessionsMu.Unlock()
			if ttPtr == nil || tt.IssueNumber == 0 || tt.PRCreated {
				return
			}
			go func(owner, repoName string, issueNum int, tt trackedTask) {
				if deps.giteaTool == nil {
					return
				}
				retryCount := 0
				comments, err := deps.giteaTool.GetComments(owner, repoName, issueNum)
				if err == nil {
					for _, c := range comments {
						body, _ := c["body"].(string)
						if strings.Contains(body, "## Retry Context") {
							retryCount++
						}
					}
				}
				filesStr := "none"
				if len(tt.FilesWritten) > 0 {
					filesStr = strings.Join(tt.FilesWritten, ", ")
				}
				branchStr := "none"
				if tt.BranchName != "" {
					branchStr = tt.BranchName
				}
				comment := fmt.Sprintf(
					"## Retry Context (attempt %d)\n**Agent:** %s\n**Failed at:** session ended without PR\n**Branch:** %s\n**Files written:** %s\n**Suggested approach:** Check if branch exists, verify files, complete remaining steps (commit, push, create PR)",
					retryCount+1,
					deps.agentName,
					branchStr,
					filesStr,
				)
				if err := deps.giteaTool.PostComment(owner, repoName, issueNum, comment); err != nil {
					logJSON("warn", "dlq: comment failed", map[string]interface{}{"issue": issueNum, "error": err.Error()})
				}
				labels, err := deps.giteaTool.GetIssueLabels(owner, repoName, issueNum)
				if err != nil {
					labels = []string{}
				}
				newLabels := make([]string, 0, len(labels)+1)
				for _, l := range labels {
					if !strings.HasPrefix(l, "status/") {
						newLabels = append(newLabels, l)
					}
				}
				if retryCount+1 >= 3 {
					newLabels = append(newLabels, "status/failed")
					logJSON("warn", "dlq: issue permanently failed after 3 attempts", map[string]interface{}{"issue": issueNum})
				} else {
					newLabels = append(newLabels, "status/retry")
					logJSON("info", "dlq: issue marked for retry", map[string]interface{}{"issue": issueNum, "attempt": retryCount + 1})
				}
				if err := deps.giteaTool.ReplaceLabels(owner, repoName, issueNum, newLabels); err != nil {
					logJSON("warn", "dlq: label update failed", map[string]interface{}{"issue": issueNum, "error": err.Error()})
				}
			}(tt.IssueOwner, tt.IssueRepo, tt.IssueNumber, tt)
		}()

		sessionsMu.Lock()
		history := append([]message(nil), sessions[sessionID]...)
		firstMessage := !seenSessions[sessionID]
		if firstMessage {
			seenSessions[sessionID] = true
		}
		sessionsMu.Unlock()
		sanitizeHistory := func(history []message) []message {
			if len(history) == 0 {
				return history
			}
			openToolCalls := map[string]int{}
			assistantWithToolCalls := map[int]bool{}
			assistantMatchedToolResponse := map[int]bool{}
			removeIdx := map[int]bool{}
			for i, msg := range history {
				switch msg.Role {
				case "assistant":
					if len(msg.ToolCalls) == 0 {
						continue
					}
					assistantWithToolCalls[i] = true
					for _, tc := range msg.ToolCalls {
						tcID := strings.TrimSpace(tc.ID)
						if tcID == "" {
							continue
						}
						openToolCalls[tcID] = i
					}
				case "tool":
					tcID := strings.TrimSpace(msg.ToolCallID)
					if tcID == "" {
						removeIdx[i] = true
						continue
					}
					assistantIdx, ok := openToolCalls[tcID]
					if !ok {
						removeIdx[i] = true
						continue
					}
					delete(openToolCalls, tcID)
					assistantMatchedToolResponse[assistantIdx] = true
				}
			}
			for assistantIdx := range assistantWithToolCalls {
				if !assistantMatchedToolResponse[assistantIdx] {
					removeIdx[assistantIdx] = true
				}
			}
			if len(removeIdx) == 0 {
				return history
			}
			removed := 0
			out := make([]message, 0, len(history)-len(removeIdx))
			for i, msg := range history {
				if removeIdx[i] {
					removed++
					continue
				}
				out = append(out, msg)
			}
			logJSON("info", "sanitized orphaned tool messages", map[string]interface{}{"removed": removed, "session_id": sessionID})
			return out
		}
		history = sanitizeHistory(history)
		if deps.episodic && firstMessage && strings.TrimSpace(deps.memoryURL) != "" {
			loaded := loadEpisodicState(deps.memoryURL, deps.agentName)
			if loaded != "" {
				sessionsMu.Lock()
				episodicStates[sessionID] = loaded
				sessionsMu.Unlock()
			}
		}

		bootstrapContext := ""
		reflectionContext := ""
		if firstMessage {
			if deps.persona != nil {
				if err := syncPersonaRepo(deps.persona.URL, deps.persona.Root); err != nil {
					logJSON("warn", "persona repo refresh failed", map[string]interface{}{"error": err.Error()})
				}
			}
			if deps.bootstrap {
				var ids []string
				bootstrapContext, ids = buildSessionBootstrapContext(deps.memoryURL, deps.agentName, deps.toolsFile, deps.playbookFile, deps.soul, deps.persona)
				if len(ids) > 0 {
					addSessionContextMemoryIDs(sessionID, ids)
				}
				go validateSkillAmendmentsAsync(deps.memoryURL, deps.agentName)
				reflectionContext = fetchReflectionContext(deps.memoryURL, deps.agentName)
			}
		}
		// Final system-prompt assembly order:
		//   [model template] + "\n\n" + [soul] + [peers block] + [bootstrap] + [reflection]
		// The model template is prepended so model-specific behavioral
		// anchoring (e.g. Qwen's "DO THE TASK IN THE MESSAGE.") frames the
		// soul rather than being buried beneath it. Empty modelTemplate
		// preserves prior soul-only behavior for tests and back-compat.
		systemContent := deps.soul
		if tmpl := strings.TrimRight(deps.modelTemplate, "\n"); strings.TrimSpace(tmpl) != "" {
			systemContent = tmpl + "\n\n" + systemContent
		}
		// M5: the persona says who the agent is; the procedure says how the work
		// is done. Injected after the identity so the mechanical sequence is
		// stated by the harness rather than left implicit in persona prose.
		if procedure := builderProcedure(deps.reg); procedure != "" {
			systemContent += "\n\n" + procedure
		}
		if peersBlock := buildPeerSystemBlock(deps.peers, deps.peerRoles); peersBlock != "" {
			systemContent += "\n\n" + peersBlock
		}
		if deps.bootstrap {
			if strings.TrimSpace(bootstrapContext) != "" {
				systemContent += "\n\n" + bootstrapContext
			}
			if strings.TrimSpace(reflectionContext) != "" {
				systemContent += "\n\n" + reflectionContext
			}
		}
		messages := []message{{Role: "system", Content: systemContent}}
		sessionsMu.Lock()
		episodicNarrative := episodicStates[sessionID]
		sessionsMu.Unlock()
		if deps.episodic && strings.TrimSpace(episodicNarrative) != "" {
			messages = append(messages, message{
				Role:    "user",
				Content: "Session context from your previous work session:\n" + episodicNarrative + "\n\nContinue from where you left off if relevant to the current task.",
			})
			messages = append(messages, message{
				Role:    "assistant",
				Content: "Understood, I have context from my previous session.",
			})
		}
		messages = append(messages, history...)
		if deps.intuition {
			if intuitionCtx := fetchIntuitiveContext(deps.memoryURL, deps.agentName, content); intuitionCtx != "" {
				messages = append(messages, message{Role: "user", Content: intuitionCtx})
				messages = append(messages, message{Role: "assistant", Content: "Noted, I'll keep that context in mind."})
				logJSON("info", "intuitive recall injected", map[string]interface{}{"agent": deps.agentName, "session_id": sessionID, "memories": strings.Count(intuitionCtx, "\n- ") + 1})
			}
		}
		messages = append(messages, message{Role: "user", Content: content})

		shouldRetryTool := func(name string) bool {
			switch name {
			case "delegate", "broadcast", "recall", "remember", "task_status", "task_result":
				return false
			default:
				return true
			}
		}

		executeOneToolCall := func(tc toolCall) toolpkg.ToolResult {
			atomic.AddInt64(&toolCalls, 1)
			atomic.AddInt64(&metricsToolCallsTotal, 1)
			incToolMetric(tc.Function.Name)
			hadToolCalls = true
			args := map[string]interface{}{}
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				args = map[string]interface{}{"_raw": tc.Function.Arguments}
			}
			logJSON("info", "tool called", map[string]interface{}{"tool": tc.Function.Name})
			if !emit(sseChunk{Type: "tool_call", Tool: tc.Function.Name, Args: args, Done: false}) {
				return toolpkg.ToolResult{Error: "stream closed"}
			}
			typedEventContext := toolpkg.CaptureTypedToolEventContext(tc.Function.Name, args, deps.workspace, deps.agentName)
			for _, event := range toolpkg.TypedToolStartEvents(tc.Function.Name, args, deps.workspace, typedEventContext) {
				if !emit(event) {
					return toolpkg.ToolResult{Error: "stream closed"}
				}
			}

			runToolAttempt := func() toolpkg.ToolResult {
				result := toolpkg.ToolResult{Error: "unknown tool: " + tc.Function.Name}
				switch tc.Function.Name {
				case "delegate":
					args["_session_id"] = sessionID
					if strings.TrimSpace(taskID) != "" {
						args["_task_id"] = taskID
					}
					targetAgent := strings.TrimSpace(fmt.Sprint(args["agent"]))
					if targetAgent != "" {
						notifyGateway(deps.gatewayURL, "delegation_started", deps.agentName, map[string]interface{}{
							"target_agent": targetAgent,
							"session_id":   sessionID,
						})
					}
					result = delegateExecValue.Execute(args)
					if targetAgent != "" {
						notifyGateway(deps.gatewayURL, "delegation_ended", deps.agentName, map[string]interface{}{
							"target_agent": targetAgent,
							"session_id":   sessionID,
						})
					}
				case "broadcast":
					result = broadcastExecValue.Execute(args)
				default:
					switch tc.Function.Name {
					case "recall", "todo", "read", "write", "edit":
						// read/write/edit share the M4 read-before-edit state,
						// keyed by session.
						args["_session_id"] = sessionID
					}
					if t, ok := deps.reg.Get(tc.Function.Name); ok {
						result = t.Execute(args)
					}
				}
				if verifyErr := deps.reg.VerifyResult(tc.Function.Name, args, result); verifyErr != nil {
					result = toolpkg.ToolResult{
						Output: result.Output,
						Error:  "verification failed: " + verifyErr.Error(),
					}
				}
				return result
			}

			result := runToolAttempt()
			firstAttemptVerificationFailed := strings.HasPrefix(result.Error, "verification failed: ")
			if firstAttemptVerificationFailed {
				rememberToolFailure(deps.memoryURL, deps.agentName, sessionID, tc.Function.Name, args, result, "verification_failed")
			}
			if result.Error != "" && deps.maxToolRetries > 0 && shouldRetryTool(tc.Function.Name) {
				for attempt := 1; attempt <= deps.maxToolRetries; attempt++ {
					sessionsMu.Lock()
					if trackedTasks[sessionID] != nil {
						trackedTasks[sessionID].ToolRetries++
					}
					sessionsMu.Unlock()
					logJSON("warn", "tool_retry", map[string]interface{}{
						"agent":      deps.agentName,
						"session_id": sessionID,
						"task_id":    taskID,
						"tool":       tc.Function.Name,
						"attempt":    attempt,
						"error":      result.Error,
					})
					time.Sleep(2 * time.Second)
					result = runToolAttempt()
					if result.Error == "" {
						logJSON("info", "tool_recovery", map[string]interface{}{
							"agent":      deps.agentName,
							"session_id": sessionID,
							"task_id":    taskID,
							"tool":       tc.Function.Name,
							"attempt":    attempt + 1,
						})
						rememberToolRecovery(deps.memoryURL, deps.agentName, sessionID, tc.Function.Name, args, attempt+1)
						break
					}
				}
			}
			if result.Error == "" && tc.Function.Name == "git-clone" {
				repoName := strings.TrimSpace(fmt.Sprint(args["repo"]))
				if repoName != "" && repoName != "<nil>" {
					if strings.TrimSpace(deps.memoryURL) != "" {
						result = appendCloneMemoryContext(deps.memoryURL, deps.agentName, repoName, result)
					}
					if cloneTool, ok := deps.reg.Get(tc.Function.Name); ok {
						if awarenessTool, ok := cloneTool.(interface {
							AppendProjectAwareness(repo string, output string) string
						}); ok {
							result.Output = awarenessTool.AppendProjectAwareness(repoName, result.Output)
						}
					}
				}
			}
			if result.Error != "" {
				rememberToolFailure(deps.memoryURL, deps.agentName, sessionID, tc.Function.Name, args, result, "retry_exhausted")
			}
			if result.Error != "" {
				logJSON("info", "tool result", map[string]interface{}{"tool": tc.Function.Name, "success": false, "error": result.Error, "output": result.Output})
				validateContextMemoriesAsync(deps.memoryURL, sessionID, "contradiction")
			} else {
				logJSON("info", "tool result", map[string]interface{}{"tool": tc.Function.Name, "success": true})
				validateContextMemoriesAsync(deps.memoryURL, sessionID, "success")

				// Emit typed events after key tool completions
				if tc.Function.Name == "delegate" {
					targetAgent := strings.TrimSpace(fmt.Sprint(args["agent"]))
					taskBrief := strings.TrimSpace(fmt.Sprint(args["task"]))
					taskID := strings.TrimSpace(fmt.Sprint(args["_task_id"]))
					dispatchSessionID := strings.TrimSpace(fmt.Sprint(args["_session_id"]))
					notifyGateway(deps.gatewayURL, "task_dispatched", deps.agentName, map[string]interface{}{
						"target_agent": targetAgent,
						"task_id":      taskID,
						"summary":      truncateMemoryValue(taskBrief, 100),
						"session_id":   dispatchSessionID,
					})
				}
				if tc.Function.Name == "git-clone" {
					repo := strings.TrimSpace(fmt.Sprint(args["repo"]))
					if repo != "" && repo != "<nil>" {
						notifyGateway(deps.gatewayURL, "repo_cloned", deps.agentName, map[string]interface{}{
							"repo": repo,
						})
					}
				}
				if tc.Function.Name == "gitea" {
					action := strings.TrimSpace(fmt.Sprint(args["action"]))
					if action == "create-pr" {
						prHead := truncateMemoryValue(fmt.Sprint(args["head"]), 200)
						prRepo := truncateMemoryValue(fmt.Sprint(args["repo"]), 200)
						prURL := truncateMemoryValue(extractFirstURL(result.Output), 300)
						notifyGateway(deps.gatewayURL, "pr_created", deps.agentName, map[string]interface{}{
							"repo":   prRepo,
							"branch": prHead,
							"pr_url": prURL,
						})
					}
				}

				if tc.Function.Name == "write" {
					path := strings.TrimSpace(fmt.Sprint(args["path"]))
					if path != "" && path != "<nil>" {
						sessionsMu.Lock()
						if trackedTasks[sessionID] != nil {
							trackedTasks[sessionID].FilesWritten = append(trackedTasks[sessionID].FilesWritten, path)
						}
						sessionsMu.Unlock()
					}
				}
				if tc.Function.Name == "gitea" {
					action := strings.TrimSpace(fmt.Sprint(args["action"]))
					repo := truncateMemoryValue(fmt.Sprint(args["repo"]), 200)
					switch action {
					case "create-pr":
						head := truncateMemoryValue(fmt.Sprint(args["head"]), 200)
						title := truncateMemoryValue(fmt.Sprint(args["title"]), 200)
						prURL := truncateMemoryValue(extractFirstURL(result.Output), 300)
						if repo != "" && head != "" && title != "" && prURL != "" {
							rememberStructuredOutcome(deps.memoryURL, deps.agentName, sessionID, tc.Function.Name, fmt.Sprintf(
								`PR_CREATED: repo=%q branch=%q title=%q pr_url=%q`,
								repo,
								head,
								title,
								prURL,
							))
						}
						var issueOwner, issueRepoName string
						var issueNum int
						sessionsMu.Lock()
						if trackedTasks[sessionID] != nil {
							trackedTasks[sessionID].PRCreated = true
							trackedTasks[sessionID].PRUrl = prURL
							trackedTasks[sessionID].BranchName = head
							issueOwner = trackedTasks[sessionID].IssueOwner
							issueRepoName = trackedTasks[sessionID].IssueRepo
							issueNum = trackedTasks[sessionID].IssueNumber
						}
						sessionsMu.Unlock()
						if issueNum > 0 && deps.giteaTool != nil {
							go func(owner, repoName string, issueNum int, prURL string) {
								labels, err := deps.giteaTool.GetIssueLabels(owner, repoName, issueNum)
								if err != nil {
									logJSON("warn", "auto-complete: get labels failed", map[string]interface{}{"issue": issueNum, "error": err.Error()})
									labels = []string{}
								}
								newLabels := []string{"status/done"}
								for _, l := range labels {
									if !strings.HasPrefix(l, "status/") {
										newLabels = append(newLabels, l)
									}
								}
								if err := deps.giteaTool.ReplaceLabels(owner, repoName, issueNum, newLabels); err != nil {
									logJSON("warn", "auto-complete: label update failed", map[string]interface{}{"issue": issueNum, "error": err.Error()})
								}
								if err := deps.giteaTool.PostComment(owner, repoName, issueNum, fmt.Sprintf("PR delivered: %s", prURL)); err != nil {
									logJSON("warn", "auto-complete: comment failed", map[string]interface{}{"issue": issueNum, "error": err.Error()})
								}
								if err := deps.giteaTool.CloseIssue(owner, repoName, issueNum); err != nil {
									logJSON("warn", "auto-complete: close failed", map[string]interface{}{"issue": issueNum, "error": err.Error()})
								} else {
									logJSON("info", "auto-complete: issue "+fmt.Sprint(issueNum)+" closed with PR", nil)
								}
							}(issueOwner, issueRepoName, issueNum, prURL)
						}
					case "close-issue":
						issueNum := truncateMemoryValue(fmt.Sprint(args["issue"]), 50)
						if issueNum == "" || issueNum == "<nil>" {
							issueNum = truncateMemoryValue(fmt.Sprint(args["index"]), 50)
						}
						if repo != "" && issueNum != "" && issueNum != "<nil>" {
							rememberStructuredOutcome(deps.memoryURL, deps.agentName, sessionID, tc.Function.Name, fmt.Sprintf(
								`ISSUE_CLOSED: repo=%q issue=%q`,
								repo,
								issueNum,
							))
						}
					case "get-issue", "list-issues":
						issueIdx := 0
						if v, ok := args["index"]; ok {
							switch n := v.(type) {
							case float64:
								issueIdx = int(n)
							case string:
								fmt.Sscanf(strings.TrimPrefix(n, "#"), "%d", &issueIdx)
							}
						}
						if issueIdx == 0 {
							if v, ok := args["issue"]; ok {
								switch n := v.(type) {
								case float64:
									issueIdx = int(n)
								case string:
									fmt.Sscanf(strings.TrimPrefix(n, "#"), "%d", &issueIdx)
								}
							}
						}
						issueOwner := strings.TrimSpace(fmt.Sprint(args["owner"]))
						issueRepoName := strings.TrimSpace(fmt.Sprint(args["repo"]))
						if issueOwner == "" || issueOwner == "<nil>" {
							issueOwner = "kit"
						}
						if issueRepoName == "" || issueRepoName == "<nil>" {
							issueRepoName = "hirdforge-tasks"
						}
						if strings.Contains(issueRepoName, "/") {
							parts := strings.SplitN(issueRepoName, "/", 2)
							issueOwner = parts[0]
							issueRepoName = parts[1]
						}
						if issueIdx > 0 {
							sessionsMu.Lock()
							if trackedTasks[sessionID] == nil {
								trackedTasks[sessionID] = &trackedTask{}
							}
							trackedTasks[sessionID].IssueNumber = issueIdx
							trackedTasks[sessionID].IssueOwner = issueOwner
							trackedTasks[sessionID].IssueRepo = issueRepoName
							sessionsMu.Unlock()
							logJSON("info", "task tracking: issue detected", map[string]interface{}{"session_id": sessionID, "issue": issueIdx, "owner": issueOwner, "repo": issueRepoName})
						}
					}
				}
			}
			if logTool != nil {
				inputBytes, _ := json.Marshal(args)
				out := result.Output
				if result.Error != "" {
					if out != "" {
						out = result.Error + "\n" + out
					} else {
						out = result.Error
					}
				}
				logTool(taskspkg.ToolLog{Name: tc.Function.Name, Input: string(inputBytes), Output: out})
			}
			maybeRememberAction(deps.memoryURL, deps.agentName, sessionID, tc.Function.Name, args, result)
			deps.reviewTracker.UpdateFromTool(tc.Function.Name, args, result)
			for _, event := range toolpkg.TypedToolResultEvents(tc.Function.Name, args, result, deps.workspace, typedEventContext) {
				if !emit(event) {
					return result
				}
			}
			if !emit(sseChunk{Type: "tool_result", Tool: tc.Function.Name, Result: result, Done: false}) {
				return result
			}
			toolContent := result.Output
			if result.Error != "" {
				if toolContent != "" {
					toolContent = result.Error + "\n" + toolContent
				} else {
					toolContent = result.Error
				}
			}
			if !strings.HasPrefix(tc.ID, "xml_") && !strings.HasPrefix(tc.ID, "mm_") {
				messages = append(messages, message{Role: "tool", ToolCallID: tc.ID, Content: toolContent})
			}
			return result
		}

		var toolCallSignatures []string
		repeatedToolCallDetected := false
		toolErrorsExhausted := false
		// M6 wobble tracking: reanchorPending fires an immediate plan re-anchor
		// on drift signals (repetition, two consecutive tool failures, an
		// exhausted tool error, or a git-commit no-op the model might misread as
		// done). consecutiveToolFailures resets on any success.
		consecutiveToolFailures := 0
		reanchorPending := false
		executeToolCalls := func(calls []toolCall) {
			for _, tc := range calls {
				toolCallSignatures = append(toolCallSignatures, toolCallSignature(tc.Function.Name, tc.Function.Arguments))
				if !repeatedToolCallDetected && hasRepeatedToolCallLoop(toolCallSignatures, repeatedToolCallThreshold) {
					repeatedToolCallDetected = true
					reanchorPending = true
					logJSON("warn", "repeated_tool_call_loop", map[string]interface{}{
						"agent":       deps.agentName,
						"session_id":  sessionID,
						"task_id":     taskID,
						"tool":        tc.Function.Name,
						"repetitions": repeatedToolCallThreshold,
					})
				}
				result := executeOneToolCall(tc)
				if result.Error != "" {
					consecutiveToolFailures++
					if consecutiveToolFailures >= 2 {
						reanchorPending = true
					}
					if shouldRetryTool(tc.Function.Name) {
						toolErrorsExhausted = true
						reanchorPending = true
					}
				} else {
					consecutiveToolFailures = 0
					// A successful tool result can still signal drift (git-commit
					// no-op, redirected shell git, M3 edit rescue) → re-anchor.
					if toolResultSignalsWobble(tc.Function.Name, result.Output) {
						reanchorPending = true
					}
				}
			}
		}

		var toolLoopExitReason terminationReason
		var lastNoToolAssistantContent string
		var toolLoopRounds int
		// M6 re-anchoring state.
		planningReminderSent := false
		roundsSinceAnchor := 0
		for i := 0; i < deps.maxToolRounds; i++ {
			toolLoopRounds = i + 1
			// M6: keep the goal pinned. Once the task is underway, nudge the model
			// to externalize its plan (once), then re-surface that plan on a cheap
			// cadence and — harder — immediately on a wobble signal. A model that
			// never wrote a todo gets no re-anchor noise; the terminal gate below
			// still catches an early stop.
			if i > 0 {
				switch {
				case !planningReminderSent && requestRequiresCompletionSignal(content) && !sessionTodos.hasItems(sessionID):
					messages = append(messages, message{Role: "user", Content: planningReminder()})
					planningReminderSent = true
					roundsSinceAnchor = 0
				case reanchorPending || roundsSinceAnchor >= reanchorCadence:
					if reminder := reanchorReminder(sessionID); reminder != "" {
						messages = append(messages, message{Role: "user", Content: reminder})
						logJSON("info", "todo_reanchor", map[string]interface{}{
							"agent": deps.agentName, "session_id": sessionID, "task_id": taskID,
							"round": i + 1, "wobble": reanchorPending,
						})
						roundsSinceAnchor = 0
					} else {
						roundsSinceAnchor++
					}
				default:
					roundsSinceAnchor++
				}
				reanchorPending = false
			}
			if deps.maxContext > 0 && len(messages)-1 > int(float64(deps.maxContext)*0.8) {
				trimmed := progressiveTrim(messages[1:], deps.maxContext)
				messages = append([]message{messages[0]}, trimmed...)
			}
			inferenceCtx, cancel := withInferenceTimeout(ctx)
			resp, err := callOllamaNonStreamingWithContext(inferenceCtx, messages, deps.toolDefs, deps.inferenceURL, deps.model, deps.apiKey)
			cancel()
			if err != nil {
				toolLoopExitReason = classifyInferenceError(ctx.Err(), err)
				logSessionTermination(toolLoopExitReason, map[string]interface{}{
					"agent":                      deps.agentName,
					"model":                      deps.model,
					"session_id":                 sessionID,
					"task_id":                    taskID,
					"round":                      toolLoopRounds,
					"max_rounds":                 deps.maxToolRounds,
					"had_tool_calls":             hadToolCalls,
					"last_no_tool_content_chars": len(lastNoToolAssistantContent),
					"repeated_tool_call":         repeatedToolCallDetected,
					"tool_errors_exhausted":      toolErrorsExhausted,
					"error":                      err.Error(),
				})
				return "", err
			}
			if len(resp.Choices) == 0 {
				toolLoopExitReason = terminationNoActionableOutput
				break
			}
			assistant := resp.Choices[0].Message
			if len(assistant.ToolCalls) == 0 && strings.Contains(assistant.Content, "<minimax:tool_call>") {
				mmCalls, cleaned := parseMiniMaxToolCalls(assistant.Content)
				if len(mmCalls) > 0 {
					assistant.ToolCalls = mmCalls
					assistant.Content = cleaned
				}
			}
			if len(assistant.ToolCalls) == 0 {
				lastNoToolAssistantContent = strings.TrimSpace(stripThinkTags(assistant.Content))
				toolLoopExitReason = classifyModelTurn(lastNoToolAssistantContent)

				// Terminal gate (M6): the model tried to stop. It may only stop on
				// a terminal outcome — a PR, a reasoned FAILED, a justified NOOP,
				// or (failing open) a specific question. This now fires in TASK
				// mode too (the dogfood/benchmark path), and engages whenever the
				// request demands a deliverable OR the model still has an open
				// todo item. Anti-dilution: the nudge states the full outcome
				// space and re-surfaces the plan — never "just make a PR".
				gateEngaged := hadToolCalls &&
					(requestRequiresCompletionSignal(content) || sessionTodos.hasOpenItems(sessionID))
				if gateEngaged {
					logJSON("info", "completion_gate_check", map[string]interface{}{
						"agent":       deps.agentName,
						"session_id":  sessionID,
						"task_id":     taskID,
						"content_len": len(lastNoToolAssistantContent),
					})
					if !contentHasTerminalOutcome(lastNoToolAssistantContent) {
						if completionNudges.hasExhausted(sessionID) {
							lastNoToolAssistantContent = "FAILED: completion gate exhausted"
							toolLoopExitReason = terminationNoActionableOutput
							logJSON("warn", "completion_gate_failed", map[string]interface{}{
								"agent":       deps.agentName,
								"session_id":  sessionID,
								"task_id":     taskID,
								"nudges":      maxCompletionNudges,
								"todo_remain": sessionTodos.remaining(sessionID),
							})
							break
						}
						completionNudges.recordError(sessionID)
						nudgeCount := completionNudges.increment(sessionID)
						nudgeMsg := terminalNudge(sessionID)
						logJSON("info", "completion_nudge_sent", map[string]interface{}{
							"agent":       deps.agentName,
							"session_id":  sessionID,
							"task_id":     taskID,
							"nudge_count": nudgeCount,
						})
						messages = append(messages, message{Role: "assistant", Content: lastNoToolAssistantContent})
						messages = append(messages, message{Role: "user", Content: nudgeMsg})
						toolLoopExitReason = ""
						continue
					}
					logJSON("info", "completion_gate_passed", map[string]interface{}{
						"agent":      deps.agentName,
						"session_id": sessionID,
						"task_id":    taskID,
					})
				}
				break
			}
			if strings.TrimSpace(assistant.Content) != "" {
				messages = append(messages, message{Role: assistant.Role, Content: assistant.Content})
				messages = append(messages, message{Role: assistant.Role, ToolCalls: assistant.ToolCalls})
			} else {
				messages = append(messages, message{Role: assistant.Role, Content: assistant.Content, ToolCalls: assistant.ToolCalls})
			}
			executeToolCalls(assistant.ToolCalls)
			if i == deps.maxToolRounds-1 {
				toolLoopExitReason = terminationMaxTurns
				logSessionTermination(terminationMaxTurns, map[string]interface{}{
					"agent":                      deps.agentName,
					"model":                      deps.model,
					"session_id":                 sessionID,
					"task_id":                    taskID,
					"round":                      toolLoopRounds,
					"max_rounds":                 deps.maxToolRounds,
					"had_tool_calls":             hadToolCalls,
					"last_no_tool_content_chars": len(lastNoToolAssistantContent),
					"repeated_tool_call":         repeatedToolCallDetected,
					"tool_errors_exhausted":      toolErrorsExhausted,
					"todo_remain":                sessionTodos.remaining(sessionID),
				})
				_ = emit(sseChunk{Type: "content", Content: "tool call limit reached", Done: false})
			}
		}
		if toolLoopExitReason != "" && toolLoopExitReason != terminationMaxTurns {
			logSessionTermination(toolLoopExitReason, map[string]interface{}{
				"agent":                      deps.agentName,
				"model":                      deps.model,
				"session_id":                 sessionID,
				"task_id":                    taskID,
				"round":                      toolLoopRounds,
				"max_rounds":                 deps.maxToolRounds,
				"had_tool_calls":             hadToolCalls,
				"last_no_tool_content_chars": len(lastNoToolAssistantContent),
				"repeated_tool_call":         repeatedToolCallDetected,
				"tool_errors_exhausted":      toolErrorsExhausted,
				"todo_remain":                sessionTodos.remaining(sessionID),
			})
		}

		// M1: on an abnormal exit, steer the final tools-disabled turn into a
		// structured handoff report (why / done / remaining / next + a terminal
		// marker) instead of silence or a bare truncation. Carries the M6 plan.
		messages = appendAbnormalExitSummaryRequest(messages, deps.agentName, sessionID, taskID, toolLoopExitReason)

		var full strings.Builder
		hadXMLToolCalls := false
		streamIterationHadToolCalls := false
		var xmlToolResults []string
		insideThink := false
		streamCtx, cancelStream := withInferenceTimeout(ctx)
		if deps.maxContext > 0 && len(messages)-1 > int(float64(deps.maxContext)*0.8) {
			trimmed := progressiveTrim(messages[1:], deps.maxContext)
			messages = append([]message{messages[0]}, trimmed...)
		}
		for evt := range streamOllamaWithContext(streamCtx, messages, nil, deps.inferenceURL, deps.model, deps.apiKey) {
			if evt.Err != nil {
				errText := evt.Err.Error()
				full.WriteString(errText)
				if !emit(sseChunk{Type: "content", Content: errText, Done: false}) {
					cancelStream()
					logSessionTermination(terminationContextCanceled, streamTerminationFields(deps.agentName, deps.model, sessionID, taskID, "stream_error", map[string]interface{}{"stream_error": errText}))
					return full.String(), context.Canceled
				}
				break
			}
			if len(evt.ToolCalls) > 0 {
				hadXMLToolCalls = true
				streamIterationHadToolCalls = true
				for _, tc := range evt.ToolCalls {
					result := executeOneToolCall(tc)
					out := result.Output
					if result.Error != "" {
						out = "ERROR: " + result.Error
					}
					if len(out) > 500 {
						out = out[:500] + "...[truncated]"
					}
					xmlToolResults = append(xmlToolResults, fmt.Sprintf("[%s]: %s", tc.Function.Name, out))
				}
				continue
			}
			chunk := evt.Content
			cleaned := ""
			combined := chunk
			if insideThink {
				if idx := strings.Index(combined, "</think>"); idx >= 0 {
					insideThink = false
					combined = combined[idx+len("</think>"):]
				} else {
					continue
				}
			}
			if idx := strings.Index(combined, "<think>"); idx >= 0 {
				cleaned = combined[:idx]
				insideThink = true
				if end := strings.Index(combined[idx:], "</think>"); end >= 0 {
					insideThink = false
					cleaned += combined[idx+end+len("</think>"):]
				}
			} else {
				cleaned = combined
			}
			cleaned = orphanThinkRe.ReplaceAllString(cleaned, "")
			if cleaned == "" {
				continue
			}
			cleanedChunk, xmlResults := extractAndExecuteXMLToolCalls(cleaned, func(tc toolCall) ToolResult {
				result := executeOneToolCall(tc)
				out := result.Output
				if result.Error != "" {
					out = "ERROR: " + result.Error
				}
				if len(out) > 500 {
					out = out[:500] + "...[truncated]"
				}
				xmlToolResults = append(xmlToolResults, fmt.Sprintf("[%s]: %s", tc.Function.Name, out))
				return result
			})
			if len(xmlResults) > 0 {
				hadXMLToolCalls = true
			}
			if cleanedChunk == "" {
				continue
			}
			full.WriteString(cleanedChunk)
			if !emit(sseChunk{Type: "content", Content: cleanedChunk, Done: false}) {
				cancelStream()
				logSessionTermination(terminationContextCanceled, streamTerminationFields(deps.agentName, deps.model, sessionID, taskID, "emit_content", nil))
				return full.String(), context.Canceled
			}
		}
		cancelStream()
		finalContent := full.String()
		if strings.TrimSpace(finalContent) == "" && strings.TrimSpace(lastNoToolAssistantContent) != "" {
			logJSON("info", "tool_loop_content_fallback", map[string]interface{}{
				"agent":      deps.agentName,
				"model":      deps.model,
				"session_id": sessionID,
				"task_id":    taskID,
				"chars":      len(lastNoToolAssistantContent),
			})
			full.Reset()
			full.WriteString(lastNoToolAssistantContent)
			if !emit(sseChunk{Type: "content", Content: lastNoToolAssistantContent, Done: false}) {
				logSessionTermination(terminationContextCanceled, streamTerminationFields(deps.agentName, deps.model, sessionID, taskID, "emit_fallback", nil))
				return lastNoToolAssistantContent, context.Canceled
			}
			finalContent = lastNoToolAssistantContent
		}
		if strings.TrimSpace(finalContent) == "" && !streamIterationHadToolCalls {
			logJSON("warn", "stall", map[string]interface{}{
				"agent":      deps.agentName,
				"model":      deps.model,
				"session_id": sessionID,
				"task_id":    taskID,
			})
			atomic.AddInt64(&metricsStallsTotal, 1)
			rememberToolFailure(deps.memoryURL, deps.agentName, sessionID, "inference", map[string]interface{}{}, toolpkg.ToolResult{Error: "no output produced"}, "stall")

			// Stall recovery: trim context aggressively and retry once
			originalMsgCount := len(messages) - 1
			trimmed := progressiveTrim(messages[1:], deps.maxContext/2)
			messages = append([]message{messages[0]}, trimmed...)
			newMsgCount := len(messages) - 1
			logJSON("info", "stall_retry", map[string]interface{}{
				"agent":         deps.agentName,
				"session_id":    sessionID,
				"task_id":       taskID,
				"original_msgs": originalMsgCount,
				"trimmed_msgs":  newMsgCount,
			})

			// Retry streaming with trimmed context
			full.Reset()
			xmlToolResults = nil
			hadXMLToolCalls = false
			streamIterationHadToolCalls = false
			streamCtx2, cancelStream2 := withInferenceTimeout(ctx)
			resp2 := streamResponsesWithContext(streamCtx2, messages, deps.toolDefs, deps.inferenceURL, deps.model, deps.apiKey)
			insideThink := false
			for resp2 != nil {
				select {
				case event, ok := <-resp2:
					if !ok {
						resp2 = nil
						break
					}
					if event.Err != nil {
						logJSON("warn", "stall_retry_error", map[string]interface{}{
							"agent":      deps.agentName,
							"session_id": sessionID,
							"task_id":    taskID,
							"error":      event.Err.Error(),
						})
						resp2 = nil
						break
					}
					for _, tc := range event.ToolCalls {
						cleanedChunk, xmlResults := extractAndExecuteXMLToolCalls(
							fmt.Sprintf("<minimax:tool_call>\n<invoke name=%q>\n<parameter name=%q>%s</parameter>\n</invoke>\n</minimax:tool_call>", tc.Function.Name, "name", tc.Function.Arguments),
							func(tc toolCall) ToolResult {
								result := executeOneToolCall(tc)
								out := result.Output
								if result.Error != "" {
									out = "ERROR: " + result.Error
								}
								if len(out) > 500 {
									out = out[:500] + "...[truncated]"
								}
								xmlToolResults = append(xmlToolResults, fmt.Sprintf("[%s]: %s", tc.Function.Name, out))
								return result
							})
						if len(xmlResults) > 0 {
							hadXMLToolCalls = true
						}
						if cleanedChunk == "" {
							continue
						}
						full.WriteString(cleanedChunk)
						if !emit(sseChunk{Type: "content", Content: cleanedChunk, Done: false}) {
							cancelStream2()
							resp2 = nil
							break
						}
					}
					if event.Content != "" {
						combined := event.Content
						if strings.Contains(combined, "<think>") {
							idx := strings.Index(combined, "<think>")
							insideThink = true
							cleaned := combined[:idx]
							if end := strings.Index(combined[idx:], "</minimax:tool_call>"); end >= 0 {
								insideThink = false
								cleaned += combined[idx+end+len("</minimax:tool_call>"):]
							}
							combined = cleaned
						} else if insideThink {
							continue
						} else if end := strings.Index(combined, "</minimax:tool_call>"); end >= 0 {
							insideThink = false
							combined = combined[end+len("</minimax:tool_call>"):]
						}
						combined = orphanThinkRe.ReplaceAllString(combined, "")
						if combined == "" {
							continue
						}
						_, xmlResults := extractAndExecuteXMLToolCalls(combined, func(tc toolCall) ToolResult {
							result := executeOneToolCall(tc)
							out := result.Output
							if result.Error != "" {
								out = "ERROR: " + result.Error
							}
							if len(out) > 500 {
								out = out[:500] + "...[truncated]"
							}
							xmlToolResults = append(xmlToolResults, fmt.Sprintf("[%s]: %s", tc.Function.Name, out))
							return result
						})
						if len(xmlResults) > 0 {
							hadXMLToolCalls = true
						}
						if combined == "" {
							continue
						}
						full.WriteString(combined)
						if !emit(sseChunk{Type: "content", Content: combined, Done: false}) {
							cancelStream2()
							resp2 = nil
							break
						}
					}
				case <-streamCtx2.Done():
					resp2 = nil
					continue
				}
			}
			cancelStream2()
			finalContent = full.String()
			if strings.TrimSpace(finalContent) == "" && strings.TrimSpace(lastNoToolAssistantContent) != "" {
				logJSON("info", "tool_loop_content_fallback", map[string]interface{}{
					"agent":      deps.agentName,
					"model":      deps.model,
					"session_id": sessionID,
					"task_id":    taskID,
					"chars":      len(lastNoToolAssistantContent),
				})
				full.Reset()
				full.WriteString(lastNoToolAssistantContent)
				if !emit(sseChunk{Type: "content", Content: lastNoToolAssistantContent, Done: false}) {
					logSessionTermination(terminationContextCanceled, streamTerminationFields(deps.agentName, deps.model, sessionID, taskID, "emit_fallback_recovery", nil))
					return lastNoToolAssistantContent, context.Canceled
				}
				finalContent = lastNoToolAssistantContent
			}
			if strings.TrimSpace(finalContent) == "" && !hadXMLToolCalls {
				logSessionTermination(terminationStallFatal, map[string]interface{}{
					"agent":      deps.agentName,
					"model":      deps.model,
					"session_id": sessionID,
					"task_id":    taskID,
				})
				atomic.AddInt64(&metricsStallsTotal, 1)
			}
		}
		if strings.Contains(finalContent, "<minimax:tool_call>") {
			cleanedFinal, postResults := extractAndExecuteXMLToolCalls(finalContent, func(tc toolCall) ToolResult {
				result := executeOneToolCall(tc)
				out := result.Output
				if result.Error != "" {
					out = "ERROR: " + result.Error
				}
				if len(out) > 500 {
					out = out[:500] + "...[truncated]"
				}
				xmlToolResults = append(xmlToolResults, fmt.Sprintf("[%s]: %s", tc.Function.Name, out))
				return result
			})
			if len(postResults) > 0 {
				hadXMLToolCalls = true
				full.Reset()
				full.WriteString(cleanedFinal)
				if !emit(sseChunk{Type: "replace", Content: cleanedFinal, Done: false}) {
					logSessionTermination(terminationContextCanceled, streamTerminationFields(deps.agentName, deps.model, sessionID, taskID, "emit_replace", nil))
					return cleanedFinal, context.Canceled
				}
			}
		}
		cleaned := strings.TrimSpace(full.String())
		if cleaned != "" {
			messages = append(messages, message{Role: "assistant", Content: cleaned})
		}
		if hadXMLToolCalls && len(xmlToolResults) > 0 {
			resultMsg := "Tool execution results:\n\n" + strings.Join(xmlToolResults, "\n\n")
			messages = append(messages, message{Role: "user", Content: resultMsg})
			for i := 0; i < 3; i++ {
				if deps.maxContext > 0 && len(messages)-1 > int(float64(deps.maxContext)*0.8) {
					trimmed := progressiveTrim(messages[1:], deps.maxContext)
					messages = append([]message{messages[0]}, trimmed...)
				}
				inferenceCtx, cancel := withInferenceTimeout(ctx)
				resp, err := callOllamaNonStreamingWithContext(inferenceCtx, messages, deps.toolDefs, deps.inferenceURL, deps.model, deps.apiKey)
				cancel()
				if err != nil {
					reason := classifyInferenceError(ctx.Err(), err)
					logSessionTermination(reason, streamTerminationFields(deps.agentName, deps.model, sessionID, taskID, "xml_followup_inference", map[string]interface{}{"error": err.Error()}))
					return cleaned, err
				}
				if len(resp.Choices) == 0 {
					break
				}
				assistant := resp.Choices[0].Message
				assistant.Content = stripThinkTags(assistant.Content)
				if len(assistant.ToolCalls) == 0 && strings.Contains(assistant.Content, "<minimax:tool_call>") {
					mmCalls, mmCleaned := parseMiniMaxToolCalls(assistant.Content)
					if len(mmCalls) > 0 {
						assistant.ToolCalls = mmCalls
						assistant.Content = mmCleaned
					}
				}
				if len(assistant.ToolCalls) > 0 {
					messages = append(messages, message{Role: assistant.Role, Content: assistant.Content, ToolCalls: assistant.ToolCalls})
					executeToolCalls(assistant.ToolCalls)
					continue
				}
				chunkContent, xmlResults := extractAndExecuteXMLToolCalls(assistant.Content, func(tc toolCall) ToolResult {
					result := executeOneToolCall(tc)
					out := result.Output
					if result.Error != "" {
						out = "ERROR: " + result.Error
					}
					if len(out) > 500 {
						out = out[:500] + "...[truncated]"
					}
					xmlToolResults = append(xmlToolResults, fmt.Sprintf("[%s]: %s", tc.Function.Name, out))
					return result
				})
				if chunkContent != "" {
					full.WriteString(chunkContent)
					if !emit(sseChunk{Type: "content", Content: chunkContent, Done: false}) {
						logSessionTermination(terminationContextCanceled, streamTerminationFields(deps.agentName, deps.model, sessionID, taskID, "emit_xml_followup", nil))
						return full.String(), context.Canceled
					}
					messages = append(messages, message{Role: "assistant", Content: chunkContent})
				}
				if len(xmlResults) == 0 {
					break
				}
				resultMsg = "Tool execution results:\n\n" + strings.Join(xmlToolResults, "\n\n")
				messages = append(messages, message{Role: "user", Content: resultMsg})
			}
			cleaned = strings.TrimSpace(full.String())
		}

		sessionsMu.Lock()
		sessions[sessionID] = append(sessions[sessionID], message{Role: "user", Content: content}, message{Role: "assistant", Content: cleaned})
		sessionsMu.Unlock()
		if deps.episodic && deps.memoryURL != "" && hadToolCalls {
			sessionsMu.Lock()
			currentHistory := append([]message(nil), sessions[sessionID]...)
			sessionsMu.Unlock()
			go persistEpisodicState(deps.memoryURL, deps.agentName, sessionID, currentHistory, deps.agentName)
		}
		return cleaned, nil
	}
}

func registerSessionRoutes(mux *http.ServeMux, deps serverDeps) {
	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
			if sessionID == "" {
				http.Error(w, "missing session_id", http.StatusBadRequest)
				return
			}
			sessionsMu.Lock()
			delete(sessions, sessionID)
			delete(seenSessions, sessionID)
			delete(sessionContextMemoryIDs, sessionID)
			delete(sessionValidatedIDs, sessionID)
			if deps.episodic {
				delete(episodicStates, sessionID)
			}
			delete(trackedTasks, sessionID)
			sessionsMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"deleted":    true,
				"session_id": sessionID,
			})
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sessionsMu.Lock()
		ids := make([]string, 0, len(sessions))
		for id := range sessions {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		out := make([]map[string]interface{}, 0, len(ids))
		for _, id := range ids {
			out = append(out, map[string]interface{}{"session_id": id, "messages": len(sessions[id])})
		}
		sessionsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	mux.HandleFunc("/message", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		startReq := time.Now()
		atomic.AddInt64(&requestCount, 1)
		atomic.AddInt64(&metricsRequestsTotal, 1)
		atomic.AddInt64(&metricsActiveRequests, 1)
		defer func() {
			atomic.AddInt64(&metricsActiveRequests, -1)
			setLastDuration(time.Since(startReq))
		}()
		logJSON("info", "request received", nil)
		var req messageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			incError("invalid request body", err, nil)
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		sessionID := req.SessionID
		if sessionID == "" {
			sessionID = fmt.Sprintf("%x", rand.Int63())
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		legacyEmit := func(chunk interface{}) bool {
			writeSSE(w, chunk)
			flusher.Flush()
			return true
		}
		a2aTaskID := "session-" + sessionID
		emit := func(chunk interface{}) bool {
			if !legacyEmit(chunk) {
				return false
			}
			return emitA2AStatusSSE(legacyEmit, a2aTaskID, chunk)
		}
		result, err := deps.processConversation(r.Context(), sessionID, "", req.Content, emit, nil)
		if err != nil {
			incError("message processing failed", err, nil)
			writeSSE(w, sseChunk{Type: "content", Content: err.Error(), Done: false})
		} else {
			_ = emitA2AArtifactSSE(legacyEmit, a2aTaskID, result)
		}
		writeSSE(w, sseChunk{Type: "done", Done: true, SessionID: sessionID})
		flusher.Flush()
	})
}
