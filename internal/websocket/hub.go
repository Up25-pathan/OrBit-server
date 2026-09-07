package websocket

import (
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/orbit/control-server/internal/middleware"
	"github.com/orbit/control-server/internal/repository"
)

type PeerConnection struct {
	mu     sync.Mutex
	conn   *websocket.Conn
	userID string
}

type Hub struct {
	mu           sync.RWMutex
	connections  map[string]map[string]*PeerConnection // projectID -> connID -> connection
	upgrader     websocket.Upgrader
	db           *repository.DB
}

type SignalMessage struct {
	ToPeer      string `json:"toPeer"`
	ToPeerAlt   string `json:"to_peer,omitempty"`
	FromPeer    string `json:"fromPeer"`
	FromPeerAlt string `json:"from_peer,omitempty"`
	Type        string `json:"type"`
	SignalType  string `json:"signal_type,omitempty"`
	Payload     string `json:"payload"`
}

var allowedOrigins = map[string]bool{
	"tauri://localhost":                      true,
	"http://tauri.localhost":                 true,
	"https://tauri.localhost":                true,
	"asset://localhost":                      true,
	"https://orbit-server-xbr5.onrender.com": true,
	"https://orbit-server-kae6.onrender.com": true,
	"https://orbit.dev":                      true,
}

func isAllowedOrigin(origin string) bool {
	if allowedOrigins[origin] {
		return true
	}
	if strings.HasPrefix(origin, "http://localhost") || strings.HasPrefix(origin, "http://127.0.0.1") || strings.HasPrefix(origin, "https://localhost") {
		return true
	}
	if strings.HasSuffix(origin, ".onrender.com") || strings.HasSuffix(origin, ".vercel.app") {
		return true
	}
	return false
}

func NewHub(db *repository.DB) *Hub {
	return &Hub{
		connections: make(map[string]map[string]*PeerConnection),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" {
					return true
				}
				return isAllowedOrigin(origin)
			},
		},
		db: db,
	}
}

func (h *Hub) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "id")
	userID := middleware.GetUserID(r)

	if userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	isMember, err := h.isProjectMember(projectID, userID)
	if err != nil || !isMember {
		http.Error(w, "not a project member", http.StatusForbidden)
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ws] upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	// Unique connection ID per device instance
	connID := strings.Join([]string{userID, r.RemoteAddr}, "@")
	if connID == "" {
		connID = userID
	}

	h.addConnection(projectID, connID, userID, conn)
	defer func() {
		h.removeConnection(projectID, connID)
		h.BroadcastToProject(projectID, connID, SignalMessage{
			FromPeer:    userID,
			FromPeerAlt: userID,
			Type:        "peer_disconnected",
			SignalType:  "peer_disconnected",
			Payload:     userID,
		})
	}()

	log.Printf("[ws] peer %s (conn %s) connected to project %s", userID, connID, projectID)

	// Notify other peer devices in this project room that a peer joined
	h.BroadcastToProject(projectID, connID, SignalMessage{
		FromPeer:    userID,
		FromPeerAlt: userID,
		Type:        "peer_connected",
		SignalType:  "peer_connected",
		Payload:     userID,
	})

	for {
		var raw map[string]interface{}
		if err := conn.ReadJSON(&raw); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("[ws] read error: %v", err)
			}
			break
		}

		toPeer, _ := raw["toPeer"].(string)
		if toPeer == "" {
			toPeer, _ = raw["to_peer"].(string)
		}

		sigType, _ := raw["type"].(string)
		if rawSigType, ok := raw["signal_type"].(string); ok && rawSigType != "" {
			sigType = rawSigType
		}

		payload, _ := raw["payload"].(string)
		if payload == "" {
			if rawAddr, ok := raw["address"].(string); ok && rawAddr != "" {
				payload = rawAddr
				if sigType == "" {
					sigType = "address"
				}
			}
		}

		if sigType == "" || (payload == "" && sigType != "ping" && sigType != "pong") {
			continue
		}

		outMsg := SignalMessage{
			ToPeer:      toPeer,
			ToPeerAlt:   toPeer,
			FromPeer:    userID,
			FromPeerAlt: userID,
			Type:        sigType,
			SignalType:  sigType,
			Payload:     payload,
		}

		if toPeer == "" || toPeer == "*" || toPeer == "all" {
			h.BroadcastToProject(projectID, connID, outMsg)
		} else {
			if err := h.db.SaveSignal(projectID, userID, toPeer, sigType, payload); err != nil {
				log.Printf("[ws] save signal failed: %v", err)
			}
			h.deliverSignal(projectID, toPeer, outMsg)
		}
	}
}

func (h *Hub) addConnection(projectID, connID, userID string, conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.connections[projectID] == nil {
		h.connections[projectID] = make(map[string]*PeerConnection)
	}
	h.connections[projectID][connID] = &PeerConnection{conn: conn, userID: userID}
}

func (h *Hub) removeConnection(projectID, connID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if projectConns, ok := h.connections[projectID]; ok {
		delete(projectConns, connID)
		if len(projectConns) == 0 {
			delete(h.connections, projectID)
		}
	}
}

func (h *Hub) DeliverSignal(projectID, toPeer string, msg SignalMessage) {
	h.mu.RLock()
	conns := h.connections[projectID]
	h.mu.RUnlock()

	if conns == nil {
		return
	}

	var deadConns []string
	for cID, peerConn := range conns {
		if cID == toPeer || peerConn.userID == toPeer {
			peerConn.mu.Lock()
			err := peerConn.conn.WriteJSON(msg)
			peerConn.mu.Unlock()

			if err != nil {
				log.Printf("[ws] write error to %s: %v", toPeer, err)
				deadConns = append(deadConns, cID)
			}
		}
	}

	for _, deadID := range deadConns {
		h.removeConnection(projectID, deadID)
	}
}

func (h *Hub) deliverSignal(projectID, toPeer string, msg SignalMessage) {
	h.DeliverSignal(projectID, toPeer, msg)
}

func (h *Hub) BroadcastToProject(projectID, senderConnID string, msg SignalMessage) {
	h.mu.RLock()
	conns := h.connections[projectID]
	h.mu.RUnlock()

	if conns == nil {
		return
	}

	var deadConns []string
	for cID, peerConn := range conns {
		// Only skip the exact socket connection that sent this message.
		// Other devices logged into the same user account WILL receive the signal.
		if cID == senderConnID {
			continue
		}

		peerConn.mu.Lock()
		err := peerConn.conn.WriteJSON(msg)
		peerConn.mu.Unlock()

		if err != nil {
			log.Printf("[ws] broadcast error to %s: %v", cID, err)
			deadConns = append(deadConns, cID)
		}
	}

	for _, deadID := range deadConns {
		h.removeConnection(projectID, deadID)
	}
}

func (h *Hub) isProjectMember(projectID, userID string) (bool, error) {
	if h.db == nil {
		return false, nil
	}
	return h.db.IsProjectMember(projectID, userID), nil
}