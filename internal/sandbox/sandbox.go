package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// RunSpec is everything the sandbox needs to run one task. Commands are data:
// the envelope is mounted read-only at /task/envelope.json and the workspace
// is a fresh emptyDir at /work — clean by construction, asserted by the guard.
type RunSpec struct {
	TaskID       string
	Envelope     []byte   // mounted at /task/envelope.json via a per-task ConfigMap
	AgentImage   string   // image for guard/agent/gate containers
	AgentCommand []string // the one-shot agent invocation (P1.4 wires the real one)
	CloneURL     string
	BaseBranch   string
	WorkBranch   string
	GateCommand  string        // run in /work/repo AFTER the agent exits (DONE_GATE.md)
	GateTimeout  time.Duration // activeDeadline contribution for the gate container
	CredSecret   string        // k8s secret with git credentials; never inline (DISPATCH_ENVELOPE.md)
	Deadline     time.Duration // whole-Job activeDeadlineSeconds (route timeout)
}

// Ref identifies an allocated sandbox.
type Ref struct {
	Namespace string `json:"namespace"`
	JobName   string `json:"job_name"`
	CMName    string `json:"cm_name"`
	PodName   string `json:"pod_name,omitempty"` // filled by Wait
}

// RunResult is the mechanical outcome of a sandbox run. Exit codes are OS
// facts; nothing here is model-reported.
type RunResult struct {
	CleanCheckOK  bool      `json:"clean_check_ok"`
	CheckoutOK    bool      `json:"checkout_ok"`
	AgentExitCode int       `json:"agent_exit_code"`
	GateExitCode  int       `json:"gate_exit_code"`
	Phase         string    `json:"phase"` // succeeded | failed | deadline
	StartedAt     time.Time `json:"started_at,omitempty"`
	FinishedAt    time.Time `json:"finished_at,omitempty"`
}

// Sandbox is the lifecycle contract: allocate → (checkout+run+gate inside the
// Job) → collect → destroy.
type Sandbox interface {
	Allocate(ctx context.Context, spec RunSpec) (Ref, error)
	Wait(ctx context.Context, ref *Ref) (RunResult, error)
	GateOutput(ctx context.Context, ref Ref) ([]byte, error)
	Destroy(ctx context.Context, ref Ref) error
}

// K8sSandbox implements Sandbox against a Kubernetes API.
type K8sSandbox struct {
	Client    *K8sClient
	Namespace string        // the dedicated sandbox namespace
	Poll      time.Duration // Wait poll interval (test-tunable)
}

func NewK8sSandbox(client *K8sClient, namespace string) *K8sSandbox {
	return &K8sSandbox{Client: client, Namespace: namespace, Poll: 5 * time.Second}
}

// exit codes the guard container uses to make failures legible
const (
	guardExitDirty     = 90
	guardExitCloneFail = 91
	containerGuard     = "guard-checkout"
	containerAgent     = "agent"
	containerGate      = "gate"
	labelTaskID        = "hirdforge.io/task-id"
	labelManagedBy     = "hirdforge.io/managed-by"
	managedByValue     = "cortex"
	envelopeMountPath  = "/task"
	workspaceMountPath = "/work"
)

// Allocate creates the per-task ConfigMap (envelope) and the Job. The Job's
// container sequence IS the lifecycle: guard+checkout → agent → gate, all
// sharing one fresh emptyDir. If Job creation fails the ConfigMap is cleaned
// up before returning.
func (s *K8sSandbox) Allocate(ctx context.Context, spec RunSpec) (Ref, error) {
	if err := validateSpec(spec); err != nil {
		return Ref{}, err
	}
	ref := Ref{
		Namespace: s.Namespace,
		JobName:   "hf-task-" + sanitizeName(spec.TaskID),
		CMName:    "hf-task-" + sanitizeName(spec.TaskID) + "-envelope",
	}
	cm := buildConfigMap(ref, spec)
	if _, err := s.Client.doJSON(ctx, "POST",
		fmt.Sprintf("/api/v1/namespaces/%s/configmaps", s.Namespace), jsonBody(cm)); err != nil {
		return Ref{}, fmt.Errorf("sandbox: envelope configmap: %w", err)
	}
	job := buildJob(ref, spec)
	if _, err := s.Client.doJSON(ctx, "POST",
		fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs", s.Namespace), jsonBody(job)); err != nil {
		// Roll the ConfigMap back so a failed allocate leaves nothing behind.
		_, _ = s.Client.doJSON(ctx, "DELETE",
			fmt.Sprintf("/api/v1/namespaces/%s/configmaps/%s", s.Namespace, ref.CMName), nil)
		return Ref{}, fmt.Errorf("sandbox: job create: %w", err)
	}
	return ref, nil
}

// Wait polls the Job until it reaches a terminal phase, then reads the pod's
// container statuses into a RunResult.
func (s *K8sSandbox) Wait(ctx context.Context, ref *Ref) (RunResult, error) {
	poll := s.Poll
	if poll <= 0 {
		poll = 5 * time.Second
	}
	for {
		data, err := s.Client.doJSON(ctx, "GET",
			fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs/%s", ref.Namespace, ref.JobName), nil)
		if err != nil {
			return RunResult{}, fmt.Errorf("sandbox: job status: %w", err)
		}
		var job struct {
			Status struct {
				Succeeded  int `json:"succeeded"`
				Failed     int `json:"failed"`
				Conditions []struct {
					Type   string `json:"type"`
					Reason string `json:"reason"`
				} `json:"conditions"`
			} `json:"status"`
		}
		if err := json.Unmarshal(data, &job); err != nil {
			return RunResult{}, err
		}
		terminal := job.Status.Succeeded > 0 || job.Status.Failed > 0
		deadline := false
		for _, c := range job.Status.Conditions {
			if c.Type == "Failed" && c.Reason == "DeadlineExceeded" {
				terminal, deadline = true, true
			}
		}
		if terminal {
			res, err := s.collectPodResult(ctx, ref)
			if err != nil {
				return res, err
			}
			if deadline {
				res.Phase = "deadline"
			} else if job.Status.Succeeded > 0 {
				res.Phase = "succeeded"
			} else {
				res.Phase = "failed"
			}
			return res, nil
		}
		select {
		case <-ctx.Done():
			return RunResult{}, ctx.Err()
		case <-time.After(poll):
		}
	}
}

func (s *K8sSandbox) collectPodResult(ctx context.Context, ref *Ref) (RunResult, error) {
	data, err := s.Client.doJSON(ctx, "GET",
		fmt.Sprintf("/api/v1/namespaces/%s/pods?labelSelector=job-name%%3D%s", ref.Namespace, ref.JobName), nil)
	if err != nil {
		return RunResult{}, fmt.Errorf("sandbox: pod list: %w", err)
	}
	var pods struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				StartTime             time.Time         `json:"startTime"`
				InitContainerStatuses []containerStatus `json:"initContainerStatuses"`
				ContainerStatuses     []containerStatus `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &pods); err != nil {
		return RunResult{}, err
	}
	if len(pods.Items) == 0 {
		return RunResult{}, fmt.Errorf("sandbox: no pod for job %s", ref.JobName)
	}
	pod := pods.Items[len(pods.Items)-1]
	ref.PodName = pod.Metadata.Name

	res := RunResult{StartedAt: pod.Status.StartTime, AgentExitCode: -1, GateExitCode: -1}
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Terminated == nil {
			continue
		}
		switch cs.Name {
		case containerGuard:
			res.CleanCheckOK = cs.State.Terminated.ExitCode != guardExitDirty
			res.CheckoutOK = cs.State.Terminated.ExitCode == 0
		case containerAgent:
			res.AgentExitCode = cs.State.Terminated.ExitCode
		}
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == containerGate && cs.State.Terminated != nil {
			res.GateExitCode = cs.State.Terminated.ExitCode
			res.FinishedAt = cs.State.Terminated.FinishedAt
		}
	}
	return res, nil
}

type containerStatus struct {
	Name  string `json:"name"`
	State struct {
		Terminated *struct {
			ExitCode   int       `json:"exitCode"`
			FinishedAt time.Time `json:"finishedAt"`
		} `json:"terminated"`
	} `json:"state"`
}

// GateOutput returns the gate container's log tail — the evidence the
// GateResult carries (DONE_GATE.md: evidence is stored even when green).
func (s *K8sSandbox) GateOutput(ctx context.Context, ref Ref) ([]byte, error) {
	if ref.PodName == "" {
		return nil, fmt.Errorf("sandbox: no pod name on ref (Wait first)")
	}
	resp, err := s.Client.do(ctx, "GET",
		fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/log?container=%s&tailLines=200",
			ref.Namespace, ref.PodName, containerGate), nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("sandbox: gate log: %s", resp.Status)
	}
	return readCapped(resp.Body, 4<<10) // ≤4KiB excerpt per the contract
}

// Destroy deletes the Job (cascading to its pod) and the envelope ConfigMap.
// Idempotent: 404s are success. A failure here is the caller's loud
// sandbox.leak event — never swallowed silently.
func (s *K8sSandbox) Destroy(ctx context.Context, ref Ref) error {
	var firstErr error
	body := strings.NewReader(`{"propagationPolicy":"Background"}`)
	if _, err := s.Client.doJSON(ctx, "DELETE",
		fmt.Sprintf("/apis/batch/v1/namespaces/%s/jobs/%s", ref.Namespace, ref.JobName), body); err != nil && !isNotFound(err) {
		firstErr = err
	}
	if _, err := s.Client.doJSON(ctx, "DELETE",
		fmt.Sprintf("/api/v1/namespaces/%s/configmaps/%s", ref.Namespace, ref.CMName), nil); err != nil && !isNotFound(err) && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "404")
}

func validateSpec(spec RunSpec) error {
	switch {
	case spec.TaskID == "":
		return fmt.Errorf("sandbox: spec missing TaskID")
	case len(spec.Envelope) == 0:
		return fmt.Errorf("sandbox: spec missing Envelope")
	case spec.AgentImage == "":
		return fmt.Errorf("sandbox: spec missing AgentImage")
	case len(spec.AgentCommand) == 0:
		return fmt.Errorf("sandbox: spec missing AgentCommand")
	case spec.CloneURL == "" || spec.BaseBranch == "" || spec.WorkBranch == "":
		return fmt.Errorf("sandbox: spec missing git checkout fields")
	case spec.GateCommand == "":
		return fmt.Errorf("sandbox: spec missing GateCommand (D-GATE: every builder run is gated)")
	}
	return nil
}

// sanitizeName makes a task id a valid k8s resource name segment.
func sanitizeName(id string) string {
	s := strings.ToLower(id)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}
