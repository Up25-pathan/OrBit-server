package tunnel

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/orbit/control-server/internal/middleware"
)

// TunnelRequestMsg represents an HTTP request to be forwarded over the WebSocket to the local dev server.
type TunnelRequestMsg struct {
	Type      string            `json:"type"` // "http_request"
	RequestID string            `json:"requestId"`
	Method    string            `json:"method"`
	Path      string            `json:"path"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body,omitempty"` // base64 encoded
}

// TunnelResponseMsg represents the HTTP response received back from the local dev server.
type TunnelResponseMsg struct {
	Type      string            `json:"type"` // "http_response"
	RequestID string            `json:"requestId"`
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body,omitempty"` // base64 encoded
}

// TunnelSession represents an active ephemeral reverse tunnel session.
type TunnelSession struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"projectId"`
	UserID       string    `json:"userId"`
	Token        string    `json:"token"`
	Port         int       `json:"port"`
	CreatedAt    time.Time `json:"createdAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Active       bool      `json:"active"`
	VisitorCount int64     `json:"visitorCount"`

	connMu      sync.Mutex
	conn        *websocket.Conn
	pendingReqs map[string]chan *TunnelResponseMsg
	reqMu       sync.RWMutex
	closeChan   chan struct{}
	closeOnce   sync.Once
}

// TunnelManager coordinates all active reverse tunnels and public proxying.
type TunnelManager struct {
	mu        sync.RWMutex
	sessions  map[string]*TunnelSession
	upgrader  websocket.Upgrader
	jwtSecret string
}

func NewTunnelManager(jwtSecret string) *TunnelManager {
	return &TunnelManager{
		sessions:  make(map[string]*TunnelSession),
		jwtSecret: jwtSecret,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				// Allow local desktop app / daemon or web clients
				return true
			},
		},
	}
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func sanitizeSlug(s string) string {
	reg := regexp.MustCompile("[^a-zA-Z0-9]+")
	cleaned := reg.ReplaceAllString(s, "-")
	cleaned = strings.Trim(cleaned, "-")
	if len(cleaned) > 12 {
		cleaned = cleaned[:12]
	}
	if cleaned == "" {
		cleaned = "app"
	}
	return strings.ToLower(cleaned)
}

// CreateSession allocates a new 10-minute ephemeral tunnel session.
func (tm *TunnelManager) CreateSession(projectID, userID string, port int, durationMinutes int) (*TunnelSession, error) {
	if port <= 0 || port > 65535 {
		port = 3000
	}
	if durationMinutes <= 0 {
		durationMinutes = 10
	}
	if durationMinutes > 60 {
		durationMinutes = 60
	}

	slug := sanitizeSlug(projectID)
	sessionID := fmt.Sprintf("%s-%s", slug, randomString(4))
	token := randomString(16)

	now := time.Now()
	expiresAt := now.Add(time.Duration(durationMinutes) * time.Minute)

	session := &TunnelSession{
		ID:          sessionID,
		ProjectID:   projectID,
		UserID:      userID,
		Token:       token,
		Port:        port,
		CreatedAt:   now,
		ExpiresAt:   expiresAt,
		Active:      true,
		pendingReqs: make(map[string]chan *TunnelResponseMsg),
		closeChan:   make(chan struct{}),
	}

	tm.mu.Lock()
	tm.sessions[sessionID] = session
	tm.mu.Unlock()

	log.Printf("[tunnel] Created session %s for project %s (port %d, expires in %dm)", sessionID, projectID, port, durationMinutes)
	return session, nil
}

// GetSession returns a session by ID if exists.
func (tm *TunnelManager) GetSession(sessionID string) (*TunnelSession, bool) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	sess, exists := tm.sessions[sessionID]
	return sess, exists
}

// ExtendSession adds additional time to a session.
func (tm *TunnelManager) ExtendSession(sessionID, userID string, additionalMinutes int) (*TunnelSession, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	session, exists := tm.sessions[sessionID]
	if !exists {
		return nil, fmt.Errorf("session not found")
	}
	if session.UserID != userID {
		return nil, fmt.Errorf("unauthorized")
	}

	if additionalMinutes <= 0 {
		additionalMinutes = 10
	}
	newExpires := time.Now().Add(time.Duration(additionalMinutes) * time.Minute)
	// Cap maximum lifetime from creation to 120 minutes
	maxExpires := session.CreatedAt.Add(120 * time.Minute)
	if newExpires.After(maxExpires) {
		newExpires = maxExpires
	}

	session.ExpiresAt = newExpires
	session.Active = true
	log.Printf("[tunnel] Extended session %s to %v", sessionID, newExpires)
	return session, nil
}

// CloseSession terminates an active tunnel session.
func (tm *TunnelManager) CloseSession(sessionID, userID string) error {
	tm.mu.Lock()
	session, exists := tm.sessions[sessionID]
	if !exists {
		tm.mu.Unlock()
		return fmt.Errorf("session not found")
	}
	if session.UserID != userID && userID != "system" {
		tm.mu.Unlock()
		return fmt.Errorf("unauthorized")
	}
	session.Active = false
	delete(tm.sessions, sessionID)
	tm.mu.Unlock()

	session.terminate()
	log.Printf("[tunnel] Closed session %s", sessionID)
	return nil
}

func (s *TunnelSession) terminate() {
	s.closeOnce.Do(func() {
		close(s.closeChan)
		s.connMu.Lock()
		if s.conn != nil {
			_ = s.conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "tunnel closed"),
				time.Now().Add(1*time.Second),
			)
			_ = s.conn.Close()
			s.conn = nil
		}
		s.connMu.Unlock()

		s.reqMu.Lock()
		for id, ch := range s.pendingReqs {
			close(ch)
			delete(s.pendingReqs, id)
		}
		s.reqMu.Unlock()
	})
}

// SweepExpiredSessions cleans up sessions that exceeded their TTL.
func (tm *TunnelManager) SweepExpiredSessions() {
	now := time.Now()
	var toClose []*TunnelSession

	tm.mu.Lock()
	for id, sess := range tm.sessions {
		if now.After(sess.ExpiresAt) || !sess.Active {
			toClose = append(toClose, sess)
			delete(tm.sessions, id)
		}
	}
	tm.mu.Unlock()

	for _, sess := range toClose {
		sess.Active = false
		sess.terminate()
		log.Printf("[tunnel-sweeper] Cleaned up expired session %s", sess.ID)
	}
}

// StartSweeper starts a background goroutine to clean up expired sessions every 15 seconds.
func (tm *TunnelManager) StartSweeper(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				tm.SweepExpiredSessions()
			case <-ctx.Done():
				return
			}
		}
	}()
}

// HandleWebSocket accepts WebSocket connection from the developer's local desktop client.
func (tm *TunnelManager) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	token := r.URL.Query().Get("token")

	if sessionID == "" || token == "" {
		http.Error(w, "missing session_id or token", http.StatusBadRequest)
		return
	}

	session, exists := tm.GetSession(sessionID)
	if !exists || !session.Active {
		http.Error(w, "session expired or invalid", http.StatusNotFound)
		return
	}

	if session.Token != token {
		http.Error(w, "invalid session token", http.StatusUnauthorized)
		return
	}

	conn, err := tm.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[tunnel-ws] Upgrade error for %s: %v", sessionID, err)
		return
	}

	session.connMu.Lock()
	if session.conn != nil {
		_ = session.conn.Close()
	}
	session.conn = conn
	session.connMu.Unlock()

	log.Printf("[tunnel-ws] Local dev agent connected for session %s (port %d)", sessionID, session.Port)

	// Send initial handshake confirmation to local client
	_ = conn.WriteJSON(map[string]interface{}{
		"type":      "connected",
		"sessionId": sessionID,
		"port":      session.Port,
		"expiresAt": session.ExpiresAt.Unix(),
	})

	// Message read loop
	defer func() {
		session.connMu.Lock()
		if session.conn == conn {
			session.conn = nil
		}
		session.connMu.Unlock()
		_ = conn.Close()
		log.Printf("[tunnel-ws] Local dev agent disconnected for session %s", sessionID)
	}()

	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var resp TunnelResponseMsg
		if err := json.Unmarshal(msgBytes, &resp); err == nil && resp.RequestID != "" {
			session.reqMu.RLock()
			ch, ok := session.pendingReqs[resp.RequestID]
			session.reqMu.RUnlock()

			if ok && ch != nil {
				select {
				case ch <- &resp:
				default:
				}
			}
		}
	}
}

// HandlePublicProxy receives external visitor requests and reverse-proxies them through the WebSocket tunnel.
func (tm *TunnelManager) HandlePublicProxy(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	subpath := chi.URLParam(r, "*")

	session, exists := tm.GetSession(sessionID)

	// Session missing or expired
	if !exists || !session.Active || time.Now().After(session.ExpiresAt) {
		renderExpiredPage(w, sessionID)
		return
	}

	// Session active but local desktop agent is not connected yet
	session.connMu.Lock()
	conn := session.conn
	session.connMu.Unlock()

	if conn == nil {
		renderWaitingPage(w, sessionID, session.Port)
		return
	}

	// Ensure trailing slash on base URL so relative assets resolve cleanly
	if subpath == "" && !strings.HasSuffix(r.URL.Path, "/") {
		redirectURL := r.URL.Path + "/"
		if r.URL.RawQuery != "" {
			redirectURL += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, redirectURL, http.StatusMovedPermanently)
		return
	}

	// Construct local destination path (with query params)
	destPath := "/" + strings.TrimPrefix(subpath, "/")
	if r.URL.RawQuery != "" {
		destPath += "?" + r.URL.RawQuery
	}

	tm.proxyToSession(w, r, session, destPath)
}

// HandleFallbackProxy catches any root-relative asset requests (like /_next/*, /assets/*, /favicon.ico)
// that were requested by an active live preview session using cookie, Referer, or active session fallback.
func (tm *TunnelManager) HandleFallbackProxy(w http.ResponseWriter, r *http.Request) {
	sessionID := ""

	// 1. Check Cookie
	if c, err := r.Cookie("orbit_live_session"); err == nil && c.Value != "" {
		sessionID = c.Value
	}

	// 2. Fallback to Referer
	if sessionID == "" {
		if ref := r.Header.Get("Referer"); ref != "" {
			if idx := strings.Index(ref, "/live/"); idx != -1 {
				rest := ref[idx+len("/live/"):]
				if slashIdx := strings.Index(rest, "/"); slashIdx != -1 {
					sessionID = rest[:slashIdx]
				} else if qIdx := strings.Index(rest, "?"); qIdx != -1 {
					sessionID = rest[:qIdx]
				} else {
					sessionID = rest
				}
			}
		}
	}

	// 3. Fallback to single active session if cookies and referer are blocked by browser/sandbox
	if sessionID == "" {
		tm.mu.RLock()
		var candidate *TunnelSession
		now := time.Now()
		for _, s := range tm.sessions {
			if s.Active && now.Before(s.ExpiresAt) {
				s.connMu.Lock()
				hasConn := s.conn != nil
				s.connMu.Unlock()
				if hasConn {
					if candidate == nil {
						candidate = s
					} else {
						// Multiple active sessions; cannot disambiguate safely
						candidate = nil
						break
					}
				}
			}
		}
		tm.mu.RUnlock()
		if candidate != nil {
			sessionID = candidate.ID
		}
	}

	if sessionID != "" {
		if session, exists := tm.GetSession(sessionID); exists && session.Active && time.Now().Before(session.ExpiresAt) {
			destPath := r.URL.Path
			if r.URL.RawQuery != "" {
				destPath += "?" + r.URL.RawQuery
			}
			tm.proxyToSession(w, r, session, destPath)
			return
		}
	}

	http.NotFound(w, r)
}

func (tm *TunnelManager) proxyToSession(w http.ResponseWriter, r *http.Request, session *TunnelSession, destPath string) {
	session.connMu.Lock()
	conn := session.conn
	session.connMu.Unlock()

	if conn == nil {
		renderWaitingPage(w, session.ID, session.Port)
		return
	}

	atomic.AddInt64(&session.VisitorCount, 1)

	// Read body if present
	var bodyB64 string
	if r.Body != nil {
		bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024)) // 10MB limit
		if err == nil && len(bodyBytes) > 0 {
			bodyB64 = base64.StdEncoding.EncodeToString(bodyBytes)
		}
	}

	// Filter and clone headers
	headers := make(map[string]string)
	for k, v := range r.Header {
		lowerKey := strings.ToLower(k)
		if lowerKey == "host" || lowerKey == "connection" || lowerKey == "upgrade" ||
			lowerKey == "sec-websocket-key" || lowerKey == "sec-websocket-version" ||
			lowerKey == "accept-encoding" {
			continue
		}
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}
	headers["X-Forwarded-For"] = r.RemoteAddr
	headers["X-Forwarded-Proto"] = "https"
	headers["X-Orbit-Tunnel"] = session.ID

	reqID := fmt.Sprintf("req-%d-%s", time.Now().UnixNano(), randomString(3))
	reqMsg := TunnelRequestMsg{
		Type:      "http_request",
		RequestID: reqID,
		Method:    r.Method,
		Path:      destPath,
		Headers:   headers,
		Body:      bodyB64,
	}

	respChan := make(chan *TunnelResponseMsg, 1)

	session.reqMu.Lock()
	session.pendingReqs[reqID] = respChan
	session.reqMu.Unlock()

	defer func() {
		session.reqMu.Lock()
		delete(session.pendingReqs, reqID)
		session.reqMu.Unlock()
	}()

	session.connMu.Lock()
	writeErr := conn.WriteJSON(reqMsg)
	session.connMu.Unlock()

	if writeErr != nil {
		http.Error(w, "Failed to forward request to local dev server", http.StatusBadGateway)
		return
	}

	select {
	case resp := <-respChan:
		if resp == nil {
			http.Error(w, "Tunnel connection severed", http.StatusBadGateway)
			return
		}

		// Decode response body
		var respBody []byte
		if resp.Body != "" {
			respBody, _ = base64.StdEncoding.DecodeString(resp.Body)
		}

		contentType := ""
		for k, v := range resp.Headers {
			lowerK := strings.ToLower(k)
			if lowerK == "transfer-encoding" || lowerK == "content-length" ||
				lowerK == "content-encoding" ||
				lowerK == "x-frame-options" || lowerK == "content-security-policy" ||
				lowerK == "content-security-policy-report-only" {
				continue
			}
			if lowerK == "content-type" {
				contentType = v
			}
			w.Header().Set(k, v)
		}

		// Set cookie on root path with SameSite=None and Secure=true so all subsequent asset requests (/_next/*, etc.)
		// in iframes and cross-site devices automatically route to this session
		http.SetCookie(w, &http.Cookie{
			Name:     "orbit_live_session",
			Value:    session.ID,
			Path:     "/",
			Expires:  session.ExpiresAt,
			SameSite: http.SameSiteNoneMode,
			Secure:   true,
		})

		// Allow embedding in in-app device cockpit frames and external iframes
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, HEAD, PATCH")
		w.Header().Set("Access-Control-Allow-Headers", "*")

		// If HTML document, rewrite root paths and inject bridge script
		if strings.Contains(strings.ToLower(contentType), "text/html") && len(respBody) > 0 {
			respBody = rewriteHTMLAndInjectBridge(respBody, session.ID)
		}

		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(respBody)))
		w.Header().Set("X-Powered-By", "OrBit-GO-Live-Preview")
		if resp.Status > 0 {
			w.WriteHeader(resp.Status)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		_, _ = w.Write(respBody)

	case <-time.After(30 * time.Second):
		http.Error(w, "Gateway Timeout: Local dev server took too long to reply", http.StatusGatewayTimeout)

	case <-r.Context().Done():
		// Visitor disconnected early
	}
}

// rewriteHTMLAndInjectBridge injects base tag, asset bridge script, and rewrites root assets for Next.js/Vite
func rewriteHTMLAndInjectBridge(html []byte, sessionID string) []byte {
	prefix := fmt.Sprintf("/live/%s", sessionID)

	// Rewrite common root asset paths in HTML
	rewrites := []struct {
		from string
		to   string
	}{
		{`"/_next/`, fmt.Sprintf(`"%s/_next/`, prefix)},
		{`'/_next/`, fmt.Sprintf(`'%s/_next/`, prefix)},
		{`"/assets/`, fmt.Sprintf(`"%s/assets/`, prefix)},
		{`'assets/`, fmt.Sprintf(`'%s/assets/`, prefix)},
		{`"/static/`, fmt.Sprintf(`"%s/static/`, prefix)},
		{`"/src/`, fmt.Sprintf(`"%s/src/`, prefix)},
		{`"/@vite/`, fmt.Sprintf(`"%s/@vite/`, prefix)},
		{`"/@fs/`, fmt.Sprintf(`"%s/@fs/`, prefix)},
	}

	for _, rw := range rewrites {
		html = bytes.ReplaceAll(html, []byte(rw.from), []byte(rw.to))
	}

	// Comprehensive client-side bridge script:
	// 1. Sets base href for relative assets
	// 2. Intercepts fetch and XMLHttpRequest
	// 3. Intercepts dynamic script and link tag injections (Webpack dynamic chunks, Vite HMR chunks)
	// 4. Intercepts setAttribute calls
	bridgeScript := fmt.Sprintf(`<base href="%s/"><script>(function(){var p='%s';var of=window.fetch;if(of){window.fetch=function(u,i){if(typeof u==='string'&&u.charAt(0)==='/'&&u.indexOf(p)!==0){u=p+u;}return of.call(this,u,i);};}var oo=window.XMLHttpRequest&&window.XMLHttpRequest.prototype.open;if(oo){window.XMLHttpRequest.prototype.open=function(m,u){if(typeof u==='string'&&u.charAt(0)==='/'&&u.indexOf(p)!==0){u=p+u;}return oo.apply(this,arguments);};}var sDesc=Object.getOwnPropertyDescriptor(HTMLScriptElement.prototype,'src');if(sDesc&&sDesc.set){var origSSet=sDesc.set;Object.defineProperty(HTMLScriptElement.prototype,'src',{set:function(v){if(typeof v==='string'&&v.charAt(0)==='/'&&v.indexOf(p)!==0){v=p+v;}return origSSet.call(this,v);},get:sDesc.get});}var lDesc=Object.getOwnPropertyDescriptor(HTMLLinkElement.prototype,'href');if(lDesc&&lDesc.set){var origLSet=lDesc.set;Object.defineProperty(HTMLLinkElement.prototype,'href',{set:function(v){if(typeof v==='string'&&v.charAt(0)==='/'&&v.indexOf(p)!==0){v=p+v;}return origLSet.call(this,v);},get:lDesc.get});}var origSetAttr=Element.prototype.setAttribute;Element.prototype.setAttribute=function(n,v){if((n==='src'||n==='href')&&typeof v==='string'&&v.charAt(0)==='/'&&v.indexOf(p)!==0){v=p+v;}return origSetAttr.call(this,n,v);};})();</script>`, prefix, prefix)

	// Look for <head>
	lowerHTML := bytes.ToLower(html)
	headIdx := bytes.Index(lowerHTML, []byte("<head>"))
	if headIdx != -1 {
		insertAt := headIdx + len("<head>")
		var buf bytes.Buffer
		buf.Write(html[:insertAt])
		buf.WriteString(bridgeScript)
		buf.Write(html[insertAt:])
		return buf.Bytes()
	}

	// Look for <head ...>
	headReg := regexp.MustCompile(`(?i)<head[^>]*>`)
	loc := headReg.FindIndex(html)
	if loc != nil {
		insertAt := loc[1]
		var buf bytes.Buffer
		buf.Write(html[:insertAt])
		buf.WriteString(bridgeScript)
		buf.Write(html[insertAt:])
		return buf.Bytes()
	}

	// Fallback
	var buf bytes.Buffer
	buf.WriteString(bridgeScript)
	buf.Write(html)
	return buf.Bytes()
}

// HTTP REST Handlers

type CreateSessionRequest struct {
	ProjectID       string `json:"projectId"`
	Port            int    `json:"port"`
	DurationMinutes int    `json:"durationMinutes"`
}

func (tm *TunnelManager) HandleCreateSession(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	var req CreateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	if req.ProjectID == "" {
		http.Error(w, `{"error":"projectId required"}`, http.StatusBadRequest)
		return
	}

	session, err := tm.CreateSession(req.ProjectID, userID, req.Port, req.DurationMinutes)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	// Determine public URLs
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" && strings.Contains(r.Host, "localhost") {
		scheme = "http"
	}
	wsScheme := "wss"
	if scheme == "http" {
		wsScheme = "ws"
	}

	host := r.Host
	if fwdHost := r.Header.Get("X-Forwarded-Host"); fwdHost != "" {
		host = fwdHost
	}

	publicURL := fmt.Sprintf("%s://%s/live/%s/", scheme, host, session.ID)
	wsURL := fmt.Sprintf("%s://%s/api/v1/tunnel/ws?session_id=%s&token=%s", wsScheme, host, session.ID, session.Token)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"sessionId":   session.ID,
		"tunnelToken": session.Token,
		"publicUrl":   publicURL,
		"wsUrl":       wsURL,
		"port":        session.Port,
		"expiresAt":   session.ExpiresAt.Unix() * 1000,
		"createdAt":   session.CreatedAt.Unix() * 1000,
		"durationMin": req.DurationMinutes,
	})
}

func (tm *TunnelManager) HandleExtendSession(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	sessionID := chi.URLParam(r, "sessionId")

	var req struct {
		AdditionalMinutes int `json:"additionalMinutes"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.AdditionalMinutes <= 0 {
		req.AdditionalMinutes = 10
	}

	session, err := tm.ExtendSession(sessionID, userID, req.AdditionalMinutes)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success":   true,
		"sessionId": session.ID,
		"expiresAt": session.ExpiresAt.Unix() * 1000,
	})
}

func (tm *TunnelManager) HandleStopSession(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	sessionID := chi.URLParam(r, "sessionId")

	err := tm.CloseSession(sessionID, userID)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Tunnel session terminated",
	})
}

func (tm *TunnelManager) HandleGetSessionStatus(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionId")
	session, exists := tm.GetSession(sessionID)
	if !exists {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}

	remaining := int(time.Until(session.ExpiresAt).Seconds())
	if remaining < 0 {
		remaining = 0
	}

	session.connMu.Lock()
	isConnected := session.conn != nil
	session.connMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"sessionId":        session.ID,
		"active":           session.Active && remaining > 0,
		"isConnected":      isConnected,
		"remainingSeconds": remaining,
		"visitorCount":     atomic.LoadInt64(&session.VisitorCount),
		"port":             session.Port,
		"expiresAt":        session.ExpiresAt.Unix() * 1000,
	})
}

// Styled Status Pages

func renderExpiredPage(w http.ResponseWriter, sessionID string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
	<meta charset="UTF-8">
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
	<title>OrBit GO - Preview Session Expired</title>
	<style>
		body {
			margin: 0;
			background: #090d16;
			color: #e2e8f0;
			font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
			display: flex;
			align-items: center;
			justify-content: center;
			min-height: 100vh;
			padding: 20px;
			box-sizing: border-box;
		}
		.card {
			background: rgba(15, 23, 42, 0.85);
			border: 1px solid rgba(244, 63, 94, 0.3);
			box-shadow: 0 20px 40px rgba(0, 0, 0, 0.6), 0 0 30px rgba(244, 63, 94, 0.15);
			border-radius: 16px;
			max-width: 480px;
			width: 100%%;
			padding: 36px 32px;
			text-align: center;
			backdrop-filter: blur(12px);
		}
		.badge {
			display: inline-flex;
			align-items: center;
			gap: 6px;
			padding: 4px 12px;
			border-radius: 9999px;
			font-size: 11px;
			font-weight: 700;
			letter-spacing: 0.05em;
			text-transform: uppercase;
			background: rgba(244, 63, 94, 0.15);
			color: #fb7185;
			border: 1px solid rgba(244, 63, 94, 0.3);
			margin-bottom: 20px;
		}
		h1 {
			font-size: 24px;
			font-weight: 800;
			letter-spacing: -0.02em;
			margin: 0 0 12px 0;
			color: #ffffff;
		}
		p {
			font-size: 14px;
			line-height: 1.6;
			color: #94a3b8;
			margin: 0 0 24px 0;
		}
		.session-code {
			background: #020617;
			border: 1px solid rgba(255, 255, 255, 0.08);
			padding: 8px 14px;
			border-radius: 8px;
			font-family: monospace;
			font-size: 12px;
			color: #38bdf8;
			margin-bottom: 24px;
			display: inline-block;
		}
		.footer {
			font-size: 12px;
			color: #64748b;
			border-top: 1px solid rgba(255, 255, 255, 0.06);
			padding-top: 20px;
		}
	</style>
</head>
<body>
	<div class="card">
		<div class="badge">Session Expired</div>
		<h1>Live Showcase Expired</h1>
		<p>This 10-minute temporary preview link has reached its expiration time or was closed by the developer.</p>
		<div class="session-code">Session: %s</div>
		<div class="footer">
			OrBit GO &bull; Real-Time P2P Code &amp; Preview Engine
		</div>
	</div>
</body>
</html>`, sessionID)
}

func renderWaitingPage(w http.ResponseWriter, sessionID string, port int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
	<meta charset="UTF-8">
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
	<meta http-equiv="refresh" content="3">
	<title>OrBit GO - Connecting to Local Dev Server</title>
	<style>
		body {
			margin: 0;
			background: #090d16;
			color: #e2e8f0;
			font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
			display: flex;
			align-items: center;
			justify-content: center;
			min-height: 100vh;
			padding: 20px;
			box-sizing: border-box;
		}
		.card {
			background: rgba(15, 23, 42, 0.85);
			border: 1px solid rgba(56, 189, 248, 0.3);
			box-shadow: 0 20px 40px rgba(0, 0, 0, 0.6), 0 0 30px rgba(56, 189, 248, 0.15);
			border-radius: 16px;
			max-width: 480px;
			width: 100%%;
			padding: 36px 32px;
			text-align: center;
			backdrop-filter: blur(12px);
		}
		.spinner {
			width: 44px;
			height: 44px;
			border: 3px solid rgba(56, 189, 248, 0.2);
			border-top-color: #38bdf8;
			border-radius: 50%%;
			animation: spin 1s linear infinite;
			margin: 0 auto 20px auto;
		}
		@keyframes spin {
			to { transform: rotate(360deg); }
		}
		h1 {
			font-size: 22px;
			font-weight: 800;
			margin: 0 0 10px 0;
			color: #ffffff;
		}
		p {
			font-size: 14px;
			line-height: 1.6;
			color: #94a3b8;
			margin: 0 0 20px 0;
		}
		.port-pill {
			background: #020617;
			border: 1px solid rgba(56, 189, 248, 0.2);
			padding: 6px 12px;
			border-radius: 8px;
			font-family: monospace;
			font-size: 13px;
			color: #38bdf8;
			display: inline-block;
			margin-bottom: 24px;
		}
		.footer {
			font-size: 12px;
			color: #64748b;
			border-top: 1px solid rgba(255, 255, 255, 0.06);
			padding-top: 18px;
		}
	</style>
</head>
<body>
	<div class="card">
		<div class="spinner"></div>
		<h1>Connecting to Dev Server...</h1>
		<p>Waiting for the developer's OrBit GO Cockpit to establish the reverse tunnel uplink.</p>
		<div class="port-pill">Target Local Port: :%d</div>
		<div class="footer">
			Page auto-reloads every 3 seconds &bull; Powered by OrBit GO
		</div>
	</div>
</body>
</html>`, port)
}
