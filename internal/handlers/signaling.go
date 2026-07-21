package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/orbit/control-server/internal/middleware"
	"github.com/orbit/control-server/internal/repository"
)

type SignalingHandler struct {
	db *repository.DB
}

func NewSignalingHandler(db *repository.DB) *SignalingHandler {
	return &SignalingHandler{db: db}
}

type SendSignalRequest struct {
	ToPeer  string `json:"toPeer"`
	Type    string `json:"type"`
	Payload string `json:"payload"`
}

func (h *SignalingHandler) SendSignal(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "id")
	fromPeer := middleware.GetUserID(r)

	// Verify project ID and membership
	members, err := h.db.GetProjectMembers(projectID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to verify membership"})
		return
	}

	isMember := false
	for _, m := range members {
		if m.UserID == fromPeer {
			isMember = true
			break
		}
	}
	if !isMember {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a project member"})
		return
	}

	var req SendSignalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if req.ToPeer == "" || req.Type == "" || req.Payload == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "toPeer, type, and payload are required"})
		return
	}

	// Verify recipient membership
	isRecipientMember := false
	for _, m := range members {
		if m.UserID == req.ToPeer {
			isRecipientMember = true
			break
		}
	}
	if !isRecipientMember {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "recipient is not a member of this project"})
		return
	}

	err = h.db.SaveSignal(projectID, fromPeer, req.ToPeer, req.Type, req.Payload)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save signal"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "signal dispatched"})
}

func (h *SignalingHandler) GetSignals(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "id")
	toPeer := middleware.GetUserID(r)

	// Verify project membership
	members, err := h.db.GetProjectMembers(projectID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to verify membership"})
		return
	}

	isMember := false
	for _, m := range members {
		if m.UserID == toPeer {
			isMember = true
			break
		}
	}
	if !isMember {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a project member"})
		return
	}

	signals, err := h.db.GetPendingSignalsForPeer(projectID, toPeer)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to pull signals"})
		return
	}

	// Format response matching repo objects
	type SignalResponse struct {
		FromPeer string `json:"fromPeer"`
		Type     string `json:"type"`
		Payload  string `json:"payload"`
	}

	resp := make([]SignalResponse, len(signals))
	for i, s := range signals {
		resp[i] = SignalResponse{
			FromPeer: s.FromPeer,
			Type:     s.Type,
			Payload:  s.Payload,
		}
	}

	// Clear signals after returning so they are consumed only once
	_ = h.db.ClearSignalsForPeer(projectID, toPeer)

	writeJSON(w, http.StatusOK, resp)
}
