package cortex

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"time"
)

// Event is the one shape Cortex consumes, from both sources: Gitea webhooks
// (issue.labeled, pr.review_submitted, pr.merged) and internal mechanics
// (task.gate_passed, task.gate_failed, operator.dispatch). Fields beyond
// Type/Repo are populated per type; unused fields stay zero.
type Event struct {
	Type string `json:"type"`
	Repo string `json:"repo,omitempty"`

	// issue.labeled
	Label       string   `json:"label,omitempty"` // the label that fired
	IssueNumber int64    `json:"issue_number,omitempty"`
	IssueTitle  string   `json:"issue_title,omitempty"`
	IssueBody   string   `json:"issue_body,omitempty"` // data, never interpreted here
	IssueLabels []string `json:"issue_labels,omitempty"`

	// pr.review_submitted / pr.merged
	PRNumber    int64  `json:"pr_number,omitempty"`
	ReviewState string `json:"review_state,omitempty"` // APPROVED | REQUEST_CHANGES
	ReviewBody  string `json:"review_body,omitempty"`  // data, never interpreted by Cortex
	Actor       string `json:"actor,omitempty"`        // the Gitea login that caused the event

	// task.gate_passed / task.gate_failed / operator.dispatch
	RouteID string `json:"route_id,omitempty"` // originating route of the task
	TaskID  string `json:"task_id,omitempty"`
}

// Decision is one routing evaluation — match or no-match — recorded for the
// Cortex log (observability contract §2 /log, §4 cortex.decision stream).
type Decision struct {
	Event        Event     `json:"event"`
	MatchedRoute string    `json:"matched_route,omitempty"` // "" = no-match
	TaskID       string    `json:"task_id,omitempty"`
	Reason       string    `json:"reason"`
	At           time.Time `json:"at"`
}

// NewTaskID mints a creation-time-sortable task id: "hf-" + zero-padded
// millisecond hex + random suffix. Deliberately dependency-free; sortability
// comes from the fixed-width timestamp prefix (PERSISTENCE.md).
func NewTaskID() string {
	var suffix [5]byte
	if _, err := io.ReadFull(rand.Reader, suffix[:]); err != nil {
		// crypto/rand failing is a platform emergency; a constant suffix
		// still yields a usable (timestamp-unique at ms grain) id.
		copy(suffix[:], []byte{0, 0, 0, 0, 0})
	}
	return fmt.Sprintf("hf-%012x-%x", time.Now().UnixMilli(), suffix)
}

// newBytesReader avoids importing bytes in config.go's decoder path twice.
func newBytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
