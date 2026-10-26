package tunnel

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

func TestCreateExtendCloseSession(t *testing.T) {
	tm := NewTunnelManager("test-jwt-secret")

	session, err := tm.CreateSession("project-123", "user-alice", 5173, 10)
	if err != nil {
		t.Fatalf("unexpected error creating session: %v", err)
	}

	if session.ProjectID != "project-123" {
		t.Errorf("expected project-123, got %s", session.ProjectID)
	}
	if session.Port != 5173 {
		t.Errorf("expected port 5173, got %d", session.Port)
	}
	if session.Token == "" {
		t.Error("expected non-empty session token")
	}
	if !session.Active {
		t.Error("expected session to be active")
	}

	// Verify lookup
	found, ok := tm.GetSession(session.ID)
	if !ok || found.ID != session.ID {
		t.Fatalf("could not retrieve session by ID")
	}

	// Extend session
	oldExpires := session.ExpiresAt
	extended, err := tm.ExtendSession(session.ID, "user-alice", 15)
	if err != nil {
		t.Fatalf("unexpected error extending session: %v", err)
	}
	if !extended.ExpiresAt.After(oldExpires) {
		t.Errorf("expected expiresAt to be after old expiresAt")
	}

	// Unauthorized extend
	_, err = tm.ExtendSession(session.ID, "user-bob", 10)
	if err == nil {
		t.Errorf("expected error when unauthorized user extends session")
	}

	// Close session
	err = tm.CloseSession(session.ID, "user-alice")
	if err != nil {
		t.Fatalf("unexpected error closing session: %v", err)
	}

	// Lookup after close
	_, ok = tm.GetSession(session.ID)
	if ok {
		t.Errorf("expected session to be removed or inactive after close")
	}
}

func TestSweepExpiredSessions(t *testing.T) {
	tm := NewTunnelManager("test-jwt-secret")

	session, err := tm.CreateSession("test-proj", "user-1", 3000, 10)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Manually backdate ExpiresAt
	session.ExpiresAt = time.Now().Add(-1 * time.Minute)

	tm.SweepExpiredSessions()

	_, ok := tm.GetSession(session.ID)
	if ok {
		t.Errorf("expected expired session to be swept")
	}
}

func TestInjectBaseHref(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		baseHref string
		expected string
	}{
		{
			name:     "standard head",
			input:    "<!DOCTYPE html><html><head><title>Test</title></head><body>Hello</body></html>",
			baseHref: "/live/sess-123/",
			expected: "<!DOCTYPE html><html><head><base href=\"/live/sess-123/\"><title>Test</title></head><body>Hello</body></html>",
		},
		{
			name:     "head with attributes",
			input:    "<html><head lang=\"en\"><title>Test</title></head></html>",
			baseHref: "/live/abc-456/",
			expected: "<html><head lang=\"en\"><base href=\"/live/abc-456/\"><title>Test</title></head></html>",
		},
		{
			name:     "no head tag",
			input:    "<div>Fragment</div>",
			baseHref: "/live/test/",
			expected: "<base href=\"/live/test/\"><div>Fragment</div>",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := string(injectBaseHref([]byte(tc.input), tc.baseHref))
			if res != tc.expected {
				t.Errorf("expected:\n%s\ngot:\n%s", tc.expected, res)
			}
		})
	}
}

func TestEndToEndTunnelProxy(t *testing.T) {
	tm := NewTunnelManager("test-jwt-secret")

	r := chi.NewRouter()
	r.Get("/api/v1/tunnel/ws", tm.HandleWebSocket)
	r.HandleFunc("/live/{sessionId}", tm.HandlePublicProxy)
	r.HandleFunc("/live/{sessionId}/*", tm.HandlePublicProxy)

	server := httptest.NewServer(r)
	defer server.Close()

	// 1. Create session
	session, err := tm.CreateSession("demo-site", "user-dev", 3000, 10)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// 2. Connect client dev agent over WebSocket
	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + fmt.Sprintf("/api/v1/tunnel/ws?session_id=%s&token=%s", session.ID, session.Token)
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial tunnel ws: %v", err)
	}
	defer wsConn.Close()

	// Read initial handshake message from server
	var handshake map[string]interface{}
	err = wsConn.ReadJSON(&handshake)
	if err != nil {
		t.Fatalf("failed to read handshake: %v", err)
	}
	if handshake["type"] != "connected" {
		t.Fatalf("expected handshake type 'connected', got %v", handshake["type"])
	}

	// Start simulated local dev server responder
	go func() {
		for {
			var req TunnelRequestMsg
			if err := wsConn.ReadJSON(&req); err != nil {
				return
			}

			// Dev server responds with HTML
			htmlPayload := "<html><head><title>App</title></head><body><h1>Hello from Local Dev</h1></body></html>"
			b64Body := base64.StdEncoding.EncodeToString([]byte(htmlPayload))

			resp := TunnelResponseMsg{
				Type:      "http_response",
				RequestID: req.RequestID,
				Status:    200,
				Headers: map[string]string{
					"Content-Type": "text/html; charset=utf-8",
				},
				Body: b64Body,
			}
			if err := wsConn.WriteJSON(resp); err != nil {
				return
			}
		}
	}()

	// Give goroutine a moment to settle
	time.Sleep(50 * time.Millisecond)

	// 3. Make HTTP request to public reverse proxy URL: /live/{sessionId}/
	publicProxyURL := server.URL + "/live/" + session.ID + "/"
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(publicProxyURL)
	if err != nil {
		t.Fatalf("public proxy GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", resp.StatusCode)
	}

	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	bodyStr := string(buf[:n])

	expectedBase := fmt.Sprintf(`<base href="/live/%s/">`, session.ID)
	if !strings.Contains(bodyStr, expectedBase) {
		t.Errorf("expected body to contain injected base href %s, got: %s", expectedBase, bodyStr)
	}
	if !strings.Contains(bodyStr, "Hello from Local Dev") {
		t.Errorf("expected body to contain dev server payload, got: %s", bodyStr)
	}
}
