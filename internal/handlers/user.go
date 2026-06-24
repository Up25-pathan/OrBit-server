package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/orbit/control-server/internal/middleware"
	"github.com/orbit/control-server/internal/models"
	"github.com/orbit/control-server/internal/repository"
)

type UserHandler struct {
	db *repository.DB
}

func NewUserHandler(db *repository.DB) *UserHandler {
	return &UserHandler{db: db}
}

func (h *UserHandler) GetProfile(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	user, err := h.db.GetUserByID(userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server error"})
		return
	}
	if user == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}

	writeJSON(w, http.StatusOK, user)
}

type UpdateProfileRequest struct {
	DisplayName string `json:"displayName"`
	Bio         string `json:"bio"`
	AvatarURL   string `json:"avatarUrl"`
}

func (h *UserHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req UpdateProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if req.DisplayName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "displayName is required"})
		return
	}

	if err := h.db.UpdateProfile(userID, req.DisplayName, req.Bio, req.AvatarURL); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update profile"})
		return
	}

	user, _ := h.db.GetUserByID(userID)
	writeJSON(w, http.StatusOK, user)
}

func (h *UserHandler) SearchUsers(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "query parameter 'q' is required"})
		return
	}

	users, err := h.db.SearchUsers(query, 20)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "search failed"})
		return
	}

	if users == nil {
		users = []models.UserSearchResult{}
	}

	writeJSON(w, http.StatusOK, users)
}

type UpdateKeyRequest struct {
	Fingerprint string `json:"fingerprint"`
}

func (h *UserHandler) UpdatePublicKey(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req UpdateKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if err := h.db.UpdatePublicKey(userID, req.Fingerprint); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update key"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
