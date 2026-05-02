package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type nodeUsage struct {
	cpuMilli int64
	memKi    int64
}

func initK8s() *k8sState {
	tokenPath := "/var/run/secrets/kubernetes.io/serviceaccount/token"
	caPath := "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	nsPath := "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
	tok, tokErr := os.ReadFile(tokenPath)
	ca, caErr := os.ReadFile(caPath)
	ns, nsErr := os.ReadFile(nsPath)
	if tokErr != nil || caErr != nil || nsErr != nil {
		log.Printf("k8s integration disabled (serviceaccount files not found)")
		return &k8sState{enabled: false, podNS: "asgard"}
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		log.Printf("k8s integration disabled (invalid CA cert)")
		return &k8sState{enabled: false, podNS: "asgard"}
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	client := &http.Client{Timeout: 5 * time.Second, Transport: tr}
	namespace := strings.TrimSpace(string(ns))
	if namespace == "" {
		namespace = "asgard"
	}
	return &k8sState{enabled: true, client: client, token: strings.TrimSpace(string(tok)), podNS: namespace}
}

func (k *k8sState) setPods(p []PodInfo) {
	k.mu.Lock()
	k.pods = p
	k.mu.Unlock()
}

func (k *k8sState) setNodes(n []NodeInfo) {
	k.mu.Lock()
	k.nodes = n
	k.mu.Unlock()
}

func (k *k8sState) snapshotPods() []PodInfo {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]PodInfo, len(k.pods))
	copy(out, k.pods)
	return out
}

func (k *k8sState) snapshotNodes() []NodeInfo {
	k.mu.RLock()
	defer k.mu.RUnlock()
	out := make([]NodeInfo, len(k.nodes))
	copy(out, k.nodes)
	return out
}

func (k *k8sState) do(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, "https://kubernetes.default.svc"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return k.client.Do(req)
}

func (k *k8sState) get(path string) (map[string]interface{}, error) {
	resp, err := k.do(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (k *k8sState) getBytes(path string) (int, []byte, error) {
	resp, err := k.do(http.MethodGet, path, nil)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, body, nil
}

func (k *k8sState) getJSONWithStatus(path string) (int, map[string]interface{}, []byte, error) {
	status, body, err := k.getBytes(path)
	if err != nil {
		return 0, nil, nil, err
	}
	if status < 200 || status >= 300 {
		return status, nil, body, nil
	}
	var out map[string]interface{}
	if len(body) == 0 {
		return status, map[string]interface{}{}, body, nil
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return status, nil, body, err
	}
	return status, out, body, nil
}

func (k *k8sState) patchJSON(path string, payload interface{}) (int, []byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequest(http.MethodPatch, "https://kubernetes.default.svc"+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	req.Header.Set("Content-Type", "application/merge-patch+json")
	resp, err := k.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, respBody, nil
}

func parsePods(body map[string]interface{}) []PodInfo {
	items := asSlice(body["items"])
	pods := make([]PodInfo, 0, len(items))
	for _, item := range items {
		im := asMap(item)
		meta := asMap(im["metadata"])
		spec := asMap(im["spec"])
		status := asMap(im["status"])
		ready := false
		for _, c := range asSlice(status["conditions"]) {
			cm := asMap(c)
			if asString(cm["type"]) == "Ready" && asString(cm["status"]) == "True" {
				ready = true
				break
			}
		}
		restarts := int64(0)
		image := ""
		cs := asSlice(status["containerStatuses"])
		if len(cs) > 0 {
			first := asMap(cs[0])
			restarts = asInt64(first["restartCount"])
			image = asString(first["image"])
		}
		if image == "" {
			containers := asSlice(spec["containers"])
			if len(containers) > 0 {
				image = asString(asMap(containers[0])["image"])
			}
		}
		pods = append(pods, PodInfo{
			Name:      asString(meta["name"]),
			Namespace: asString(meta["namespace"]),
			Status:    asString(status["phase"]),
			Node:      asString(spec["nodeName"]),
			Restarts:  restarts,
			Age:       ageFrom(asString(meta["creationTimestamp"])),
			Ready:     ready,
			Image:     image,
		})
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	return pods
}

func parseNodes(body map[string]interface{}) []NodeInfo {
	items := asSlice(body["items"])
	nodes := make([]NodeInfo, 0, len(items))
	for _, item := range items {
		im := asMap(item)
		meta := asMap(im["metadata"])
		status := asMap(im["status"])
		labels := asMap(meta["labels"])
		roles := []string{}
		for k := range labels {
			if strings.HasPrefix(k, "node-role.kubernetes.io/") {
				r := strings.TrimPrefix(k, "node-role.kubernetes.io/")
				if r == "" {
					r = "control-plane"
				}
				roles = append(roles, r)
			}
		}
		if len(roles) == 0 {
			roles = []string{"worker"}
		}
		sort.Strings(roles)
		nodeStatus := "Unknown"
		for _, c := range asSlice(status["conditions"]) {
			cm := asMap(c)
			if asString(cm["type"]) == "Ready" {
				if asString(cm["status"]) == "True" {
					nodeStatus = "Ready"
				} else {
					nodeStatus = "NotReady"
				}
				break
			}
		}
		nodeInfo := asMap(status["nodeInfo"])
		alloc := asMap(status["allocatable"])
		nodes = append(nodes, NodeInfo{
			Name:              asString(meta["name"]),
			Status:            nodeStatus,
			Roles:             roles,
			KubeletVersion:    asString(nodeInfo["kubeletVersion"]),
			OS:                asString(nodeInfo["osImage"]),
			Architecture:      asString(nodeInfo["architecture"]),
			AllocatableCPU:    asString(alloc["cpu"]),
			AllocatableMemory: asString(alloc["memory"]),
		})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes
}

func parseCPUUsageNanoToMilli(q string) (int64, bool) {
	q = strings.TrimSpace(q)
	if q == "" {
		return 0, false
	}
	if !strings.HasSuffix(q, "n") {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSuffix(q, "n"), 10, 64)
	if err != nil {
		return 0, false
	}
	return v / 1_000_000, true
}

func parseCPUAllocMilli(q string) (int64, bool) {
	q = strings.TrimSpace(q)
	if q == "" {
		return 0, false
	}
	if strings.HasSuffix(q, "m") {
		v, err := strconv.ParseInt(strings.TrimSuffix(q, "m"), 10, 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	v, err := strconv.ParseInt(q, 10, 64)
	if err != nil {
		return 0, false
	}
	return v * 1000, true
}

func parseMemoryKi(q string) (int64, bool) {
	q = strings.TrimSpace(q)
	if q == "" || !strings.HasSuffix(q, "Ki") {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSuffix(q, "Ki"), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func parseNodeUsage(body map[string]interface{}) map[string]nodeUsage {
	items := asSlice(body["items"])
	out := make(map[string]nodeUsage, len(items))
	for _, item := range items {
		im := asMap(item)
		name := asString(asMap(im["metadata"])["name"])
		usage := asMap(im["usage"])
		cpuMilli, okCPU := parseCPUUsageNanoToMilli(asString(usage["cpu"]))
		memKi, okMem := parseMemoryKi(asString(usage["memory"]))
		if name == "" || !okCPU || !okMem {
			continue
		}
		out[name] = nodeUsage{cpuMilli: cpuMilli, memKi: memKi}
	}
	return out
}

func enrichNodesWithUsage(nodes []NodeInfo, usage map[string]nodeUsage) []NodeInfo {
	for i := range nodes {
		u, ok := usage[nodes[i].Name]
		if !ok {
			continue
		}
		if allocMilli, ok := parseCPUAllocMilli(nodes[i].AllocatableCPU); ok && allocMilli > 0 {
			nodes[i].CPUPercent = float64((u.cpuMilli * 100) / allocMilli)
		}
		if allocMemKi, ok := parseMemoryKi(nodes[i].AllocatableMemory); ok && allocMemKi > 0 {
			nodes[i].MemoryPercent = float64((u.memKi * 100) / allocMemKi)
		}
	}
	return nodes
}

func parseK8sEvents(body map[string]interface{}) []K8sEventInfo {
	items := asSlice(body["items"])
	events := make([]K8sEventInfo, 0, len(items))
	for _, item := range items {
		im := asMap(item)
		inv := asMap(im["involvedObject"])
		t := asString(im["lastTimestamp"])
		if t == "" {
			t = asString(im["eventTime"])
		}
		if t == "" {
			t = asString(im["firstTimestamp"])
		}
		events = append(events, K8sEventInfo{
			Type:    asString(im["type"]),
			Reason:  asString(im["reason"]),
			Object:  strings.ToLower(asString(inv["kind"])) + "/" + asString(inv["name"]),
			Message: asString(im["message"]),
			Time:    t,
			Count:   asInt64(im["count"]),
		})
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Time > events[j].Time })
	if len(events) > 50 {
		events = events[:50]
	}
	return events
}

func parsePodResources(body map[string]interface{}) []PodResourceInfo {
	items := asSlice(body["items"])
	out := make([]PodResourceInfo, 0, len(items))
	for _, item := range items {
		im := asMap(item)
		meta := asMap(im["metadata"])
		spec := asMap(im["spec"])
		info := PodResourceInfo{Pod: asString(meta["name"])}
		containers := asSlice(spec["containers"])
		for _, c := range containers {
			res := asMap(asMap(c)["resources"])
			req := asMap(res["requests"])
			lim := asMap(res["limits"])
			if info.CPURequest == "" {
				info.CPURequest = asString(req["cpu"])
			}
			if info.MemRequest == "" {
				info.MemRequest = asString(req["memory"])
			}
			if info.CPULimit == "" {
				info.CPULimit = asString(lim["cpu"])
			}
			if info.MemLimit == "" {
				info.MemLimit = asString(lim["memory"])
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pod < out[j].Pod })
	return out
}

func fetchGitOpsStatus(k8s *k8sState) gitopsStatusResponse {
	out := gitopsStatusResponse{App: "asgard", RecentDeployments: []gitopsDeploymentInfo{}}
	if k8s == nil || !k8s.enabled {
		log.Printf("gitops: kubernetes integration disabled")
		return out
	}
	status, body, rawBody, err := k8s.getJSONWithStatus("/apis/argoproj.io/v1alpha1/namespaces/argocd/applications/asgard")
	if err != nil {
		log.Printf("gitops: argocd request error: %v", err)
		return out
	}
	if status == http.StatusNotFound {
		log.Printf("gitops: argocd application 'asgard' not found (404)")
		return out
	}
	if status < 200 || status >= 300 {
		log.Printf("gitops: argocd returned status %d, body: %s", status, truncateRunes(string(rawBody), 256))
		return out
	}
	spec := asMap(body["spec"])
	appStatus := asMap(body["status"])
	sync := asMap(appStatus["sync"])
	health := asMap(appStatus["health"])
	operationState := asMap(appStatus["operationState"])
	syncPolicy := asMap(spec["syncPolicy"])
	automated := asMap(syncPolicy["automated"])
	history := asSlice(appStatus["history"])

	out.SyncStatus = asString(sync["status"])
	out.HealthStatus = asString(health["status"])
	out.DeployedSHA = shortSHA(asString(sync["revision"]))
	out.GitHeadSHA = shortSHA(asString(asMap(spec["source"])["targetRevision"]))
	out.Drift = strings.TrimSpace(out.SyncStatus) != "" && out.SyncStatus != "Synced"
	out.SelfHeal = asMap(syncPolicy["automated"]) != nil && (automated["selfHeal"] == true)
	if finished := asString(operationState["finishedAt"]); finished != "" {
		out.LastSync = finished
	} else if len(history) > 0 {
		out.LastSync = asString(asMap(history[0])["deployedAt"])
	}
	for _, item := range history {
		im := asMap(item)
		out.RecentDeployments = append(out.RecentDeployments, gitopsDeploymentInfo{
			Image:      "argocd",
			SHA:        shortSHA(asString(im["revision"])),
			DeployedAt: asString(im["deployedAt"]),
		})
		if len(out.RecentDeployments) == 5 {
			break
		}
	}
	log.Printf("gitops: synced=%s health=%s sha=%s last_sync=%s", out.SyncStatus, out.HealthStatus, out.DeployedSHA, out.LastSync)
	return out
}

func listCertExpiries(k8s *k8sState, namespace string) certsResponse {
	out := certsResponse{Certs: []certExpiryInfo{}}
	if k8s == nil || !k8s.enabled {
		log.Printf("certs: kubernetes integration disabled")
		return out
	}
	status, body, rawBody, err := k8s.getJSONWithStatus(fmt.Sprintf("/apis/cert-manager.io/v1/namespaces/%s/certificates", url.PathEscape(namespace)))
	if err != nil {
		log.Printf("certs: certificate request error: %v", err)
		return out
	}
	if status == http.StatusNotFound {
		log.Printf("certs: cert-manager certificates not found in namespace %s (404)", namespace)
		return out
	}
	if status < 200 || status >= 300 {
		log.Printf("certs: certificate request returned status %d, body: %s", status, truncateRunes(string(rawBody), 256))
		return out
	}
	for _, item := range asSlice(body["items"]) {
		im := asMap(item)
		meta := asMap(im["metadata"])
		statusMap := asMap(im["status"])
		expiry := asString(statusMap["notAfter"])
		daysRemaining := int64(0)
		if expiry != "" {
			if t, err := time.Parse(time.RFC3339, expiry); err == nil {
				daysRemaining = int64(time.Until(t).Hours() / 24)
			}
		}
		out.Certs = append(out.Certs, certExpiryInfo{
			Name:          asString(meta["name"]),
			Expiry:        expiry,
			DaysRemaining: daysRemaining,
		})
	}
	sort.Slice(out.Certs, func(i, j int) bool { return out.Certs[i].Name < out.Certs[j].Name })
	log.Printf("certs: returned %d certificates from namespace %s", len(out.Certs), namespace)
	return out
}

func updateAgentModel(k8s *k8sState, agentName, model string) error {
	if k8s == nil || !k8s.enabled {
		return fmt.Errorf("kubernetes integration disabled")
	}
	agentLabelSelector := fmt.Sprintf("%s.io/agent=%s", k8s.podNS, agentName)
	deployPath := fmt.Sprintf("/apis/apps/v1/namespaces/%s/deployments/%s", url.PathEscape(k8s.podNS), url.PathEscape(agentName))
	status, deploy, _, err := k8s.getJSONWithStatus(deployPath)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return os.ErrNotExist
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("deployment lookup returned %d", status)
	}
	updatedViaConfigMap := false
	if cmStatus, cmBody, _, err := k8s.getJSONWithStatus(fmt.Sprintf("/api/v1/namespaces/%s/configmaps", url.PathEscape(k8s.podNS))); err == nil && cmStatus >= 200 && cmStatus < 300 {
		for _, item := range asSlice(cmBody["items"]) {
			im := asMap(item)
			meta := asMap(im["metadata"])
			name := asString(meta["name"])
			labels := asMap(meta["labels"])
			if name == "" {
				continue
			}
			if labels["valhalla.io/agent"] != agentName && !strings.Contains(name, agentName) {
				continue
			}
			data := asMap(im["data"])
			patchData := map[string]string{}
			switch {
			case asString(data["model"]) != "":
				patchData["model"] = model
			case asString(data["MODEL"]) != "":
				patchData["MODEL"] = model
			case asString(data["args"]) != "":
				patchData["args"] = regexp.MustCompile(`--model=[^\s"]+`).ReplaceAllString(asString(data["args"]), "--model="+model)
			}
			if len(patchData) == 0 {
				continue
			}
			cmPath := fmt.Sprintf("/api/v1/namespaces/%s/configmaps/%s", url.PathEscape(k8s.podNS), url.PathEscape(name))
			patch := map[string]interface{}{"data": patchData}
			if patchStatus, patchBody, err := k8s.patchJSON(cmPath, patch); err != nil {
				return err
			} else if patchStatus < 200 || patchStatus >= 300 {
				return fmt.Errorf("configmap patch returned %d: %s", patchStatus, strings.TrimSpace(string(patchBody)))
			}
			updatedViaConfigMap = true
			break
		}
	}
	if !updatedViaConfigMap {
	}
	spec := asMap(deploy["spec"])
	template := asMap(spec["template"])
	podSpec := asMap(template["spec"])
	containers := asSlice(podSpec["containers"])
	if len(containers) == 0 {
		return fmt.Errorf("deployment has no containers")
	}
	container := asMap(containers[0])
	args := asSlice(container["args"])
	if len(args) > 0 {
		arg0 := asString(args[0])
		if arg0 != "" && strings.Contains(arg0, "--model=") {
			arg0 = regexp.MustCompile(`--model=[^\s"]+`).ReplaceAllString(arg0, "--model="+model)
			patch := map[string]interface{}{
				"spec": map[string]interface{}{
					"template": map[string]interface{}{
						"spec": map[string]interface{}{
							"containers": []map[string]interface{}{{
								"name": container["name"],
								"args": []string{arg0},
							}},
						},
					},
				},
			}
			if patchStatus, patchBody, err := k8s.patchJSON(deployPath, patch); err != nil {
				return err
			} else if patchStatus < 200 || patchStatus >= 300 {
				return fmt.Errorf("deployment patch returned %d: %s", patchStatus, strings.TrimSpace(string(patchBody)))
			}
		}
	}
	podName := ""
	if podStatus, podBody, _, err := k8s.getJSONWithStatus(fmt.Sprintf("/api/v1/namespaces/%s/pods?labelSelector=%s", url.PathEscape(k8s.podNS), url.QueryEscape(agentLabelSelector))); err == nil && podStatus >= 200 && podStatus < 300 {
		items := asSlice(podBody["items"])
		if len(items) > 0 {
			podName = asString(asMap(asMap(items[0])["metadata"])["name"])
		}
	}
	if podName == "" {
		if podStatus, podBody, _, err := k8s.getJSONWithStatus(fmt.Sprintf("/api/v1/namespaces/%s/pods", url.PathEscape(k8s.podNS))); err == nil && podStatus >= 200 && podStatus < 300 {
			for _, item := range asSlice(podBody["items"]) {
				name := asString(asMap(asMap(item)["metadata"])["name"])
				if strings.HasPrefix(name, agentName+"-") || name == agentName {
					podName = name
					break
				}
			}
		}
	}
	if podName != "" {
		resp, err := k8s.do(http.MethodDelete, fmt.Sprintf("/api/v1/namespaces/%s/pods/%s", url.PathEscape(k8s.podNS), url.PathEscape(podName)), nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			return fmt.Errorf("pod delete returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}
	}
	return nil
}

func (k *k8sState) refreshPods() error {
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods", url.PathEscape(k.podNS))
	body, err := k.get(path)
	if err != nil {
		k.setPods(nil)
		return err
	}
	k.setPods(parsePods(body))
	return nil
}

func (k *k8sState) refreshNodes() error {
	body, err := k.get("/api/v1/nodes")
	if err != nil {
		k.setNodes(nil)
		return err
	}
	nodes := parseNodes(body)
	metricsBody, err := k.get("/apis/metrics.k8s.io/v1beta1/nodes")
	if err == nil {
		nodes = enrichNodesWithUsage(nodes, parseNodeUsage(metricsBody))
	} else {
		log.Printf("k8s: node metrics fetch failed: %v", err)
	}
	k.setNodes(nodes)
	return nil
}

func podNameFromPath(path, action string) (string, bool) {
	const prefix = "/api/v1/cluster/pods/"
	suffix := "/" + action
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	name = strings.Trim(name, "/")
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}

func namespacedPodPath(path string) (namespace, pod, action string, ok bool) {
	const prefix = "/api/v1/cluster/pods/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", "", false
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(path, prefix), "/"), "/")
	switch len(parts) {
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return "", "", "", false
		}
		return parts[0], parts[1], "", true
	case 3:
		if parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return "", "", "", false
		}
		return parts[0], parts[1], parts[2], true
	default:
		return "", "", "", false
	}
}

func registerClusterRoutes(mux *http.ServeMux, gw *gateway) {
	mux.HandleFunc("/api/v1/cluster/pods", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if gw.k8s == nil || !gw.k8s.enabled {
			writeJSON(w, http.StatusOK, []PodInfo{})
			return
		}
		writeJSON(w, http.StatusOK, gw.k8s.snapshotPods())
	})
	mux.HandleFunc("/api/v1/cluster/pods/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			namespace, podName, action, ok := namespacedPodPath(r.URL.Path)
			if ok && action == "logs" {
				if gw.k8s == nil || !gw.k8s.enabled {
					writeJSON(w, http.StatusBadGateway, map[string]string{"error": "kubernetes integration disabled"})
					return
				}
				tail := 50
				if raw := strings.TrimSpace(r.URL.Query().Get("tail")); raw != "" {
					v, err := strconv.Atoi(raw)
					if err != nil {
						writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tail parameter"})
						return
					}
					tail = clampInt(v, 50, 500)
				}
				status, body, err := gw.k8s.getBytes(fmt.Sprintf(
					"/api/v1/namespaces/%s/pods/%s/log?tailLines=%d&timestamps=true",
					url.PathEscape(namespace),
					url.PathEscape(podName),
					tail,
				))
				if err != nil {
					writeJSON(w, http.StatusBadGateway, map[string]string{"error": "kubernetes request failed"})
					return
				}
				if status == http.StatusNotFound {
					writeJSON(w, http.StatusNotFound, map[string]string{"error": "pod not found"})
					return
				}
				if status < 200 || status >= 300 {
					msg := strings.TrimSpace(string(body))
					if strings.Contains(msg, "PodInitializing") || strings.Contains(msg, "ContainerCreating") {
						writeJSON(w, http.StatusOK, podLogsResponse{Pod: podName, Logs: ""})
						return
					}
					writeJSON(w, http.StatusBadGateway, map[string]string{"error": msg})
					return
				}
				writeJSON(w, http.StatusOK, podLogsResponse{Pod: podName, Logs: string(body)})
				return
			}
		case strings.HasSuffix(r.URL.Path, "/logs") && strings.HasPrefix(r.URL.Path, "/api/v1/cluster/pods/"):
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if gw.k8s == nil || !gw.k8s.enabled {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "kubernetes integration disabled"})
				return
			}
			podName, ok := podNameFromPath(r.URL.Path, "logs")
			if !ok {
				http.NotFound(w, r)
				return
			}
			lines := 100
			if raw := strings.TrimSpace(r.URL.Query().Get("lines")); raw != "" {
				v, err := strconv.Atoi(raw)
				if err != nil || v <= 0 {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid lines parameter"})
					return
				}
				lines = v
			}
			namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
			if namespace == "" {
				namespace = gw.k8s.podNS
			}
			path := fmt.Sprintf(
				"/api/v1/namespaces/%s/pods/%s/log?tailLines=%d&timestamps=true",
				url.PathEscape(namespace),
				url.PathEscape(podName),
				lines,
			)
			resp, err := gw.k8s.do(http.MethodGet, path, nil)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "kubernetes request failed"})
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "pod not found"})
				return
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(b))})
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			_, _ = io.Copy(w, resp.Body)
			return

		case r.Method == http.MethodDelete:
			namespace, podName, action, ok := namespacedPodPath(r.URL.Path)
			if !ok || action != "" {
				http.NotFound(w, r)
				return
			}
			if gw.k8s == nil || !gw.k8s.enabled {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "kubernetes integration disabled"})
				return
			}
			resp, err := gw.k8s.do(http.MethodDelete, fmt.Sprintf("/api/v1/namespaces/%s/pods/%s", url.PathEscape(namespace), url.PathEscape(podName)), nil)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "kubernetes request failed"})
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "pod not found"})
				return
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(b))})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"pod": podName, "status": "deleted"})
			return

		case strings.HasSuffix(r.URL.Path, "/restart") && strings.HasPrefix(r.URL.Path, "/api/v1/cluster/pods/"):
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if gw.k8s == nil || !gw.k8s.enabled {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "kubernetes integration disabled"})
				return
			}
			podName, ok := podNameFromPath(r.URL.Path, "restart")
			if !ok {
				http.NotFound(w, r)
				return
			}
			path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s", url.PathEscape(gw.k8s.podNS), url.PathEscape(podName))
			resp, err := gw.k8s.do(http.MethodDelete, path, nil)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "kubernetes request failed"})
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "pod not found"})
				return
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(b))})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{
				"status":  "ok",
				"message": fmt.Sprintf("Pod %s deleted, deployment will recreate", podName),
			})
			return
		default:
			http.NotFound(w, r)
			return
		}
	})
	mux.HandleFunc("/api/v1/cluster/nodes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if gw.k8s == nil || !gw.k8s.enabled {
			writeJSON(w, http.StatusOK, []NodeInfo{})
			return
		}
		writeJSON(w, http.StatusOK, gw.k8s.snapshotNodes())
	})
	mux.HandleFunc("/api/v1/cluster/certs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, listCertExpiries(gw.k8s, gw.k8s.podNS))
	})
	mux.HandleFunc("/api/v1/gitops/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, fetchGitOpsStatus(gw.k8s))
	})
	mux.HandleFunc("/api/v1/gitops/sync", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if gw.k8s == nil || !gw.k8s.enabled {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "kubernetes integration disabled"})
			return
		}
		status, body, err := gw.k8s.patchJSON("/apis/argoproj.io/v1alpha1/namespaces/argocd/applications/asgard", map[string]interface{}{
			"metadata": map[string]interface{}{
				"annotations": map[string]string{
					"argocd.argoproj.io/refresh": "hard",
				},
			},
		})
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		if status == http.StatusNotFound {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "ArgoCD application CRD not found"})
			return
		}
		if status < 200 || status >= 300 {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(string(body))})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "sync triggered"})
	})
	mux.HandleFunc("/api/v1/k8s/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if gw.k8s == nil || !gw.k8s.enabled {
			writeJSON(w, http.StatusOK, []K8sEventInfo{})
			return
		}
		body, err := gw.k8s.get(fmt.Sprintf("/api/v1/namespaces/%s/events", url.PathEscape(gw.k8s.podNS)))
		if err != nil {
			log.Printf("k8s events: fetch error: %v", err)
			writeJSON(w, http.StatusOK, []K8sEventInfo{})
			return
		}
		parsed := parseK8sEvents(body)
		log.Printf("k8s events: returned %d events from namespace %s", len(parsed), gw.k8s.podNS)
		writeJSON(w, http.StatusOK, parsed)
	})
	mux.HandleFunc("/api/v1/k8s/resources", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if gw.k8s == nil || !gw.k8s.enabled {
			writeJSON(w, http.StatusOK, []PodResourceInfo{})
			return
		}
		body, err := gw.k8s.get(fmt.Sprintf("/api/v1/namespaces/%s/pods", url.PathEscape(gw.k8s.podNS)))
		if err != nil {
			writeJSON(w, http.StatusOK, []PodResourceInfo{})
			return
		}
		writeJSON(w, http.StatusOK, parsePodResources(body))
	})
}
