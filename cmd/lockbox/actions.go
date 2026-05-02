package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	calendarv3 "google.golang.org/api/calendar/v3"
	gmailv1 "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

type MockService struct{}

func (m *MockService) Name() string { return "mock" }

func (m *MockService) SupportedActions() []string {
	return []string{"read_data", "write_data"}
}

func (m *MockService) Execute(action string, params map[string]interface{}, cred ServiceCredential) (interface{}, error) {
	return map[string]interface{}{
		"action":    action,
		"params":    params,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"mock":      true,
	}, nil
}

func (m *MockService) IsWriteAction(action string) bool {
	return action == "write_data"
}

type oauthTokenFile struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	Expiry       string `json:"expiry,omitempty"`
}

type googleClients struct {
	http     *http.Client
	gmail    *gmailv1.Service
	calendar *calendarv3.Service
}

func toJSONString(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func strParam(params map[string]interface{}, key string, required bool) (string, error) {
	raw, ok := params[key]
	if !ok {
		if required {
			return "", fmt.Errorf("missing %s", key)
		}
		return "", nil
	}
	val, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	val = strings.TrimSpace(val)
	if required && val == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return val, nil
}

func intParam(params map[string]interface{}, key string, fallback int) int {
	raw, ok := params[key]
	if !ok {
		return fallback
	}
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return fallback
	}
}

func saveOAuthToken(path string, token *oauth2.Token) error {
	payload := oauthTokenFile{
		AccessToken:  token.AccessToken,
		TokenType:    token.TokenType,
		RefreshToken: token.RefreshToken,
	}
	if !token.Expiry.IsZero() {
		payload.Expiry = token.Expiry.UTC().Format(time.RFC3339)
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func loadOAuthToken(path string) (*oauth2.Token, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var payload oauthTokenFile
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	token := &oauth2.Token{
		AccessToken:  strings.TrimSpace(payload.AccessToken),
		TokenType:    strings.TrimSpace(payload.TokenType),
		RefreshToken: strings.TrimSpace(payload.RefreshToken),
	}
	if payload.Expiry != "" {
		if t, err := time.Parse(time.RFC3339, payload.Expiry); err == nil {
			token.Expiry = t
		}
	}
	return token, nil
}

func setupGoogleClients(ctx context.Context, clientID, clientSecret, tokenFile string) (*googleClients, error) {
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	tokenFile = strings.TrimSpace(tokenFile)
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("google OAuth credentials are required")
	}
	if tokenFile == "" {
		tokenFile = "/vault/secrets/google-token.json"
	}
	config := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     google.Endpoint,
		Scopes: []string{
			gmailv1.GmailReadonlyScope,
			gmailv1.GmailModifyScope,
			gmailv1.GmailSendScope,
			calendarv3.CalendarReadonlyScope,
			calendarv3.CalendarEventsScope,
		},
		RedirectURL: "urn:ietf:wg:oauth:2.0:oob",
	}

	token, err := loadOAuthToken(tokenFile)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read token file: %w", err)
		}
		authURL := config.AuthCodeURL("lockbox-offline", oauth2.AccessTypeOffline, oauth2.ApprovalForce)
		fmt.Printf("Lockbox Google OAuth setup required.\nOpen this URL in your browser and authorize:\n%s\n\nPaste authorization code: ", authURL)
		var code string
		if _, scanErr := fmt.Scanln(&code); scanErr != nil {
			return nil, fmt.Errorf("read authorization code: %w", scanErr)
		}
		token, err = config.Exchange(ctx, strings.TrimSpace(code))
		if err != nil {
			return nil, fmt.Errorf("exchange auth code: %w", err)
		}
		if err := saveOAuthToken(tokenFile, token); err != nil {
			return nil, fmt.Errorf("save token file: %w", err)
		}
	}

	tokenSource := config.TokenSource(ctx, token)
	refreshed, err := tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = token.RefreshToken
	}
	if err := saveOAuthToken(tokenFile, refreshed); err != nil {
		return nil, fmt.Errorf("persist refreshed token: %w", err)
	}
	httpClient := oauth2.NewClient(ctx, oauth2.ReuseTokenSource(refreshed, tokenSource))
	gmailSvc, err := gmailv1.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("gmail client: %w", err)
	}
	calendarSvc, err := calendarv3.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("calendar client: %w", err)
	}
	return &googleClients{http: httpClient, gmail: gmailSvc, calendar: calendarSvc}, nil
}

func decodeBodyPart(payload *gmailv1.MessagePart) string {
	if payload == nil {
		return ""
	}
	if payload.Body != nil && payload.Body.Data != "" {
		if decoded, err := base64.URLEncoding.DecodeString(payload.Body.Data); err == nil {
			return string(decoded)
		}
	}
	for _, part := range payload.Parts {
		if strings.HasPrefix(part.MimeType, "text/plain") || part.MimeType == "" {
			if body := decodeBodyPart(part); strings.TrimSpace(body) != "" {
				return body
			}
		}
	}
	return ""
}

func headerValue(payload *gmailv1.MessagePart, name string) string {
	if payload == nil {
		return ""
	}
	for _, h := range payload.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

func registerGoogleTools(state *lockboxState, clients *googleClients) {
	state.tools["gmail_list_inbox"] = ToolHandler{
		Name:        "gmail_list_inbox",
		Description: "List inbox emails",
		WriteTier:   "read",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"max_results": map[string]interface{}{"type": "integer"},
			},
		},
		Handler: func(params map[string]interface{}) (interface{}, error) {
			maxResults := intParam(params, "max_results", 10)
			if maxResults <= 0 {
				maxResults = 10
			}
			resp, err := clients.gmail.Users.Messages.List("me").LabelIds("INBOX").MaxResults(int64(maxResults)).Do()
			if err != nil {
				return nil, err
			}
			items := make([]map[string]interface{}, 0, len(resp.Messages))
			for _, m := range resp.Messages {
				msg, err := clients.gmail.Users.Messages.Get("me", m.Id).Format("metadata").MetadataHeaders("From", "Subject", "Date").Do()
				if err != nil {
					continue
				}
				items = append(items, map[string]interface{}{
					"id":      msg.Id,
					"from":    headerValue(msg.Payload, "From"),
					"subject": headerValue(msg.Payload, "Subject"),
					"snippet": msg.Snippet,
					"date":    headerValue(msg.Payload, "Date"),
				})
			}
			return items, nil
		},
	}

	state.tools["gmail_read_email"] = ToolHandler{
		Name:        "gmail_read_email",
		Description: "Read one email by ID",
		WriteTier:   "read",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id": map[string]interface{}{"type": "string"},
			},
			"required": []string{"id"},
		},
		Handler: func(params map[string]interface{}) (interface{}, error) {
			id, err := strParam(params, "id", true)
			if err != nil {
				return nil, err
			}
			msg, err := clients.gmail.Users.Messages.Get("me", id).Format("full").Do()
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{
				"from":    headerValue(msg.Payload, "From"),
				"to":      headerValue(msg.Payload, "To"),
				"subject": headerValue(msg.Payload, "Subject"),
				"body":    decodeBodyPart(msg.Payload),
				"date":    headerValue(msg.Payload, "Date"),
			}, nil
		},
	}

	state.tools["gmail_archive_email"] = ToolHandler{
		Name:        "gmail_archive_email",
		Description: "Archive one email by removing INBOX label",
		WriteTier:   "safe_write",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id": map[string]interface{}{"type": "string"},
			},
			"required": []string{"id"},
		},
		Handler: func(params map[string]interface{}) (interface{}, error) {
			id, err := strParam(params, "id", true)
			if err != nil {
				return nil, err
			}
			_, err = clients.gmail.Users.Messages.Modify("me", id, &gmailv1.ModifyMessageRequest{
				RemoveLabelIds: []string{"INBOX"},
			}).Do()
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"success": true}, nil
		},
	}

	state.tools["gmail_send_email"] = ToolHandler{
		Name:        "gmail_send_email",
		Description: "Send an email",
		WriteTier:   "destructive_write",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"to":      map[string]interface{}{"type": "string"},
				"subject": map[string]interface{}{"type": "string"},
				"body":    map[string]interface{}{"type": "string"},
			},
			"required": []string{"to", "subject", "body"},
		},
		Handler: func(params map[string]interface{}) (interface{}, error) {
			to, err := strParam(params, "to", true)
			if err != nil {
				return nil, err
			}
			subject, err := strParam(params, "subject", true)
			if err != nil {
				return nil, err
			}
			body, err := strParam(params, "body", true)
			if err != nil {
				return nil, err
			}
			msg := fmt.Sprintf("To: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s", to, subject, body)
			encoded := base64.URLEncoding.EncodeToString([]byte(msg))
			resp, err := clients.gmail.Users.Messages.Send("me", &gmailv1.Message{Raw: encoded}).Do()
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"id": resp.Id, "success": true}, nil
		},
	}

	state.tools["calendar_list_events"] = ToolHandler{
		Name:        "calendar_list_events",
		Description: "List upcoming calendar events",
		WriteTier:   "read",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"days_ahead": map[string]interface{}{"type": "integer"},
			},
		},
		Handler: func(params map[string]interface{}) (interface{}, error) {
			daysAhead := intParam(params, "days_ahead", 7)
			if daysAhead <= 0 {
				daysAhead = 7
			}
			start := time.Now().UTC()
			end := start.Add(time.Duration(daysAhead) * 24 * time.Hour)
			resp, err := clients.calendar.Events.List("primary").
				ShowDeleted(false).
				SingleEvents(true).
				OrderBy("startTime").
				TimeMin(start.Format(time.RFC3339)).
				TimeMax(end.Format(time.RFC3339)).
				Do()
			if err != nil {
				return nil, err
			}
			items := make([]map[string]interface{}, 0, len(resp.Items))
			for _, e := range resp.Items {
				startVal := e.Start.DateTime
				if startVal == "" {
					startVal = e.Start.Date
				}
				endVal := e.End.DateTime
				if endVal == "" {
					endVal = e.End.Date
				}
				items = append(items, map[string]interface{}{
					"id":       e.Id,
					"summary":  e.Summary,
					"start":    startVal,
					"end":      endVal,
					"location": e.Location,
				})
			}
			return items, nil
		},
	}

	state.tools["calendar_create_event"] = ToolHandler{
		Name:        "calendar_create_event",
		Description: "Create a calendar event",
		WriteTier:   "safe_write",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"summary":     map[string]interface{}{"type": "string"},
				"start":       map[string]interface{}{"type": "string"},
				"end":         map[string]interface{}{"type": "string"},
				"location":    map[string]interface{}{"type": "string"},
				"description": map[string]interface{}{"type": "string"},
			},
			"required": []string{"summary", "start", "end"},
		},
		Handler: func(params map[string]interface{}) (interface{}, error) {
			summary, err := strParam(params, "summary", true)
			if err != nil {
				return nil, err
			}
			start, err := strParam(params, "start", true)
			if err != nil {
				return nil, err
			}
			end, err := strParam(params, "end", true)
			if err != nil {
				return nil, err
			}
			location, _ := strParam(params, "location", false)
			description, _ := strParam(params, "description", false)
			event := &calendarv3.Event{
				Summary:     summary,
				Location:    location,
				Description: description,
				Start:       &calendarv3.EventDateTime{DateTime: start},
				End:         &calendarv3.EventDateTime{DateTime: end},
			}
			created, err := clients.calendar.Events.Insert("primary", event).Do()
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{
				"id":   created.Id,
				"link": created.HtmlLink,
			}, nil
		},
	}

	state.tools["api_call"] = ToolHandler{
		Name:        "api_call",
		Description: "Make an authenticated API call through lockbox",
		WriteTier:   "destructive_write",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"service": map[string]interface{}{"type": "string"},
				"method":  map[string]interface{}{"type": "string"},
				"path":    map[string]interface{}{"type": "string"},
				"body":    map[string]interface{}{"type": "string"},
			},
			"required": []string{"service", "method", "path"},
		},
		Handler: func(params map[string]interface{}) (interface{}, error) {
			service, err := strParam(params, "service", true)
			if err != nil {
				return nil, err
			}
			method, err := strParam(params, "method", true)
			if err != nil {
				return nil, err
			}
			path, err := strParam(params, "path", true)
			if err != nil {
				return nil, err
			}
			body, _ := strParam(params, "body", false)

			var baseURL string
			switch strings.ToLower(service) {
			case "gmail":
				baseURL = "https://gmail.googleapis.com"
			case "calendar":
				baseURL = "https://www.googleapis.com"
			default:
				return nil, fmt.Errorf("unsupported service: %s", service)
			}

			method = strings.ToUpper(method)
			switch method {
			case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete:
			default:
				return nil, fmt.Errorf("unsupported method: %s", method)
			}

			if path == "" {
				return nil, fmt.Errorf("path is required")
			}
			url := strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(path, "/")
			req, err := http.NewRequestWithContext(context.Background(), method, url, strings.NewReader(body))
			if err != nil {
				return nil, fmt.Errorf("create request: %w", err)
			}
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			resp, err := clients.http.Do(req)
			if err != nil {
				return nil, fmt.Errorf("request failed: %w", err)
			}
			defer resp.Body.Close()

			data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if err != nil {
				return nil, fmt.Errorf("read response: %w", err)
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return nil, fmt.Errorf("api status %d: %s", resp.StatusCode, string(data))
			}
			return string(data), nil
		},
	}
}
