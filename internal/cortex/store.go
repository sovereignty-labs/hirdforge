package cortex

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "github.com/lib/pq" // postgres driver, same as cmd/gateway/a2a.go
)

// TaskRecord is one row of cortex_tasks (PERSISTENCE.md). PRNumber is written
// only from a Gitea API observation, never from agent output (invariant #3).
type TaskRecord struct {
	ID      string `json:"id"`
	RouteID string `json:"route_id"`
	Status  string `json:"status"`
	Attempt int    `json:"attempt"`

	IssueRepo   string `json:"issue_repo"`
	IssueNumber int64  `json:"issue_number,omitempty"`
	IssueTitle  string `json:"issue_title,omitempty"`

	Agent      string `json:"agent,omitempty"`
	Reviewer   string `json:"reviewer,omitempty"`
	SandboxID  string `json:"sandbox_id,omitempty"`
	WorkBranch string `json:"work_branch,omitempty"`
	PRRepo     string `json:"pr_repo,omitempty"`
	PRNumber   int64  `json:"pr_number,omitempty"`

	Bundle         Bundle          `json:"bundle"`
	DoneGate       json.RawMessage `json:"done_gate"` // gate config snapshot + last GateResult
	FailureContext json.RawMessage `json:"failure_context,omitempty"`
	Diagnostics    json.RawMessage `json:"diagnostics,omitempty"` // informational ONLY — nothing reads this for control flow

	TimeoutAt *time.Time `json:"timeout_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// TransitionRecord is one row of cortex_transitions.
type TransitionRecord struct {
	TaskID     string    `json:"task_id"`
	FromStatus string    `json:"from_status,omitempty"`
	ToStatus   string    `json:"to_status"`
	Reason     string    `json:"reason"`
	Cause      Cause     `json:"cause"`
	At         time.Time `json:"at"`
}

// TaskFilter filters ListTasks.
type TaskFilter struct {
	Status string
	Agent  string
	Repo   string
	Limit  int
}

// Store is the lifecycle store. Implementations MUST enforce the transition
// invariants (CheckTransition + transition-row-with-status-change atomicity).
type Store interface {
	// CreateTask inserts the task in StatusQueued together with its creating
	// transition row, atomically.
	CreateTask(t *TaskRecord, reason string, cause Cause) error
	// Transition moves a task to a new status and records the transition row
	// atomically. It is the ONLY way a status changes.
	Transition(taskID, to, reason string, cause Cause) error
	GetTask(id string) (*TaskRecord, []TransitionRecord, error)
	ListTasks(f TaskFilter) ([]TaskRecord, error)
	// ListActive returns all tasks in a non-terminal status (for the startup
	// reconciler and the timeout watchdog).
	ListActive() ([]TaskRecord, error)
	// Non-status mutators (status changes go through Transition ONLY).
	SetPR(taskID, prRepo string, prNumber int64) error
	SetGateResult(taskID string, lastResult []byte) error
	SetReviewer(taskID, reviewer string) error
	// PrepareRetry bumps attempt and stores the failure context the next
	// dispatch envelope will carry (D-LESSONS #2). Does not change status.
	PrepareRetry(taskID string, failureContext []byte) error
	FindTaskByPR(prRepo string, prNumber int64) (*TaskRecord, error)
	RecordDecision(d Decision) error
	ListDecisions(limit int) ([]Decision, error)
}

// PGStore is the Postgres implementation, following the gateway's existing
// lib/pq + CREATE TABLE IF NOT EXISTS convention (cmd/gateway/a2a.go).
type PGStore struct {
	db *sql.DB
}

// InitPGStore opens the database, creates the v2 tables (beside — never
// touching — the v1 a2a tables), and returns the store.
func InitPGStore(dbURL string) (*PGStore, error) {
	dbURL = strings.TrimSpace(dbURL)
	if dbURL == "" {
		return nil, fmt.Errorf("cortex store: empty database URL")
	}
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS cortex_tasks (
			id             TEXT PRIMARY KEY,
			route_id       TEXT NOT NULL,
			status         TEXT NOT NULL DEFAULT 'queued',
			attempt        INT  NOT NULL DEFAULT 1,
			issue_repo     TEXT NOT NULL,
			issue_number   BIGINT,
			issue_title    TEXT,
			agent          TEXT,
			reviewer       TEXT,
			sandbox_id     TEXT,
			work_branch    TEXT,
			pr_repo        TEXT,
			pr_number      BIGINT,
			bundle         JSONB NOT NULL,
			done_gate      JSONB NOT NULL,
			failure_context JSONB,
			diagnostics    JSONB,
			timeout_at     TIMESTAMPTZ,
			created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`ALTER TABLE cortex_tasks ADD COLUMN IF NOT EXISTS reviewer TEXT`,
		`CREATE INDEX IF NOT EXISTS idx_cortex_tasks_status ON cortex_tasks(status)`,
		`CREATE INDEX IF NOT EXISTS idx_cortex_tasks_pr ON cortex_tasks(pr_repo, pr_number)`,
		`CREATE INDEX IF NOT EXISTS idx_cortex_tasks_issue ON cortex_tasks(issue_repo, issue_number)`,
		`CREATE TABLE IF NOT EXISTS cortex_transitions (
			id          BIGSERIAL PRIMARY KEY,
			task_id     TEXT NOT NULL REFERENCES cortex_tasks(id),
			from_status TEXT,
			to_status   TEXT NOT NULL,
			reason      TEXT NOT NULL,
			cause       JSONB NOT NULL,
			at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cortex_transitions_task ON cortex_transitions(task_id)`,
		`CREATE TABLE IF NOT EXISTS cortex_decisions (
			id            BIGSERIAL PRIMARY KEY,
			event_type    TEXT NOT NULL,
			event         JSONB NOT NULL,
			matched_route TEXT,
			task_id       TEXT,
			reason        TEXT NOT NULL,
			at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cortex_decisions_at ON cortex_decisions(at DESC)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("cortex store: init: %w", err)
		}
	}
	return &PGStore{db: db}, nil
}

func (s *PGStore) CreateTask(t *TaskRecord, reason string, cause Cause) error {
	if err := CheckTransition("", StatusQueued, cause); err != nil {
		return err
	}
	bundleJSON, err := json.Marshal(t.Bundle)
	if err != nil {
		return err
	}
	causeJSON, err := json.Marshal(cause)
	if err != nil {
		return err
	}
	doneGate := t.DoneGate
	if len(doneGate) == 0 {
		doneGate = json.RawMessage(`{}`)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`INSERT INTO cortex_tasks
		(id, route_id, status, attempt, issue_repo, issue_number, issue_title, bundle, done_gate, timeout_at)
		VALUES ($1,$2,$3,1,$4,$5,$6,$7,$8,$9)`,
		t.ID, t.RouteID, StatusQueued, t.IssueRepo, nullableInt64(t.IssueNumber), t.IssueTitle,
		bundleJSON, doneGate, t.TimeoutAt)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO cortex_transitions (task_id, from_status, to_status, reason, cause)
		VALUES ($1, NULL, $2, $3, $4)`, t.ID, StatusQueued, reason, causeJSON)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PGStore) Transition(taskID, to, reason string, cause Cause) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var from string
	if err := tx.QueryRow(`SELECT status FROM cortex_tasks WHERE id = $1 FOR UPDATE`, taskID).Scan(&from); err != nil {
		return fmt.Errorf("cortex store: task %s: %w", taskID, err)
	}
	if err := CheckTransition(from, to, cause); err != nil {
		return err
	}
	causeJSON, err := json.Marshal(cause)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE cortex_tasks SET status = $1, updated_at = NOW() WHERE id = $2`, to, taskID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO cortex_transitions (task_id, from_status, to_status, reason, cause)
		VALUES ($1,$2,$3,$4,$5)`, taskID, from, to, reason, causeJSON); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PGStore) GetTask(id string) (*TaskRecord, []TransitionRecord, error) {
	var t TaskRecord
	var bundleJSON, doneGate []byte
	var failureCtx, diagnostics sql.NullString
	var issueNumber, prNumber sql.NullInt64
	var agent, reviewer, sandboxID, workBranch, prRepo, issueTitle sql.NullString
	var timeoutAt sql.NullTime
	err := s.db.QueryRow(`SELECT id, route_id, status, attempt, issue_repo, issue_number, issue_title,
		agent, reviewer, sandbox_id, work_branch, pr_repo, pr_number, bundle, done_gate, failure_context, diagnostics,
		timeout_at, created_at, updated_at FROM cortex_tasks WHERE id = $1`, id).Scan(
		&t.ID, &t.RouteID, &t.Status, &t.Attempt, &t.IssueRepo, &issueNumber, &issueTitle,
		&agent, &reviewer, &sandboxID, &workBranch, &prRepo, &prNumber, &bundleJSON, &doneGate,
		&failureCtx, &diagnostics, &timeoutAt, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, nil, err
	}
	t.IssueNumber = issueNumber.Int64
	t.IssueTitle = issueTitle.String
	t.Agent = agent.String
	t.Reviewer = reviewer.String
	t.SandboxID = sandboxID.String
	t.WorkBranch = workBranch.String
	t.PRRepo = prRepo.String
	t.PRNumber = prNumber.Int64
	if timeoutAt.Valid {
		t.TimeoutAt = &timeoutAt.Time
	}
	_ = json.Unmarshal(bundleJSON, &t.Bundle)
	t.DoneGate = doneGate
	if failureCtx.Valid {
		t.FailureContext = json.RawMessage(failureCtx.String)
	}
	if diagnostics.Valid {
		t.Diagnostics = json.RawMessage(diagnostics.String)
	}

	rows, err := s.db.Query(`SELECT task_id, from_status, to_status, reason, cause, at
		FROM cortex_transitions WHERE task_id = $1 ORDER BY at, id`, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var history []TransitionRecord
	for rows.Next() {
		var tr TransitionRecord
		var fromStatus sql.NullString
		var causeJSON []byte
		if err := rows.Scan(&tr.TaskID, &fromStatus, &tr.ToStatus, &tr.Reason, &causeJSON, &tr.At); err != nil {
			return nil, nil, err
		}
		tr.FromStatus = fromStatus.String
		_ = json.Unmarshal(causeJSON, &tr.Cause)
		history = append(history, tr)
	}
	return &t, history, rows.Err()
}

func (s *PGStore) ListTasks(f TaskFilter) ([]TaskRecord, error) {
	q := `SELECT id, route_id, status, attempt, issue_repo, issue_number, agent, pr_number, created_at, updated_at
		FROM cortex_tasks WHERE 1=1`
	args := []any{}
	if f.Status != "" {
		args = append(args, f.Status)
		q += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if f.Agent != "" {
		args = append(args, f.Agent)
		q += fmt.Sprintf(" AND agent = $%d", len(args))
	}
	if f.Repo != "" {
		args = append(args, f.Repo)
		q += fmt.Sprintf(" AND issue_repo = $%d", len(args))
	}
	q += " ORDER BY created_at DESC"
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	args = append(args, limit)
	q += fmt.Sprintf(" LIMIT $%d", len(args))

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskRecord
	for rows.Next() {
		var t TaskRecord
		var issueNumber, prNumber sql.NullInt64
		var agent sql.NullString
		if err := rows.Scan(&t.ID, &t.RouteID, &t.Status, &t.Attempt, &t.IssueRepo, &issueNumber,
			&agent, &prNumber, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		t.IssueNumber = issueNumber.Int64
		t.Agent = agent.String
		t.PRNumber = prNumber.Int64
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *PGStore) ListActive() ([]TaskRecord, error) {
	rows, err := s.db.Query(`SELECT id, route_id, status, attempt, issue_repo, issue_number, issue_title,
		agent, work_branch, pr_repo, pr_number, timeout_at, created_at, updated_at
		FROM cortex_tasks WHERE status NOT IN ('validated','failed') ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskRecord
	for rows.Next() {
		var t TaskRecord
		var issueNumber, prNumber sql.NullInt64
		var agent, workBranch, prRepo, issueTitle sql.NullString
		var timeoutAt sql.NullTime
		if err := rows.Scan(&t.ID, &t.RouteID, &t.Status, &t.Attempt, &t.IssueRepo, &issueNumber, &issueTitle,
			&agent, &workBranch, &prRepo, &prNumber, &timeoutAt, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		t.IssueNumber = issueNumber.Int64
		t.IssueTitle = issueTitle.String
		t.Agent = agent.String
		t.WorkBranch = workBranch.String
		t.PRRepo = prRepo.String
		t.PRNumber = prNumber.Int64
		if timeoutAt.Valid {
			t.TimeoutAt = &timeoutAt.Time
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *PGStore) RecordDecision(d Decision) error {
	eventJSON, err := json.Marshal(d.Event)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO cortex_decisions (event_type, event, matched_route, task_id, reason, at)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		d.Event.Type, eventJSON, nullableString(d.MatchedRoute), nullableString(d.TaskID), d.Reason, d.At)
	return err
}

func (s *PGStore) ListDecisions(limit int) ([]Decision, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT event, matched_route, task_id, reason, at
		FROM cortex_decisions ORDER BY at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Decision
	for rows.Next() {
		var d Decision
		var eventJSON []byte
		var matched, taskID sql.NullString
		if err := rows.Scan(&eventJSON, &matched, &taskID, &d.Reason, &d.At); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(eventJSON, &d.Event)
		d.MatchedRoute = matched.String
		d.TaskID = taskID.String
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *PGStore) SetPR(taskID, prRepo string, prNumber int64) error {
	return s.execOne(`UPDATE cortex_tasks SET pr_repo=$1, pr_number=$2, updated_at=NOW() WHERE id=$3`,
		prRepo, prNumber, taskID)
}

func (s *PGStore) SetGateResult(taskID string, lastResult []byte) error {
	return s.execOne(`UPDATE cortex_tasks SET done_gate = jsonb_set(done_gate, '{last_result}', $1::jsonb), updated_at=NOW() WHERE id=$2`,
		lastResult, taskID)
}

func (s *PGStore) SetReviewer(taskID, reviewer string) error {
	return s.execOne(`UPDATE cortex_tasks SET reviewer=$1, updated_at=NOW() WHERE id=$2`, reviewer, taskID)
}

func (s *PGStore) PrepareRetry(taskID string, failureContext []byte) error {
	return s.execOne(`UPDATE cortex_tasks SET attempt = attempt + 1, failure_context = $1::jsonb, updated_at=NOW() WHERE id=$2`,
		failureContext, taskID)
}

func (s *PGStore) FindTaskByPR(prRepo string, prNumber int64) (*TaskRecord, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM cortex_tasks WHERE pr_repo=$1 AND pr_number=$2 ORDER BY created_at DESC LIMIT 1`,
		prRepo, prNumber).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("cortex store: task for PR %s#%d: %w", prRepo, prNumber, err)
	}
	t, _, err := s.GetTask(id)
	return t, err
}

func (s *PGStore) execOne(q string, args ...any) error {
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("cortex store: no task matched update")
	}
	return nil
}

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
