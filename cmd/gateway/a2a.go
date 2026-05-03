package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"
)

type TaskState string

const (
	TaskStateSubmitted   TaskState = "submitted"
	TaskStateWorking     TaskState = "working"
	TaskStateCompleted   TaskState = "completed"
	TaskStateFailed      TaskState = "failed"
	TaskStateCanceled    TaskState = "canceled"
	TaskStateInputNeeded TaskState = "input-needed"
)

type Task struct {
	ID        string                 `json:"id"`
	ContextID string                 `json:"contextId"`
	Status    TaskStatus             `json:"status"`
	Artifacts []Artifact             `json:"artifacts,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Agent     string                 `json:"agent,omitempty"`
	CreatedAt string                 `json:"created_at,omitempty"`
	UpdatedAt string                 `json:"updated_at,omitempty"`
}

type TaskStatus struct {
	State     TaskState `json:"state"`
	Timestamp string    `json:"timestamp,omitempty"`
	Message   *Message  `json:"message,omitempty"`
}

type Message struct {
	Role      string `json:"role,omitempty"`
	Parts     []Part `json:"parts,omitempty"`
	MessageID string `json:"messageId,omitempty"`
}

type Part struct {
	Text     string                 `json:"text,omitempty"`
	Data     interface{}            `json:"data,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

type Artifact struct {
	ArtifactID string `json:"artifactId,omitempty"`
	Parts      []Part `json:"parts,omitempty"`
}

type PushNotificationConfig struct {
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
}

type TaskStatusUpdateEvent struct {
	TaskID string     `json:"taskId"`
	Status TaskStatus `json:"status"`
	Final  bool       `json:"final"`
}

type TaskArtifactUpdateEvent struct {
	TaskID   string   `json:"taskId"`
	Artifact Artifact `json:"artifact"`
}

type TaskFilter struct {
	State     TaskState
	Agent     string
	ContextID string
	Limit     int
}

type A2ATaskStore struct {
	db *sql.DB
	mu sync.RWMutex

	createTaskOverride   func(task *Task) error
	getTaskOverride      func(id string) (*Task, error)
	listTasksOverride    func(filter TaskFilter) ([]Task, error)
	updateStatusOverride func(id string, status TaskStatus) error
	addArtifactOverride  func(id string, artifact Artifact) error
	addMessageOverride   func(taskID string, msg Message) error
}

func (s *A2ATaskStore) Init(dbURL string) error {
	dbURL = strings.TrimSpace(dbURL)
	if dbURL == "" {
		return fmt.Errorf("a2a: empty database URL")
	}
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		return err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return err
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS a2a_tasks (
			id TEXT PRIMARY KEY,
			context_id TEXT NOT NULL,
			state TEXT NOT NULL DEFAULT 'submitted',
			agent TEXT NOT NULL,
			metadata JSONB,
			artifacts JSONB,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS a2a_messages (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL REFERENCES a2a_tasks(id),
			role TEXT NOT NULL,
			parts JSONB NOT NULL,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_a2a_tasks_state ON a2a_tasks(state)`,
		`CREATE INDEX IF NOT EXISTS idx_a2a_tasks_agent ON a2a_tasks(agent)`,
		`CREATE INDEX IF NOT EXISTS idx_a2a_tasks_context ON a2a_tasks(context_id)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			return err
		}
	}
	s.mu.Lock()
	s.db = db
	s.mu.Unlock()
	return nil
}

func (s *A2ATaskStore) CreateTask(task *Task) error {
	if s != nil && s.createTaskOverride != nil {
		return s.createTaskOverride(task)
	}
	if s == nil || s.db == nil {
		return fmt.Errorf("a2a: task store not initialized")
	}
	if task == nil {
		return fmt.Errorf("a2a: task is nil")
	}
	if strings.TrimSpace(task.ID) == "" || strings.TrimSpace(task.ContextID) == "" || strings.TrimSpace(task.Agent) == "" {
		return fmt.Errorf("a2a: task id, context id, and agent are required")
	}
	state := strings.TrimSpace(string(task.Status.State))
	if state == "" {
		state = string(TaskStateSubmitted)
	}
	metadataJSON, err := json.Marshal(task.Metadata)
	if err != nil {
		return err
	}
	artifactsJSON, err := json.Marshal(task.Artifacts)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO a2a_tasks (id, context_id, state, agent, metadata, artifacts) VALUES ($1, $2, $3, $4, $5, $6)`,
		strings.TrimSpace(task.ID),
		strings.TrimSpace(task.ContextID),
		state,
		strings.TrimSpace(task.Agent),
		metadataJSON,
		artifactsJSON,
	)
	return err
}

func (s *A2ATaskStore) GetTask(id string) (*Task, error) {
	if s != nil && s.getTaskOverride != nil {
		return s.getTaskOverride(id)
	}
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("a2a: task store not initialized")
	}
	row := s.db.QueryRow(
		`SELECT id, context_id, state, agent, metadata, artifacts, created_at, updated_at FROM a2a_tasks WHERE id = $1`,
		strings.TrimSpace(id),
	)
	return scanA2ATask(row)
}

func (s *A2ATaskStore) ListTasks(filter TaskFilter) ([]Task, error) {
	if s != nil && s.listTasksOverride != nil {
		return s.listTasksOverride(filter)
	}
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("a2a: task store not initialized")
	}
	query := `SELECT id, context_id, state, agent, metadata, artifacts, created_at, updated_at FROM a2a_tasks`
	where := make([]string, 0, 3)
	args := make([]interface{}, 0, 4)
	if state := strings.TrimSpace(string(filter.State)); state != "" {
		args = append(args, state)
		where = append(where, fmt.Sprintf("state = $%d", len(args)))
	}
	if agent := strings.TrimSpace(filter.Agent); agent != "" {
		args = append(args, agent)
		where = append(where, fmt.Sprintf("agent = $%d", len(args)))
	}
	if contextID := strings.TrimSpace(filter.ContextID); contextID != "" {
		args = append(args, contextID)
		where = append(where, fmt.Sprintf("context_id = $%d", len(args)))
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, clampInt(filter.Limit, 50, 200))
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		task, err := scanA2ATask(rows)
		if err != nil {
			return nil, err
		}
		if task != nil {
			out = append(out, *task)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *A2ATaskStore) UpdateTaskStatus(id string, status TaskStatus) error {
	if s != nil && s.updateStatusOverride != nil {
		return s.updateStatusOverride(id, status)
	}
	if s == nil || s.db == nil {
		return fmt.Errorf("a2a: task store not initialized")
	}
	taskID := strings.TrimSpace(id)
	if taskID == "" {
		return fmt.Errorf("a2a: task id is required")
	}
	state := strings.TrimSpace(string(status.State))
	if state == "" {
		return fmt.Errorf("a2a: task state is required")
	}
	updatedAt := time.Now().UTC()
	if ts := strings.TrimSpace(status.Timestamp); ts != "" {
		if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			updatedAt = parsed.UTC()
		}
	}
	result, err := s.db.Exec(
		`UPDATE a2a_tasks SET state = $1, updated_at = $2 WHERE id = $3`,
		state,
		updatedAt,
		taskID,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *A2ATaskStore) AddArtifact(id string, artifact Artifact) error {
	if s != nil && s.addArtifactOverride != nil {
		return s.addArtifactOverride(id, artifact)
	}
	if s == nil || s.db == nil {
		return fmt.Errorf("a2a: task store not initialized")
	}
	artifactJSON, err := json.Marshal([]Artifact{artifact})
	if err != nil {
		return err
	}
	result, err := s.db.Exec(
		`UPDATE a2a_tasks
		 SET artifacts = COALESCE(artifacts, '[]'::jsonb) || $1::jsonb,
		     updated_at = NOW()
		 WHERE id = $2`,
		artifactJSON,
		strings.TrimSpace(id),
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *A2ATaskStore) AddMessage(taskID string, msg Message) error {
	if s != nil && s.addMessageOverride != nil {
		return s.addMessageOverride(taskID, msg)
	}
	if s == nil || s.db == nil {
		return fmt.Errorf("a2a: task store not initialized")
	}
	messageID := strings.TrimSpace(msg.MessageID)
	if messageID == "" {
		messageID = fmt.Sprintf("a2a-msg-%d", time.Now().UTC().UnixNano())
	}
	partsJSON, err := json.Marshal(msg.Parts)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO a2a_messages (id, task_id, role, parts) VALUES ($1, $2, $3, $4)`,
		messageID,
		strings.TrimSpace(taskID),
		strings.TrimSpace(msg.Role),
		partsJSON,
	)
	return err
}

func scanA2ATask(scanner interface {
	Scan(dest ...interface{}) error
}) (*Task, error) {
	var (
		id         string
		contextID  string
		state      string
		agent      string
		metadataDB []byte
		artifacts  []byte
		createdAt  time.Time
		updatedAt  time.Time
	)
	if err := scanner.Scan(&id, &contextID, &state, &agent, &metadataDB, &artifacts, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	task := &Task{
		ID:        id,
		ContextID: contextID,
		Agent:     agent,
		CreatedAt: createdAt.UTC().Format(time.RFC3339),
		UpdatedAt: updatedAt.UTC().Format(time.RFC3339),
		Status: TaskStatus{
			State:     TaskState(strings.TrimSpace(state)),
			Timestamp: updatedAt.UTC().Format(time.RFC3339),
		},
	}
	if len(metadataDB) > 0 && string(metadataDB) != "null" {
		if err := json.Unmarshal(metadataDB, &task.Metadata); err != nil {
			return nil, err
		}
	}
	if len(artifacts) > 0 && string(artifacts) != "null" {
		if err := json.Unmarshal(artifacts, &task.Artifacts); err != nil {
			return nil, err
		}
	}
	return task, nil
}

func (g *gateway) registerA2ARoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/a2a/tasks", func(w http.ResponseWriter, r *http.Request) {
		if g.a2aStore == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "A2A not configured"})
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit := 50
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid limit"})
				return
			}
			limit = clampInt(n, 50, 200)
		}
		tasks, err := g.a2aStore.ListTasks(TaskFilter{
			State:     TaskState(strings.TrimSpace(r.URL.Query().Get("state"))),
			Agent:     strings.TrimSpace(r.URL.Query().Get("agent")),
			ContextID: strings.TrimSpace(r.URL.Query().Get("context_id")),
			Limit:     limit,
		})
		if err != nil {
			log.Printf("a2a: list tasks failed: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list tasks"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"tasks": tasks})
	})

	mux.HandleFunc("/api/v1/a2a/tasks/", func(w http.ResponseWriter, r *http.Request) {
		if g.a2aStore == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "A2A not configured"})
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/a2a/tasks/")
		if path == "" || strings.Contains(path, "//") {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(path, "/cancel") {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			taskID := strings.Trim(strings.TrimSuffix(path, "/cancel"), "/")
			if taskID == "" || strings.Contains(taskID, "/") {
				http.NotFound(w, r)
				return
			}
			task, err := g.a2aStore.GetTask(taskID)
			if err != nil {
				log.Printf("a2a: get task for cancel failed: %v", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load task"})
				return
			}
			if task == nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
				return
			}
			status := TaskStatus{
				State:     TaskStateCanceled,
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			}
			if err := g.a2aStore.UpdateTaskStatus(taskID, status); err != nil {
				log.Printf("a2a: cancel task failed: %v", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to cancel task"})
				return
			}
			updated, err := g.a2aStore.GetTask(taskID)
			if err != nil {
				log.Printf("a2a: reload canceled task failed: %v", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to reload task"})
				return
			}
			if updated == nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
				return
			}
			writeJSON(w, http.StatusOK, updated)
			return
		}
		taskID := strings.Trim(path, "/")
		if taskID == "" || strings.Contains(taskID, "/") {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		task, err := g.a2aStore.GetTask(taskID)
		if err != nil {
			log.Printf("a2a: get task failed: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load task"})
			return
		}
		if task == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
			return
		}
		writeJSON(w, http.StatusOK, task)
	})

	mux.HandleFunc("/api/v1/a2a/notify", func(w http.ResponseWriter, r *http.Request) {
		if g.a2aStore == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "A2A not configured"})
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			TaskID    string                 `json:"taskId"`
			Status    TaskStatus             `json:"status"`
			Artifacts []Artifact             `json:"artifacts"`
			Metadata  map[string]interface{} `json:"metadata"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
		taskID := strings.TrimSpace(in.TaskID)
		if taskID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "taskId is required"})
			return
		}
		task, err := g.a2aStore.GetTask(taskID)
		if err != nil {
			log.Printf("a2a: notify load task failed: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load task"})
			return
		}
		if task == nil {
			state := in.Status.State
			if strings.TrimSpace(string(state)) == "" {
				state = TaskStateSubmitted
			}
			contextID := taskID
			if in.Status.Message != nil && strings.TrimSpace(in.Status.Message.MessageID) != "" {
				contextID = strings.TrimSpace(in.Status.Message.MessageID)
			}
			agent := strings.TrimSpace(fmt.Sprint(in.Metadata["agent"]))
			if agent == "" || agent == "<nil>" {
				agent = "unknown"
			}
			task = &Task{
				ID:        taskID,
				ContextID: contextID,
				Agent:     agent,
				Status:    TaskStatus{State: state},
				Metadata:  in.Metadata,
			}
			if err := g.a2aStore.CreateTask(task); err != nil {
				log.Printf("a2a: create task from notify failed: %v", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create task"})
				return
			}
		}
		if strings.TrimSpace(string(in.Status.State)) != "" {
			if strings.TrimSpace(in.Status.Timestamp) == "" {
				in.Status.Timestamp = time.Now().UTC().Format(time.RFC3339)
			}
			if err := g.a2aStore.UpdateTaskStatus(taskID, in.Status); err != nil {
				log.Printf("a2a: notify update status failed: %v", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update task status"})
				return
			}
		}
		if in.Status.Message != nil {
			if err := g.a2aStore.AddMessage(taskID, *in.Status.Message); err != nil {
				log.Printf("a2a: notify add message failed: %v", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to add task message"})
				return
			}
		}
		for _, artifact := range in.Artifacts {
			if err := g.a2aStore.AddArtifact(taskID, artifact); err != nil {
				log.Printf("a2a: notify add artifact failed: %v", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to add task artifact"})
				return
			}
		}
		g.broadcastPayload(map[string]interface{}{
			"type":      "a2a_task_update",
			"taskId":    taskID,
			"status":    in.Status,
			"artifacts": in.Artifacts,
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
}
