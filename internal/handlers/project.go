package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/orbit/control-server/internal/middleware"
	"github.com/orbit/control-server/internal/models"
	"github.com/orbit/control-server/internal/repository"
)

type ProjectHandler struct {
	db *repository.DB
}

func NewProjectHandler(db *repository.DB) *ProjectHandler {
	return &ProjectHandler{db: db}
}

func (h *ProjectHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	var req models.CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"}); return
	}
	if req.Name == "" { writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"}); return }

	project, err := h.db.CreateProject(req.Name, userID)
	if err != nil { writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return }

	writeJSON(w, http.StatusCreated, project)
}

func (h *ProjectHandler) List(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projects, err := h.db.ListProjectsForUser(userID)
	if err != nil { writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return }
	if projects == nil { projects = []models.Project{} }

	writeJSON(w, http.StatusOK, projects)
}

func (h *ProjectHandler) Members(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	members, err := h.db.GetProjectMembers(projectID)
	if err != nil { writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return }
	if members == nil { members = []models.ProjectMember{} }

	writeJSON(w, http.StatusOK, members)
}

func (h *ProjectHandler) Invite(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")

	var req models.InviteMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"}); return
	}

	if err := h.db.InviteMember(projectID, req.UserID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "invited"})
}

func (h *ProjectHandler) PushDelta(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")

	var req models.PushDeltaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"}); return
	}
	if req.Data == "" { writeJSON(w, http.StatusBadRequest, map[string]string{"error": "data is required"}); return }

	delta, err := h.db.StoreDelta(projectID, userID, req.Data)
	if err != nil { writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return }

	writeJSON(w, http.StatusCreated, delta)
}

func (h *ProjectHandler) PullDeltas(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	sinceStr := r.URL.Query().Get("since")

	var since time.Time
	if sinceStr != "" {
		since, _ = time.Parse(time.RFC3339, sinceStr)
	}

	deltas, err := h.db.GetDeltas(projectID, since)
	if err != nil { writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return }
	if deltas == nil { deltas = []models.ProjectDelta{} }

	writeJSON(w, http.StatusOK, deltas)
}
