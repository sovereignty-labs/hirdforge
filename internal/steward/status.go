package steward

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// TaskView is the projected, read-only view of one task the interlocutor can
// cite. It is a COPY of the observability surface — the cortex task plus its
// latest mechanical transition reason — never the model's recollection (§6, §7).
// The gateway maps live cortex records into this; tests inject fixtures. The
// interlocutor decides nothing from it; it only reports it.
type TaskView struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Status  string    `json:"status"` // queued|dispatched|building|review|approved|merged|validated|failed
	Repo    string    `json:"repo"`
	Issue   int64     `json:"issue,omitempty"`
	PR      int64     `json:"pr,omitempty"`
	Reason  string    `json:"reason"` // the latest transition's mechanical reason, quoted VERBATIM
	Updated time.Time `json:"updated"`
}

// terminalStatuses are the done states — a task in one of these is not "running".
var terminalStatuses = map[string]bool{"validated": true, "failed": true}

// Active reports whether the task is still in flight (not a terminal state).
func (t TaskView) Active() bool { return !terminalStatuses[t.Status] }

// ref renders a stable citation for a task: its id, and the issue/PR it concerns.
func (t TaskView) ref() string {
	var b strings.Builder
	b.WriteString("task " + t.ID)
	switch {
	case t.PR > 0:
		fmt.Fprintf(&b, " (%s#%d, PR #%d)", t.Repo, t.Issue, t.PR)
	case t.Issue > 0:
		fmt.Fprintf(&b, " (%s#%d)", t.Repo, t.Issue)
	}
	return b.String()
}

// TaskSource reads the live task records — the observability surface (§2/§3).
// Read-only. The gateway backs it with /cortex/tasks + the transition log; tests
// inject a fixture. This is the seam that guarantees a status answer is a fresh
// projection, not the model recalling.
type TaskSource interface {
	Tasks(ctx context.Context) ([]TaskView, error)
	Task(ctx context.Context, id string) (TaskView, bool, error)
}

// StatusProjector answers status questions as deterministic projections of the
// task record: every answer cites task ids and quotes the mechanical reason
// verbatim, and an unknown task is answered "I don't have that", never invented.
type StatusProjector struct{ src TaskSource }

// NewStatusProjector builds a projector over a task source.
func NewStatusProjector(src TaskSource) *StatusProjector { return &StatusProjector{src: src} }

const unknownTaskPrefix = "I don't have"

// Running renders the active tasks — what is in flight right now — and returns the
// structured views alongside so a caller can render differently. The text cites
// each task and quotes its latest mechanical reason verbatim.
func (p *StatusProjector) Running(ctx context.Context) (string, []TaskView, error) {
	all, err := p.src.Tasks(ctx)
	if err != nil {
		return "", nil, err
	}
	active := make([]TaskView, 0, len(all))
	for _, t := range all {
		if t.Active() {
			active = append(active, t)
		}
	}
	sort.SliceStable(active, func(i, j int) bool { return active[i].Updated.After(active[j].Updated) })
	if len(active) == 0 {
		return "Nothing is running right now.", active, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d task(s) running:\n", len(active))
	for _, t := range active {
		fmt.Fprintf(&b, "- %s: %s", t.ref(), t.Status)
		if r := strings.TrimSpace(t.Reason); r != "" {
			fmt.Fprintf(&b, " — %s", r) // verbatim, not paraphrased
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n"), active, nil
}

// Explain renders one task's status, quoting its mechanical reason verbatim. An
// unknown id is answered "I don't have …" — the projector never invents a task.
func (p *StatusProjector) Explain(ctx context.Context, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("steward: empty task id")
	}
	t, ok, err := p.src.Task(ctx, id)
	if err != nil {
		return "", err
	}
	if !ok {
		return fmt.Sprintf("%s a task %q — I only report what's in the task record.", unknownTaskPrefix, id), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s is %s.", t.ref(), t.Status)
	if r := strings.TrimSpace(t.Reason); r != "" {
		fmt.Fprintf(&b, " Reason: %s", r) // verbatim from the transition record
	}
	return b.String(), nil
}
