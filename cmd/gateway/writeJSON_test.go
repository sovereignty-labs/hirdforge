package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteJSON(t *testing.T) {
	tests := []struct {
		name           string
		status         int
		payload        interface{}
		wantStatus     int
		wantContentType string
		wantBody       interface{}
	}{
		{
			name:       "valid struct with 200 status",
			status:     http.StatusOK,
			payload:    struct{ Message string }{Message: "success"},
			wantStatus: http.StatusOK,
			wantContentType: "application/json",
			wantBody:   map[string]string{"Message": "success"},
		},
		{
			name:       "empty struct",
			status:     http.StatusOK,
			payload:    struct{}{},
			wantStatus: http.StatusOK,
			wantContentType: "application/json",
			wantBody:   map[string]interface{}{},
		},
		{
			name:       "custom status code 404",
			status:     http.StatusNotFound,
			payload:    map[string]string{"error": "not found"},
			wantStatus: http.StatusNotFound,
			wantContentType: "application/json",
			wantBody:   map[string]string{"error": "not found"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			writeJSON(rr, tt.status, tt.payload)

			// Check status code
			if rr.Code != tt.wantStatus {
				t.Errorf("writeJSON() status = %d, want %d", rr.Code, tt.wantStatus)
			}

			// Check Content-Type header
			contentType := rr.Header().Get("Content-Type")
			if contentType != tt.wantContentType {
				t.Errorf("writeJSON() Content-Type = %q, want %q", contentType, tt.wantContentType)
			}

			// Check body
			var gotBody interface{}
			if err := json.NewDecoder(rr.Body).Decode(&gotBody); err != nil {
				t.Errorf("writeJSON() body decode error: %v", err)
			}
			if !jsonDeepEqual(gotBody, tt.wantBody) {
				t.Errorf("writeJSON() body = %v, want %v", gotBody, tt.wantBody)
			}
		})
	}
}

// jsonDeepEqual compares two JSON-decoded values for equality.
func jsonDeepEqual(a, b interface{}) bool {
	aJSON, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bJSON, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(aJSON) == string(bJSON)
}
