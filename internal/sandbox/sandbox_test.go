package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testSpec() RunSpec {
	return RunSpec{
		TaskID:       "hf-000ABC-xyz",
		Envelope:     []byte(`{"envelope_version":1}`),
		AgentImage:   "registry/agent:test",
		AgentCommand: []string{"/agent", "-one-shot", "-envelope", "/task/envelope.json"},
		CloneURL:     "https://git.hirdforge.com/kit/hirdforge.git",
		BaseBranch:   "main",
		WorkBranch:   "agent/hf-000abc",
		GateCommand:  "go build ./... && go test ./...",
		Deadline:     30 * time.Minute,
	}
}

// fakeAPI is a minimal k8s API fake capturing created objects.
type fakeAPI struct {
	t          *testing.T
	mux        *http.ServeMux
	createdCM  map[string]any
	createdJob map[string]any
	jobStatus  map[string]any
	podList    string
	deleted    []string
}

func newFakeAPI(t *testing.T) (*fakeAPI, *httptest.Server) {
	f := &fakeAPI{t: t, mux: http.NewServeMux()}
	f.mux.HandleFunc("POST /api/v1/namespaces/sandbox/configmaps", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&f.createdCM)
		w.WriteHeader(201)
		fmt.Fprint(w, `{}`)
	})
	f.mux.HandleFunc("POST /apis/batch/v1/namespaces/sandbox/jobs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&f.createdJob)
		w.WriteHeader(201)
		fmt.Fprint(w, `{}`)
	})
	f.mux.HandleFunc("GET /apis/batch/v1/namespaces/sandbox/jobs/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": f.jobStatus})
	})
	f.mux.HandleFunc("GET /api/v1/namespaces/sandbox/pods", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, f.podList)
	})
	f.mux.HandleFunc("GET /api/v1/namespaces/sandbox/pods/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/log") {
			fmt.Fprint(w, "ok  \tbenchfixture\t0.01s\n")
			return
		}
		w.WriteHeader(404)
	})
	f.mux.HandleFunc("DELETE /", func(w http.ResponseWriter, r *http.Request) {
		f.deleted = append(f.deleted, r.URL.Path)
		fmt.Fprint(w, `{}`)
	})
	srv := httptest.NewServer(f.mux)
	return f, srv
}

func newTestSandbox(srv *httptest.Server) *K8sSandbox {
	s := NewK8sSandbox(&K8sClient{BaseURL: srv.URL, Token: "t", HTTPClient: srv.Client()}, "sandbox")
	s.Poll = time.Millisecond
	return s
}

func TestAllocateBuildsContractShapedJob(t *testing.T) {
	f, srv := newFakeAPI(t)
	defer srv.Close()
	s := newTestSandbox(srv)

	ref, err := s.Allocate(context.Background(), testSpec())
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if ref.JobName != "hf-task-hf-000abc-xyz" {
		t.Fatalf("job name = %q", ref.JobName)
	}
	if f.createdCM == nil || f.createdJob == nil {
		t.Fatal("configmap/job not created")
	}

	raw, _ := json.Marshal(f.createdJob)
	job := string(raw)
	for _, want := range []string{
		`"backoffLimit":0`, // no blind k8s retry — Cortex retries with context
		`"restartPolicy":"Never"`,
		`"serviceAccountName":"sandbox-runner"`,
		`"automountServiceAccountToken":false`, // zero k8s API reach from inside
		`"emptyDir":{}`,                        // fresh workspace by construction
		`"runAsNonRoot":true`,
		`"drop":["ALL"]`,
		`DIRTY WORKSPACE`, // the mechanical clean assertion
		`git clone --branch`,
		`"gate"`,
		"go build ./...", // gate command (json-escaped && checked separately)
		"go test ./...",
	} {
		if !strings.Contains(job, want) {
			t.Errorf("job spec missing %q", want)
		}
	}
	// The gate is a main container; guard and agent are init containers — the
	// sequence IS the lifecycle.
	spec := f.createdJob["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	inits := spec["initContainers"].([]any)
	if len(inits) != 2 {
		t.Fatalf("initContainers = %d, want 2", len(inits))
	}
	if inits[0].(map[string]any)["name"] != containerGuard || inits[1].(map[string]any)["name"] != containerAgent {
		t.Fatalf("init order wrong: %v", inits)
	}
}

// Gateless specs are legal only for legs gated elsewhere (reviewer via the
// Gitea review webhook); the job still runs an explicit no-op gate container
// so the sequence shape never silently changes.
func TestAllocateGatelessSpecGetsNoopGate(t *testing.T) {
	f, srv := newFakeAPI(t)
	defer srv.Close()
	s := newTestSandbox(srv)
	spec := testSpec()
	spec.GateCommand = ""
	if _, err := s.Allocate(context.Background(), spec); err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	raw, _ := json.Marshal(f.createdJob)
	if !strings.Contains(string(raw), "no-job-gate-for-this-role") {
		t.Fatal("gateless spec must run the explicit no-op gate container")
	}
}

func podListJSON(guardExit, agentExit, gateExit int) string {
	return fmt.Sprintf(`{"items":[{"metadata":{"name":"hf-task-pod-1"},"status":{
		"startTime":"2026-07-23T12:00:00Z",
		"initContainerStatuses":[
			{"name":"guard-checkout","state":{"terminated":{"exitCode":%d}}},
			{"name":"agent","state":{"terminated":{"exitCode":%d}}}],
		"containerStatuses":[
			{"name":"gate","state":{"terminated":{"exitCode":%d,"finishedAt":"2026-07-23T12:10:00Z"}}}]}}]}`,
		guardExit, agentExit, gateExit)
}

func TestWaitCollectsMechanicalResult(t *testing.T) {
	f, srv := newFakeAPI(t)
	defer srv.Close()
	s := newTestSandbox(srv)
	f.jobStatus = map[string]any{"succeeded": 1}
	f.podList = podListJSON(0, 0, 0)

	ref := Ref{Namespace: "sandbox", JobName: "hf-task-x", CMName: "hf-task-x-envelope"}
	res, err := s.Wait(context.Background(), &ref)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !res.CleanCheckOK || !res.CheckoutOK || res.AgentExitCode != 0 || res.GateExitCode != 0 || res.Phase != "succeeded" {
		t.Fatalf("result = %+v", res)
	}
	if ref.PodName != "hf-task-pod-1" {
		t.Fatalf("pod name not captured: %q", ref.PodName)
	}
}

func TestWaitSurfacesGateFailureAndDirtyWorkspace(t *testing.T) {
	f, srv := newFakeAPI(t)
	defer srv.Close()
	s := newTestSandbox(srv)
	f.jobStatus = map[string]any{"failed": 1}
	f.podList = podListJSON(guardExitDirty, 0, 1)

	ref := Ref{Namespace: "sandbox", JobName: "hf-task-y"}
	res, err := s.Wait(context.Background(), &ref)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.CleanCheckOK {
		t.Fatal("dirty workspace reported clean")
	}
	if res.GateExitCode != 1 || res.Phase != "failed" {
		t.Fatalf("result = %+v", res)
	}
}

func TestGateOutputReadsGateLog(t *testing.T) {
	f, srv := newFakeAPI(t)
	defer srv.Close()
	s := newTestSandbox(srv)
	f.jobStatus = map[string]any{"succeeded": 1}
	f.podList = podListJSON(0, 0, 0)
	ref := Ref{Namespace: "sandbox", JobName: "hf-task-z"}
	if _, err := s.Wait(context.Background(), &ref); err != nil {
		t.Fatal(err)
	}
	out, err := s.GateOutput(context.Background(), ref)
	if err != nil {
		t.Fatalf("GateOutput: %v", err)
	}
	if !strings.Contains(string(out), "benchfixture") {
		t.Fatalf("gate output = %q", out)
	}
}

func TestDestroyDeletesJobAndConfigMap(t *testing.T) {
	f, srv := newFakeAPI(t)
	defer srv.Close()
	s := newTestSandbox(srv)
	ref := Ref{Namespace: "sandbox", JobName: "hf-task-d", CMName: "hf-task-d-envelope"}
	if err := s.Destroy(context.Background(), ref); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	joined := strings.Join(f.deleted, " ")
	if !strings.Contains(joined, "/jobs/hf-task-d") || !strings.Contains(joined, "/configmaps/hf-task-d-envelope") {
		t.Fatalf("deleted = %v", f.deleted)
	}
}

func TestSanitizeName(t *testing.T) {
	if got := sanitizeName("hf-01ABC_def/ghi"); got != "hf-01abc-def-ghi" {
		t.Fatalf("sanitizeName = %q", got)
	}
}
