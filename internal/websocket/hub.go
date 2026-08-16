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

type Hub struct {
	mu           sync.RWMutex
	connections  map[string]map[string]*websocket.Conn // projectID -> peerID -> connection
	upgrader     websocket.Upgrader
	db           *repository.DB
}

type SignalMessage struct {
	ToPeer   string `json:"toPeer"`
	FromPeer string `json:"fromPeer"`
	Type     string `json:"type"`
	Payload  string `json:"payload"`
}

var allowedOrigins = map[string]bool{
	"tauri://localhost":                      true,
	"http://tauri.localhost":                 true,
	"https://tauri.localhost":                true,
	"asset://localhost":                      true,
	"https://orbit-sync.onrender.com":        true,
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
		connections: make(map[string]map[string]*websocket.Conn),
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

	h.addConnection(projectID, userID, conn)
	defer h.removeConnection(projectID, userID)

	log.Printf("[ws] peer %s connected to project %s", userID, projectID)

	for {
		var msg SignalMessage
		if err := conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("[ws] read error: %v", err)
			}
			break
		}

		if msg.ToPeer == "" || msg.Type == "" || msg.Payload == "" {
			continue
		}

		if err := h.db.SaveSignal(projectID, userID, msg.ToPeer, msg.Type, msg.Payload); err != nil {
			log.Printf("[ws] save signal failed: %v", err)
			continue
		}

		h.deliverSignal(projectID, msg.ToPeer, SignalMessage{
			FromPeer: userID,
			Type:     msg.Type,
			Payload:  msg.Payload,
		})
	}
}

func (h *Hub) addConnection(projectID, peerID string, conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.connections[projectID] == nil {
		h.connections[projectID] = make(map[string]*websocket.Conn)
	}
	h.connections[projectID][peerID] = conn
}

func (h *Hub) removeConnection(projectID, peerID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if projectConns, ok := h.connections[projectID]; ok {
		delete(projectConns, peerID)
		if len(projectConns) == 0 {
			delete(h.connections, projectID)
		}
	}
}

func (h *Hub) DeliverSignal(projectID, toPeer string, msg SignalMessage) {
	// Hold lock during entire operation to prevent race where connection
	// is removed between RUnlock and WriteJSON
	h.mu.RLock()
	conn, ok := h.connections[projectID][toPeer]
	if ok {
		if err := conn.WriteJSON(msg); err != nil {
			log.Printf("[ws] write error to %s: %v", toPeer, err)
			h.removeConnection(projectID, toPeer)
		}
	}
	h.mu.RUnlock()
}

func (h *Hub) deliverSignal(projectID, toPeer string, msg SignalMessage) {
	h.DeliverSignal(projectID, toPeer, msg)
}

func (h *Hub) BroadcastToProject(projectID string, msg SignalMessage) {
	h.mu.RLock()
	conns := h.connections[projectID]
	h.mu.RUnlock()

	for peerID, conn := range conns {
		if peerID == msg.FromPeer {
			continue
		}
		if err := conn.WriteJSON(msg); err != nil {
			log.Printf("[ws] broadcast error to %s: %v", peerID, err)
			h.removeConnection(projectID, peerID)
		}
	}
}

func (h *Hub) isProjectMember(projectID, userID string) (bool, error) {
	if h.db == nil {
		return false, nil
	}
	members, err := h.db.GetProjectMembers(projectID)
	if err != nil {
		return false, err
	}
	for _, m := range members {
		if m.UserID == userID {
			return true, nil
		}
	}
	return false, nil
}