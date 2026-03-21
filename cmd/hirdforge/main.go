package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/chzyer/readline"
)

const (
	colorReset = "\033[0m"
	colorGreen = "\033[32m"
	colorRed   = "\033[31m"
	colorDim   = "\033[90m"
)

type agentInfo struct {
	Name    string `json:"name"`
	Model   string `json:"model"`
	Healthy bool   `json:"healthy"`
}

type agentState struct {
	Name    string `json:"name"`
	Active  bool   `json:"active"`
	TaskRef string `json:"task_ref"`
}

type approvalsResponse struct {
	Queues []struct {
		QueueID string                 `json:"queue_id"`
		Action  string                 `json:"action"`
		Status  string                 `json:"status"`
		Params  map[string]interface{} `json:"params"`
	} `json:"queues"`
}

type prInfo struct {
	Number    int64  `json:"number"`
	Title     string `json:"title"`
	State     string `json:"state"`
	User      string `json:"user"`
	Repo      string `json:"repo"`
	Base      string `json:"base"`
	Head      string `json:"head"`
	Approvals int64  `json:"approvals"`
	Mergeable bool   `json:"mergeable"`
}

type podInfo struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Restarts  int64  `json:"restarts"`
	Age       string `json:"age"`
	Namespace string `json:"namespace"`
}

type nodeInfo struct {
	Name          string  `json:"name"`
	Status        string  `json:"status"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryPercent float64 `json:"memory_percent"`
}

type gitopsStatus struct {
	App          string `json:"app"`
	SyncStatus   string `json:"sync_status"`
	HealthStatus string `json:"health_status"`
	DeployedSHA  string `json:"deployed_sha"`
	GitHeadSHA   string `json:"git_head_sha"`
}

type podLogsResponse struct {
	Pod  string `json:"pod"`
	Logs string `json:"logs"`
}

type historyTasksResponse struct {
	Tasks []struct {
		ID       int64    `json:"id"`
		Title    string   `json:"title"`
		Agents   []string `json:"agents"`
		Outcome  string   `json:"outcome"`
		PRNumber int64    `json:"pr_number"`
		ClosedAt string   `json:"closed_at"`
	} `json:"tasks"`
}

type config struct {
	URL string
}

type app struct {
	url       string
	cluster   string
	client    *http.Client
	agents    []agentInfo
	fleet     []agentState
	prs       []prInfo
	completer *dynamicCompleter
}

type dynamicCompleter struct {
	app *app
}

func main() {
	urlFlag := flag.String("url", "", "Gateway URL")
	flag.Parse()

	cfg := loadConfig(strings.TrimSpace(*urlFlag))
	a := &app{
		url:     strings.TrimRight(cfg.URL, "/"),
		cluster: "Valhalla",
		client:  &http.Client{Timeout: 60 * time.Second},
	}
	if err := a.refreshStartup(); err != nil {
		printError(err.Error())
		os.Exit(1)
	}

	fmt.Printf("Hirdforge v1.0 · %s cluster connected\n", a.cluster)
	fmt.Printf("%d agents online · %d active · %d awaiting sovereign\n\n", countOnline(a.agents), countActive(a.fleet), countAwaiting(a))

	rl, err := readline.NewEx(&readline.Config{
		Prompt:       "hirdforge> ",
		HistoryLimit: 200,
		AutoComplete: &dynamicCompleter{app: a},
	})
	if err != nil {
		printError(err.Error())
		os.Exit(1)
	}
	defer rl.Close()
	a.completer = rl.Config.AutoComplete.(*dynamicCompleter)

	if err := a.repl(rl, ""); err != nil && !errors.Is(err, io.EOF) {
		printError(err.Error())
		os.Exit(1)
	}
}

func loadConfig(flagURL string) config {
	if flagURL != "" {
		return config{URL: flagURL}
	}
	if env := strings.TrimSpace(os.Getenv("HIRDFORGE_URL")); env != "" {
		return config{URL: env}
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		path := filepath.Join(home, ".hirdforge", "config")
		if data, err := os.ReadFile(path); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "url:") {
					if value := strings.TrimSpace(strings.TrimPrefix(line, "url:")); value != "" {
						return config{URL: value}
					}
				}
			}
		}
	}
	return config{URL: "http://localhost:8080"}
}

func (a *app) repl(rl *readline.Instance, chatAgent string) error {
	for {
		if chatAgent != "" {
			rl.SetPrompt(chatAgent + "> ")
		} else {
			rl.SetPrompt("hirdforge> ")
		}
		line, err := rl.Readline()
		if errors.Is(err, readline.ErrInterrupt) {
			if chatAgent != "" {
				fmt.Println()
				return nil
			}
			fmt.Println()
			return nil
		}
		if errors.Is(err, io.EOF) {
			fmt.Println()
			return nil
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "kubectl ") || line == "kubectl" {
			if err := runKubectl(line); err != nil {
				printError(err.Error())
			}
			continue
		}
		if chatAgent != "" {
			switch line {
			case "exit", "quit":
				return nil
			default:
				if err := a.streamMessage(chatAgent, line); err != nil {
					printError(err.Error())
				}
				continue
			}
		}
		if line == "exit" || line == "quit" {
			return nil
		}
		if err := a.runCommand(rl, line); err != nil {
			printError(err.Error())
		}
	}
}

func (a *app) runCommand(rl *readline.Instance, line string) error {
	switch {
	case line == "clear":
		fmt.Print("\033[H\033[2J")
	case line == "help":
		printHelp()
	case line == "config":
		fmt.Printf("URL:\t%s\nCluster:\t%s\n", a.url, a.cluster)
	case line == "status":
		return a.printStatus()
	case line == "agents":
		return a.printAgents()
	case strings.HasPrefix(line, "delegate "):
		agent, task := parseAgentAndTail(strings.TrimPrefix(line, "delegate "))
		if agent == "" || task == "" {
			return fmt.Errorf("usage: delegate <agent> \"<task>\"")
		}
		return a.streamMessage(agent, task)
	case strings.HasPrefix(line, "chat "):
		agent := strings.TrimSpace(strings.TrimPrefix(line, "chat "))
		if agent == "" {
			return fmt.Errorf("usage: chat <agent>")
		}
		return a.repl(rl, agent)
	case line == "prs":
		return a.printPRs()
	case strings.HasPrefix(line, "prs merge "):
		return a.mergePRInteractive(strings.TrimSpace(strings.TrimPrefix(line, "prs merge ")))
	case line == "approve":
		return a.printApprovals()
	case strings.HasPrefix(line, "approve "):
		return a.decideApproval(strings.TrimSpace(strings.TrimPrefix(line, "approve ")), true)
	case strings.HasPrefix(line, "reject "):
		return a.decideApproval(strings.TrimSpace(strings.TrimPrefix(line, "reject ")), false)
	case line == "cluster":
		return a.printCluster()
	case line == "pods":
		return a.printPods()
	case strings.HasPrefix(line, "pods logs "):
		return a.printPodLogs(strings.TrimSpace(strings.TrimPrefix(line, "pods logs ")))
	case strings.HasPrefix(line, "pods restart "):
		return a.restartPodInteractive(strings.TrimSpace(strings.TrimPrefix(line, "pods restart ")))
	case line == "history":
		return a.printHistory()
	default:
		printHelp()
	}
	return nil
}

func (a *app) refreshStartup() error {
	if err := a.getJSON("/api/v1/agents", &a.agents); err != nil {
		return fmt.Errorf("failed to connect to gateway: %w", err)
	}
	_ = a.getJSON("/api/v1/fleet/state", &a.fleet)
	_ = a.getJSON("/api/v1/gitea/prs", &a.prs)
	return nil
}

func (a *app) getJSON(path string, out interface{}) error {
	resp, err := a.client.Get(a.url + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (a *app) postJSON(path string, body interface{}, out interface{}) error {
	payload, _ := json.Marshal(body)
	resp, err := a.client.Post(a.url+path, "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (a *app) deleteJSON(path string, out interface{}) error {
	req, err := http.NewRequest(http.MethodDelete, a.url+path, nil)
	if err != nil {
		return err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (a *app) printStatus() error {
	if err := a.getJSON("/api/v1/agents", &a.agents); err != nil {
		return err
	}
	if err := a.getJSON("/api/v1/fleet/state", &a.fleet); err != nil {
		return err
	}
	var approvals approvalsResponse
	if err := a.getJSON("/api/v1/approvals", &approvals); err != nil {
		return err
	}
	fmt.Printf("Online: %d\tActive: %d\tAwaiting sovereign: %d\n\n", countOnline(a.agents), countActive(a.fleet), len(approvals.Queues))
	if err := a.printAgents(); err != nil {
		return err
	}
	fmt.Println()
	return a.printApprovals()
}

func (a *app) printAgents() error {
	if err := a.getJSON("/api/v1/agents", &a.agents); err != nil {
		return err
	}
	_ = a.getJSON("/api/v1/fleet/state", &a.fleet)
	stateByAgent := map[string]agentState{}
	for _, item := range a.fleet {
		stateByAgent[item.Name] = item
	}
	tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tSTATUS\tMODEL\tCURRENT TASK")
	for _, agent := range a.agents {
		st := stateByAgent[agent.Name]
		task := st.TaskRef
		if task == "" {
			task = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", agent.Name, statusDot(agent.Healthy), agent.Model, task)
	}
	return tw.Flush()
}

func (a *app) printPRs() error {
	if err := a.getJSON("/api/v1/gitea/prs", &a.prs); err != nil {
		return err
	}
	sort.Slice(a.prs, func(i, j int) bool { return a.prs[i].Number < a.prs[j].Number })
	tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PR\tREPO\tAUTHOR\tAPPROVALS\tTITLE")
	for _, pr := range a.prs {
		if pr.State != "open" {
			continue
		}
		fmt.Fprintf(tw, "#%d\t%s\t%s\t%d\t%s\n", pr.Number, pr.Repo, pr.User, pr.Approvals, pr.Title)
	}
	return tw.Flush()
}

func (a *app) printApprovals() error {
	var approvals approvalsResponse
	if err := a.getJSON("/api/v1/approvals", &approvals); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tACTION\tDETAILS")
	for _, item := range approvals.Queues {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", item.QueueID, item.Status, item.Action, summarizeParams(item.Params))
	}
	return tw.Flush()
}

func (a *app) decideApproval(id string, approved bool) error {
	path := "/api/v1/approvals/approve"
	if !approved {
		path = "/api/v1/approvals/reject"
	}
	if err := a.postJSON(path, map[string]string{"queue_id": id}, nil); err != nil {
		return err
	}
	printOK(fmt.Sprintf("%s %s", map[bool]string{true: "approved", false: "rejected"}[approved], id))
	return nil
}

func (a *app) printCluster() error {
	var nodes []nodeInfo
	var gitops gitopsStatus
	if err := a.getJSON("/api/v1/cluster/nodes", &nodes); err != nil {
		return err
	}
	_ = a.getJSON("/api/v1/gitops/status", &gitops)
	tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NODE\tSTATUS\tCPU\tMEM")
	for _, node := range nodes {
		fmt.Fprintf(tw, "%s\t%s\t%.0f%%\t%.0f%%\n", node.Name, node.Status, node.CPUPercent, node.MemoryPercent)
	}
	_ = tw.Flush()
	fmt.Printf("\nGitOps:\t%s / %s\t%s -> %s\n", gitops.SyncStatus, gitops.HealthStatus, gitops.DeployedSHA, gitops.GitHeadSHA)
	return nil
}

func (a *app) printPods() error {
	var pods []podInfo
	if err := a.getJSON("/api/v1/cluster/pods", &pods); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "POD\tSTATUS\tRESTARTS\tAGE")
	for _, pod := range pods {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", pod.Name, pod.Status, pod.Restarts, pod.Age)
	}
	return tw.Flush()
}

func (a *app) printPodLogs(name string) error {
	var out podLogsResponse
	if err := a.getJSON("/api/v1/cluster/pods/valhalla/"+name+"/logs?tail=50", &out); err != nil {
		return err
	}
	fmt.Println(out.Logs)
	return nil
}

func (a *app) printHistory() error {
	var out historyTasksResponse
	if err := a.getJSON("/api/v1/history/tasks?limit=20", &out); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tOUTCOME\tAGENTS\tPR\tTITLE")
	for _, task := range out.Tasks {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\n", task.ID, task.Outcome, strings.Join(task.Agents, ","), task.PRNumber, task.Title)
	}
	return tw.Flush()
}

func (a *app) mergePRInteractive(raw string) error {
	num, err := strconv.ParseInt(strings.TrimPrefix(raw, "#"), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid PR number")
	}
	if len(a.prs) == 0 {
		_ = a.getJSON("/api/v1/gitea/prs", &a.prs)
	}
	var target *prInfo
	for i := range a.prs {
		if a.prs[i].Number == num && a.prs[i].State == "open" {
			target = &a.prs[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("PR #%d not found", num)
	}
	if !confirm(fmt.Sprintf("Merge PR #%d: %s? [y/N] ", num, target.Title)) {
		return nil
	}
	owner, repo, ok := strings.Cut(target.Repo, "/")
	if !ok {
		return fmt.Errorf("invalid repo %q", target.Repo)
	}
	if err := a.postJSON(fmt.Sprintf("/api/v1/gitea/prs/%s/%s/%d/merge", owner, repo, num), map[string]string{}, nil); err != nil {
		return err
	}
	printOK(fmt.Sprintf("merged PR #%d", num))
	return nil
}

func (a *app) restartPodInteractive(name string) error {
	if !confirm(fmt.Sprintf("Restart pod %s? [y/N] ", name)) {
		return nil
	}
	var out map[string]interface{}
	if err := a.deleteJSON("/api/v1/cluster/pods/valhalla/"+name, &out); err != nil {
		return err
	}
	printOK(fmt.Sprintf("restarted %s", name))
	return nil
}

func (a *app) streamMessage(agent, content string) error {
	sessionID := fmt.Sprintf("hirdforge-%d", time.Now().UnixNano())
	payload, _ := json.Marshal(map[string]string{"agent": agent, "content": content, "session_id": sessionID})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url+"/api/v1/message", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	reader := bufio.NewScanner(resp.Body)
	reader.Buffer(make([]byte, 0, 4096), 1024*1024)
	for reader.Scan() {
		line := strings.TrimSpace(reader.Text())
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			continue
		}
		switch event["type"] {
		case "content":
			fmt.Print(asStringAny(event["content"]))
			_ = os.Stdout.Sync()
		case "replace":
			fmt.Print("\r" + asStringAny(event["content"]))
			_ = os.Stdout.Sync()
		case "tool_call":
			fmt.Printf("\n%s  · %s %v%s\n", colorDim, asStringAny(event["tool"]), event["args"], colorReset)
		case "tool_result":
			fmt.Printf("\n%s  · result %s%s\n", colorDim, truncate(asStringAny(event["result"]), 120), colorReset)
		}
		if done, _ := event["done"].(bool); done {
			fmt.Println()
			return nil
		}
	}
	if err := reader.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	fmt.Println()
	return nil
}

func (d *dynamicCompleter) Do(line []rune, pos int) ([][]rune, int) {
	prefix := string(line[:pos])
	fields := strings.Fields(prefix)
	commands := []string{"status", "agents", "delegate", "chat", "prs", "approve", "reject", "cluster", "pods", "history", "config", "clear", "help", "exit", "quit", "kubectl"}
	candidates := commands
	if len(fields) >= 1 {
		switch fields[0] {
		case "chat", "delegate":
			candidates = d.agentNames()
		case "prs":
			if len(fields) >= 2 && fields[1] == "merge" {
				candidates = d.prNumbers()
			} else {
				candidates = []string{"merge"}
			}
		case "approve", "reject":
			candidates = []string{}
		case "pods":
			if len(fields) == 1 {
				candidates = []string{"logs", "restart"}
			}
		}
	}
	target := ""
	if len(fields) > 0 {
		target = fields[len(fields)-1]
		if strings.HasSuffix(prefix, " ") {
			target = ""
		}
	}
	out := make([][]rune, 0, len(candidates))
	for _, candidate := range candidates {
		if strings.HasPrefix(candidate, target) {
			out = append(out, []rune(candidate))
		}
	}
	return out, len(target)
}

func (d *dynamicCompleter) agentNames() []string {
	out := make([]string, 0, len(d.app.agents))
	for _, agent := range d.app.agents {
		out = append(out, agent.Name)
	}
	sort.Strings(out)
	return out
}

func (d *dynamicCompleter) prNumbers() []string {
	out := make([]string, 0, len(d.app.prs))
	for _, pr := range d.app.prs {
		if pr.State == "open" {
			out = append(out, strconv.FormatInt(pr.Number, 10))
		}
	}
	sort.Strings(out)
	return out
}

func runKubectl(line string) error {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return fmt.Errorf("kubectl not found on PATH — install kubectl or use 'hirdforge cluster' for basic cluster info")
	}
	args := strings.Fields(line)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func parseAgentAndTail(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	parts := strings.Fields(raw)
	if len(parts) < 2 {
		return "", ""
	}
	agent := parts[0]
	tail := strings.TrimSpace(strings.TrimPrefix(raw, agent))
	tail = strings.Trim(strings.TrimSpace(tail), `"`)
	return agent, tail
}

func countOnline(agents []agentInfo) int {
	n := 0
	for _, agent := range agents {
		if agent.Healthy {
			n++
		}
	}
	return n
}

func countActive(fleet []agentState) int {
	n := 0
	for _, item := range fleet {
		if item.Active {
			n++
		}
	}
	return n
}

func countAwaiting(a *app) int {
	var approvals approvalsResponse
	if err := a.getJSON("/api/v1/approvals", &approvals); err != nil {
		return 0
	}
	return len(approvals.Queues)
}

func statusDot(ok bool) string {
	if ok {
		return colorGreen + "●" + colorReset
	}
	return colorRed + "●" + colorReset
}

func asStringAny(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func summarizeParams(params map[string]interface{}) string {
	b, _ := json.Marshal(params)
	return truncate(string(b), 80)
}

func confirm(prompt string) bool {
	fmt.Fprint(os.Stderr, prompt)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y")
}

func printError(msg string) {
	fmt.Fprintf(os.Stderr, "%s[error]%s %s\n", colorRed, colorReset, msg)
}

func printOK(msg string) {
	fmt.Printf("%s[ok]%s %s\n", colorGreen, colorReset, msg)
}

func printHelp() {
	fmt.Println("Commands:")
	fmt.Println("  status")
	fmt.Println("  agents")
	fmt.Println("  delegate <agent> \"<task>\"")
	fmt.Println("  chat <agent>")
	fmt.Println("  prs")
	fmt.Println("  prs merge <number>")
	fmt.Println("  approve")
	fmt.Println("  approve <id>")
	fmt.Println("  reject <id>")
	fmt.Println("  cluster")
	fmt.Println("  pods")
	fmt.Println("  pods logs <name>")
	fmt.Println("  pods restart <name>")
	fmt.Println("  history")
	fmt.Println("  config")
	fmt.Println("  clear")
	fmt.Println("  kubectl <args>")
	fmt.Println("  help")
	fmt.Println("  exit | quit")
}
