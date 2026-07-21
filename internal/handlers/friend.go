package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/orbit/control-server/internal/middleware"
	"github.com/orbit/control-server/internal/models"
	"github.com/orbit/control-server/internal/repository"
)

type FriendHandler struct {
	db *repository.DB
}

func NewFriendHandler(db *repository.DB) *FriendHandler {
	return &FriendHandler{db: db}
}

type SendFriendRequestPayload struct {
	UserID string `json:"userId"`
}

func (h *FriendHandler) SendRequest(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req SendFriendRequestPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	target, err := h.db.GetUserByID(req.UserID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server error"})
		return
	}
	if target == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}

	fr, err := h.db.SendFriendRequest(userID, req.UserID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, fr)
}

func (h *FriendHandler) AcceptRequest(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	requestID := r.URL.Query().Get("id")
	if requestID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request id is required"})
		return
	}

	if err := h.db.AcceptFriendRequest(requestID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}

func (h *FriendHandler) GetRequests(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	requests, err := h.db.GetPendingRequests(userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server error"})
		return
	}

	if requests == nil {
		requests = []models.FriendRequest{}
	}

	writeJSON(w, http.StatusOK, requests)
}

func (h *FriendHandler) ListFriends(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	friends, err := h.db.GetFriends(userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server error"})
		return
	}

	if friends == nil {
		friends = []models.Friend{}
	}

	writeJSON(w, http.StatusOK, friends)
}

func (h *FriendHandler) DeclineRequest(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	requestID := r.URL.Query().Get("id")
	if requestID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request id is required"})
		return
	}

	if err := h.db.RejectFriendRequest(requestID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "declined"})
}
