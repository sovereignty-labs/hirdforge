package tasklife

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type TaskState string

const (
	StateDispatched TaskState = "dispatched"
	StateWorking    TaskState = "working"
	StateGateCheck  TaskState = "gate_check"
	StateCompleted  TaskState = "completed"
	StateNudged     TaskState = "nudged"
	StateFailedNoPR TaskState = "failed_no_pr"
)

var (
	ErrDuplicateDispatch = errors.New("duplicate dispatch blocked")
	ErrTaskExists        = errors.New("task already exists")
	ErrTaskNotFound      = errors.New("task not found")
)

type TaskRecord struct {
	TaskID    string
	Agent     string
	State     TaskState
	Result    string
	Nudge     string
	CreatedAt time.Time
	UpdatedAt time.Time
	History   []TaskState
}

type TaskEvent struct {
	From      string
	TaskID    string
	Agent     string
	State     TaskState
	Result    string
	Timestamp time.Time
}

type TaskTracker struct {
	mu    sync.Mutex
	tasks map[string]*TaskRecord
}

func NewTaskTracker() *TaskTracker {
	return &TaskTracker{tasks: make(map[string]*TaskRecord)}
}

func (t *TaskTracker) Dispatch(taskID, agent string) (TaskRecord, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	taskID = strings.TrimSpace(taskID)
	agent = strings.TrimSpace(agent)
	if taskID == "" || agent == "" {
		return TaskRecord{}, fmt.Errorf("taskID and agent are required")
	}
	if _, ok := t.tasks[taskID]; ok {
		return TaskRecord{}, ErrTaskExists
	}
	for _, task := range t.tasks {
		if task.Agent == agent && task.State == StateWorking {
			return TaskRecord{}, ErrDuplicateDispatch
		}
	}

	now := time.Now().UTC()
	record := &TaskRecord{
		TaskID:    taskID,
		Agent:     agent,
		State:     StateWorking,
		CreatedAt: now,
		UpdatedAt: now,
		History:   []TaskState{StateDispatched, StateWorking},
	}
	t.tasks[taskID] = record
	return cloneTaskRecord(record), nil
}

func (t *TaskTracker) Complete(taskID, result string, hasPR bool) (TaskRecord, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	task, ok := t.tasks[strings.TrimSpace(taskID)]
	if !ok {
		return TaskRecord{}, ErrTaskNotFound
	}
	task.Result = strings.TrimSpace(result)
	task.History = append(task.History, StateGateCheck)
	if hasPR {
		task.State = StateCompleted
		task.History = append(task.History, StateCompleted)
	} else {
		task.State = StateFailedNoPR
		task.History = append(task.History, StateFailedNoPR)
	}
	task.UpdatedAt = time.Now().UTC()
	return cloneTaskRecord(task), nil
}

func (t *TaskTracker) Nudge(taskID, nudge string) (TaskRecord, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	task, ok := t.tasks[strings.TrimSpace(taskID)]
	if !ok {
		return TaskRecord{}, ErrTaskNotFound
	}
	task.Nudge = strings.TrimSpace(nudge)
	task.History = append(task.History, StateGateCheck)
	task.State = StateNudged
	task.History = append(task.History, StateNudged)
	task.UpdatedAt = time.Now().UTC()
	return cloneTaskRecord(task), nil
}

func (t *TaskTracker) Task(taskID string) (TaskRecord, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	task, ok := t.tasks[strings.TrimSpace(taskID)]
	if !ok {
		return TaskRecord{}, false
	}
	return cloneTaskRecord(task), true
}

func cloneTaskRecord(in *TaskRecord) TaskRecord {
	out := *in
	out.History = append([]TaskState(nil), in.History...)
	return out
}
