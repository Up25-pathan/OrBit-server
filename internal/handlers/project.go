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

	h.db.LogActivity(userID, projectID, "delta_pushed")

	writeJSON(w, http.StatusCreated, delta)
}

func (h *ProjectHandler) LogPush(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }
	projectID := chi.URLParam(r, "id")
	h.db.LogActivity(userID, projectID, "delta_pushed")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *ProjectHandler) Update(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")

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

	// Ideally we'd verify the user is the owner, but keeping it simple for now
	if err := h.db.DeleteProject(projectID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
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

func (h *ProjectHandler) CreateTask(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")

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
	tasks, err := h.db.GetTasks(projectID)
	if err != nil { writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return }
	if tasks == nil { tasks = []models.Task{} }

	writeJSON(w, http.StatusOK, tasks)
}

func (h *ProjectHandler) CompleteTask(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	taskID := chi.URLParam(r, "taskId")

	task, err := h.db.CompleteTask(projectID, taskID)
	if err != nil { writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return }

	// Log activity for the assignee
	h.db.LogActivity(task.AssigneeID, projectID, "task_completed")

	writeJSON(w, http.StatusOK, task)
}

func (h *ProjectHandler) DeleteTask(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	taskID := chi.URLParam(r, "taskId")

	if err := h.db.DeleteTask(projectID, taskID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *ProjectHandler) Leaderboard(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	leaderboard, err := h.db.GetLeaderboard(projectID)
	if err != nil { writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return }
	if leaderboard == nil { leaderboard = []models.LeaderboardEntry{} }

	writeJSON(w, http.StatusOK, leaderboard)
}

func (h *ProjectHandler) UpdateMemberPath(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"}); return
	}

	if err := h.db.UpdateMemberPath(projectID, userID, req.Path); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *ProjectHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")

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
	msgs, err := h.db.GetMessages(projectID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
	}
	if msgs == nil {
		msgs = []models.ChatMessage{}
	}

	writeJSON(w, http.StatusOK, msgs)
}
