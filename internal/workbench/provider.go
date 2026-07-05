package workbench

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ProviderState is the sanitized view of the configured model provider. The
// raw API key is never exposed through this type.
type ProviderState struct {
	BaseURL   string `json:"base_url"`
	APIKeySet bool   `json:"api_key_set"`
	Model     string `json:"model"`
}

// providerConfig is the internal record stored for the configured provider.
// The raw API key lives here and must never be returned in any response.
type providerConfig struct {
	baseURL string
	apiKey  string
	model   string
}

// providerState holds the currently configured provider, if any. A nil cfg
// means no provider is configured.
type providerState struct {
	mu  sync.Mutex
	cfg *providerConfig
}

func newProviderState() *providerState {
	return &providerState{}
}

// Get returns a sanitized snapshot of the provider state, or nil if no
// provider is configured.
func (p *providerState) Get() *ProviderState {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cfg == nil {
		return nil
	}
	return &ProviderState{
		BaseURL:   p.cfg.baseURL,
		APIKeySet: p.cfg.apiKey != "",
		Model:     p.cfg.model,
	}
}

// Config returns a copy of the internal configuration including the raw API
// key. It is intended for the test endpoint only and the result must not be
// exposed to API clients.
func (p *providerState) Config() *providerConfig {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cfg == nil {
		return nil
	}
	return &providerConfig{
		baseURL: p.cfg.baseURL,
		apiKey:  p.cfg.apiKey,
		model:   p.cfg.model,
	}
}

func (p *providerState) Set(cfg providerConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg = &cfg
}

func (wb *Server) handleProviderRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		state := wb.provider.Get()
		if state == nil {
			http.Error(w, "no provider configured", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, *state)
	case http.MethodPost:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var in struct {
			BaseURL string `json:"base_url"`
			APIKey  string `json:"api_key"`
			Model   string `json:"model"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if in.BaseURL == "" {
			http.Error(w, "base_url is required", http.StatusBadRequest)
			return
		}
		if in.APIKey == "" {
			http.Error(w, "api_key is required", http.StatusBadRequest)
			return
		}
		if in.Model == "" {
			http.Error(w, "model is required", http.StatusBadRequest)
			return
		}
		u, err := url.Parse(in.BaseURL)
		if err != nil {
			http.Error(w, "invalid base_url: "+err.Error(), http.StatusBadRequest)
			return
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			http.Error(w, "base_url must be http or https", http.StatusBadRequest)
			return
		}
		if u.Host == "" {
			http.Error(w, "base_url must have a host", http.StatusBadRequest)
			return
		}

		wb.provider.Set(providerConfig{
			baseURL: in.BaseURL,
			apiKey:  in.APIKey,
			model:   in.Model,
		})

		state := ProviderState{
			BaseURL:   in.BaseURL,
			APIKeySet: true,
			Model:     in.Model,
		}
		data, err := json.Marshal(state)
		if err != nil {
			http.Error(w, "marshal state: "+err.Error(), http.StatusInternalServerError)
			return
		}
		wb.store.Append("provider.configured", "Configured provider model "+in.Model, data)
		writeJSON(w, http.StatusCreated, state)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

const providerTestTimeout = 20 * time.Second

func (wb *Server) handleProviderTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := wb.provider.Config()
	if cfg == nil {
		http.Error(w, "no provider configured", http.StatusNotFound)
		return
	}

	endpoint := strings.TrimRight(cfg.baseURL, "/") + "/chat/completions"
	reqBody, err := json.Marshal(map[string]any{
		"model": cfg.model,
		"messages": []map[string]string{
			{"role": "user", "content": "Reply with exactly: hirdforge provider online"},
		},
		"temperature": 0,
	})
	if err != nil {
		http.Error(w, "marshal request: "+err.Error(), http.StatusInternalServerError)
		return
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(reqBody)))
	if err != nil {
		wb.store.Append("provider.test_failed", "Provider test failed", nil)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok":    false,
			"error": "build request: " + err.Error(),
		})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.apiKey)

	client := &http.Client{Timeout: providerTestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		wb.store.Append("provider.test_failed", "Provider test failed", nil)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		wb.store.Append("provider.test_failed", "Provider test failed", nil)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok":    false,
			"error": "read response: " + err.Error(),
		})
		return
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		wb.store.Append("provider.tested", "Provider test succeeded", nil)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"status": resp.StatusCode,
			"model":  cfg.model,
		})
		return
	}

	errMsg := string(respBody)
	if len(errMsg) > 1000 {
		errMsg = errMsg[:1000]
	}
	wb.store.Append("provider.test_failed", "Provider test failed", nil)
	writeJSON(w, http.StatusBadGateway, map[string]any{
		"ok":     false,
		"status": resp.StatusCode,
		"error":  errMsg,
	})
}
