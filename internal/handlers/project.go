package handlers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/orbit/control-server/internal/middleware"
	"github.com/orbit/control-server/internal/models"
	"github.com/orbit/control-server/internal/repository"
)

const maxRequestBodySize = 11 << 20 // 11 MiB — must exceed maxDeltaDataSize (10 MB) + JSON envelope

type ProjectHandler struct {
	db         *repository.DB
	inviteSalt string
}

func NewProjectHandler(db *repository.DB, inviteSalt string) *ProjectHandler {
	return &ProjectHandler{db: db, inviteSalt: inviteSalt}
}

func (h *ProjectHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	var req models.CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"}); return
	}
	if req.Name == "" { writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"}); return }

	project, err := h.db.CreateProject(req.Name, req.Language, req.Domain, userID)
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
	if !h.db.IsProjectMember(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member"}); return
	}

	members, err := h.db.GetProjectMembers(projectID)
	if err != nil { writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return }
	if members == nil { members = []models.ProjectMember{} }

	writeJSON(w, http.StatusOK, members)
}

func (h *ProjectHandler) Invite(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	if !h.db.IsProjectMember(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member"}); return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	var req models.InviteMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"}); return
	}

	if err := h.db.InviteMember(projectID, req.UserID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "invited"})
}

// Audit Fix #40: Invite token system — generates deterministic verifiable token
func (h *ProjectHandler) GenerateToken(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	if !h.db.IsProjectMember(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member"}); return
	}

	hash := sha256.Sum256([]byte(projectID + h.inviteSalt))
	token := fmt.Sprintf("orbit_inv_%x_%s", hash[:8], projectID)
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

type JoinTokenRequest struct {
	Token string `json:"token"`
}

func (h *ProjectHandler) JoinByToken(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	var req JoinTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"}); return
	}

	parts := strings.Split(req.Token, "_")
	if len(parts) < 4 || !strings.HasPrefix(req.Token, "orbit_inv_") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid invite token"}); return
	}

	projectID := strings.Join(parts[3:], "_")
	expectedHash := sha256.Sum256([]byte(projectID + h.inviteSalt))
	expectedPrefix := fmt.Sprintf("%x", expectedHash[:8])
	if parts[2] != expectedPrefix {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "invite token signature verification failed"}); return
	}

	if err := h.db.InviteMember(projectID, userID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "joined", "projectId": projectID})
}

func (h *ProjectHandler) PushDelta(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	if !h.db.IsProjectMember(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member"}); return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	var req models.PushDeltaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"}); return
	}
	if req.Data == "" { writeJSON(w, http.StatusBadRequest, map[string]string{"error": "data is required"}); return }
	const maxDeltaDataSize = 10 * 1024 * 1024 // 10 MB
	if len(req.Data) > maxDeltaDataSize {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "delta data exceeds 10 MB limit"}); return
	}

	delta, err := h.db.StoreDelta(projectID, userID, req.Data)
	if err != nil { writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return }

	h.db.LogActivity(userID, projectID, "delta_pushed")

	writeJSON(w, http.StatusCreated, delta)
}

func (h *ProjectHandler) Update(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	if !h.db.IsProjectOwner(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "owner only"}); return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	var req models.UpdateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"}); return
	}
	if req.Name == "" { writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"}); return }

	project, err := h.db.GetProject(projectID)
	if err != nil { writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server error"}); return }
	if project == nil { writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"}); return }

	project.Name = req.Name
	if err := h.db.UpdateProject(project); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
	}

	writeJSON(w, http.StatusOK, project)
}

func (h *ProjectHandler) DeleteProject(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	if !h.db.IsProjectOwner(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "owner only"}); return
	}

	if err := h.db.DeleteProject(projectID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *ProjectHandler) PullDeltas(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	if !h.db.IsProjectMember(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member"}); return
	}

	sinceStr := r.URL.Query().Get("since")

	var since time.Time
	if sinceStr != "" {
		parsed, err := time.Parse(time.RFC3339, sinceStr)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid 'since' timestamp, expected RFC3339"}); return
		}
		since = parsed
	}

	deltas, err := h.db.GetDeltas(projectID, since)
	if err != nil { writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return }
	if deltas == nil { deltas = []models.ProjectDelta{} }

	sanitized := make([]models.ProjectDelta, len(deltas))
	for i, d := range deltas {
		sanitized[i] = d
		sanitized[i].Author = models.PublicUser{
			ID:     d.Author.ID,
			Name:   d.Author.Name,
			Status: d.Author.Status,
		}
		sanitized[i].Author.Email = ""
		sanitized[i].Author.Bio = ""
	}

	writeJSON(w, http.StatusOK, sanitized)
}

func (h *ProjectHandler) CreateTask(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	if !h.db.IsProjectMember(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member"}); return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	var req models.CreateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"}); return
	}
	if req.Title == "" { writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title is required"}); return }
	if req.AssigneeID == "" { writeJSON(w, http.StatusBadRequest, map[string]string{"error": "assignee is required"}); return }

	task, err := h.db.CreateTask(projectID, req.Title, req.AssigneeID, userID)
	if err != nil { writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return }

	writeJSON(w, http.StatusCreated, task)
}

func (h *ProjectHandler) ListTasks(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	if !h.db.IsProjectMember(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member"}); return
	}

	tasks, err := h.db.GetTasks(projectID)
	if err != nil { writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return }
	if tasks == nil { tasks = []models.Task{} }

	writeJSON(w, http.StatusOK, tasks)
}

func (h *ProjectHandler) CompleteTask(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	if !h.db.IsProjectMember(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member"}); return
	}

	taskID := chi.URLParam(r, "taskId")

	task, err := h.db.CompleteTask(projectID, taskID)
	if err != nil { writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return }

	h.db.LogActivity(task.AssigneeID, projectID, "task_completed")

	writeJSON(w, http.StatusOK, task)
}

func (h *ProjectHandler) DeleteTask(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	if !h.db.IsProjectMember(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member"}); return
	}

	taskID := chi.URLParam(r, "taskId")

	if err := h.db.DeleteTask(projectID, taskID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
	}

	h.db.LogActivity(userID, projectID, "task_deleted")

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *ProjectHandler) Leaderboard(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	if !h.db.IsProjectMember(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member"}); return
	}

	leaderboard, err := h.db.GetLeaderboard(projectID)
	if err != nil { writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return }
	if leaderboard == nil { leaderboard = []models.LeaderboardEntry{} }

	writeJSON(w, http.StatusOK, leaderboard)
}

func (h *ProjectHandler) UpdateMemberPath(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	if !h.db.IsProjectMember(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member"}); return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"}); return
	}

	cleaned := filepath.Clean(req.Path)
	if strings.Contains(cleaned, "..") || filepath.IsAbs(cleaned) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path must be relative and contain no '..' components"}); return
	}

	if err := h.db.UpdateMemberPath(projectID, userID, cleaned); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *ProjectHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	if !h.db.IsProjectMember(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member"}); return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"}); return
	}
	if req.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text is required"}); return
	}

	msg, err := h.db.SaveMessage(projectID, userID, req.Text)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
	}

	writeJSON(w, http.StatusCreated, msg)
}

func (h *ProjectHandler) ListMessages(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	if !h.db.IsProjectMember(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member"}); return
	}

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if offset < 0 { offset = 0 }
	if limit <= 0 || limit > 200 { limit = 100 }

	msgs, err := h.db.GetMessages(projectID, offset, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
	}
	if msgs == nil {
		msgs = []models.ChatMessage{}
	}

	writeJSON(w, http.StatusOK, msgs)
}
