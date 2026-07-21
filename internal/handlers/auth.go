package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/orbit/control-server/internal/license"
	"github.com/orbit/control-server/internal/middleware"
	"github.com/orbit/control-server/internal/models"
	"github.com/orbit/control-server/internal/repository"
)

type AuthHandler struct {
	db        *repository.DB
	validator license.LicenseValidator
	jwtSecret string
	jwtExpiry time.Duration
}

func NewAuthHandler(db *repository.DB, validator license.LicenseValidator, jwtSecret string, jwtExpiry time.Duration) *AuthHandler {
	return &AuthHandler{db: db, validator: validator, jwtSecret: jwtSecret, jwtExpiry: jwtExpiry}
}

// AuthenticateKey is the single authentication endpoint.
// Flow: Desktop → Go Server → LicenseValidator → UpsertUser → JWT → Desktop logged in.
func (h *AuthHandler) AuthenticateKey(w http.ResponseWriter, r *http.Request) {
	var req models.LicenseAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if req.LicenseKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "licenseKey is required"})
		return
	}

	// Validate the license key against the authority (mock today, website later)
	info, err := h.validator.Validate(req.LicenseKey)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid license key"})
		return
	}

	// Upsert the user — creates if new, updates metadata if existing
	user, err := h.db.UpsertUser(info.UserID, info.Name, info.Email, info.PlanTier, req.LicenseKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create user session"})
		return
	}

	// Generate JWT with enriched claims
	token, err := middleware.GenerateToken(user.ID, user.Email, user.PlanTier, req.LicenseKey, h.jwtSecret)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate token"})
		return
	}

	writeJSON(w, http.StatusOK, models.AuthResponse{Token: token, User: *user})
}
