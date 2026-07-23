package sandbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// buildConfigMap holds the envelope. Per-task, deleted with the sandbox.
func buildConfigMap(ref Ref, spec RunSpec) map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      ref.CMName,
			"namespace": ref.Namespace,
			"labels":    map[string]string{labelTaskID: sanitizeName(spec.TaskID), labelManagedBy: managedByValue},
		},
		"data": map[string]string{"envelope.json": string(spec.Envelope)},
	}
}

// buildJob renders the per-task Job per SANDBOX_LIFECYCLE.md: one fresh pod,
// one fresh emptyDir, the container sequence guard+checkout → agent → gate.
// Isolation: sandbox-runner SA with no token automounted, non-root, no
// privilege escalation, all caps dropped, seccomp RuntimeDefault.
func buildJob(ref Ref, spec RunSpec) map[string]any {
	guardScript := fmt.Sprintf(
		`set -u
if [ -n "$(ls -A %s 2>/dev/null)" ]; then echo "DIRTY WORKSPACE — prior-task residue"; exit %d; fi
echo "workspace clean"
git clone --branch %q %q %s/repo || exit %d
cd %s/repo && git checkout -b %q && echo "checkout ok: $(git rev-parse HEAD)"`,
		workspaceMountPath, guardExitDirty,
		spec.BaseBranch, spec.CloneURL, workspaceMountPath, guardExitCloneFail,
		workspaceMountPath, spec.WorkBranch)

	gateScript := fmt.Sprintf("cd %s/repo && %s", workspaceMountPath, spec.GateCommand)
	if spec.GateCommand == "" {
		gateScript = "echo no-job-gate-for-this-role"
	}

	volumes := []map[string]any{
		{"name": "work", "emptyDir": map[string]any{}},
		{"name": "task", "configMap": map[string]any{"name": ref.CMName}},
	}
	volumeMounts := []map[string]any{
		{"name": "work", "mountPath": workspaceMountPath},
		{"name": "task", "mountPath": envelopeMountPath, "readOnly": true},
	}
	if spec.CredSecret != "" {
		volumes = append(volumes, map[string]any{
			"name": "gitcred", "secret": map[string]any{"secretName": spec.CredSecret},
		})
		volumeMounts = append(volumeMounts, map[string]any{
			"name": "gitcred", "mountPath": "/task-cred", "readOnly": true,
		})
	}

	securityContext := map[string]any{
		"runAsNonRoot":             true,
		"runAsUser":                1000,
		"runAsGroup":               1000,
		"allowPrivilegeEscalation": false,
		"capabilities":             map[string]any{"drop": []string{"ALL"}},
		"seccompProfile":           map[string]any{"type": "RuntimeDefault"},
	}

	container := func(name string, command []string) map[string]any {
		return map[string]any{
			"name":            name,
			"image":           spec.AgentImage,
			"command":         command,
			"volumeMounts":    volumeMounts,
			"securityContext": securityContext,
			"workingDir":      workspaceMountPath,
		}
	}

	deadlineSecs := int64(3600)
	if spec.Deadline > 0 {
		deadlineSecs = int64(spec.Deadline.Seconds())
	}

	return map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":      ref.JobName,
			"namespace": ref.Namespace,
			"labels":    map[string]string{labelTaskID: sanitizeName(spec.TaskID), labelManagedBy: managedByValue},
		},
		"spec": map[string]any{
			"backoffLimit":            0, // retry is Cortex's decision with failure context, never k8s's blind restart
			"ttlSecondsAfterFinished": 3600,
			"activeDeadlineSeconds":   deadlineSecs,
			"template": map[string]any{
				"metadata": map[string]any{
					"labels": map[string]string{labelTaskID: sanitizeName(spec.TaskID), labelManagedBy: managedByValue},
				},
				"spec": map[string]any{
					"restartPolicy":                "Never",
					"serviceAccountName":           "sandbox-runner",
					"automountServiceAccountToken": false,
					"initContainers": []map[string]any{
						container(containerGuard, []string{"sh", "-c", guardScript}),
						container(containerAgent, spec.AgentCommand),
					},
					"containers": []map[string]any{
						container(containerGate, []string{"sh", "-c", gateScript}),
					},
					"volumes": volumes,
				},
			},
		},
	}
}

func jsonBody(v any) io.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

func readCapped(r io.Reader, cap int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, cap+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > cap {
		data = data[:cap]
		data = append(data, []byte("\n…[truncated]")...)
	}
	return data, nil
}

// String renders a ref for logs.
func (r Ref) String() string {
	return strings.TrimSuffix(r.Namespace+"/"+r.JobName, "/")
}
