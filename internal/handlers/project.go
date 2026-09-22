package handlers

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/orbit/control-server/internal/middleware"
	"github.com/orbit/control-server/internal/models"
	"github.com/orbit/control-server/internal/repository"
)

const maxRequestBodySize = 26 << 20 // 26 MiB — must exceed maxDeltaDataSize (25 MB) + JSON envelope

// Tier limits: Free = 1 owned project / 2 peers (3 members total).
// Pro = 10 projects at a time / 10 peers per project (11 members total).
const (
	tierMaxProjectsFree = 1
	tierMaxProjectsPro  = 10
	tierMaxMembersFree  = 3 // owner + 2 peers
	tierMaxMembersPro   = 11 // owner + 10 peers
)

func maxProjectsForTier(tier string) int {
	switch tier {
	case "free":
		return tierMaxProjectsFree
	case "pro":
		return tierMaxProjectsPro
	default: // enterprise and any unknown tier
		return -1 // no cap
	}
}

func maxMembersForTier(tier string) int {
	switch tier {
	case "free":
		return tierMaxMembersFree
	case "pro":
		return tierMaxMembersPro
	default: // enterprise and any unknown tier
		return -1 // no cap
	}
}

func projectLimitMessage(tier string) string {
	switch tier {
	case "free":
		return "Free tier is limited to 1 project at a time (owned or joined). Upgrade to Pro for more."
	case "pro":
		return "Pro tier is limited to 10 projects at a time (owned or joined)."
	default:
		return "project limit reached"
	}
}

func memberLimitMessage(tier string) string {
	switch tier {
	case "free":
		return "Free tier is limited to 2 peers per project (3 members total). Upgrade to Pro for up to 10 peers."
	case "pro":
		return "Pro tier is limited to 10 peers per project (11 members total)."
	default:
		return "member limit reached"
	}
}

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

	user, err := h.db.GetUserByID(userID)
	if err != nil || user == nil { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "user not found"}); return }

	// The project cap counts BOTH owned and joined projects ("10 projects at a
	// time"), so joining more projects cannot be a workaround for the cap.
	if max := maxProjectsForTier(user.PlanTier); max >= 0 {
		if count, _ := h.db.CountProjectsForUser(userID); count >= max {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": projectLimitMessage(user.PlanTier)})
			return
		}
	}

	project, err := h.db.CreateProject(req.Name, req.Language, req.Domain, userID, req.ProjectToken)
	if err != nil { writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return }

	writeJSON(w, http.StatusCreated, project)
}

// checkProjectCap returns true (and writes the 403 response) if adding userID to
// a project would put them over their tier's project cap (owned + joined).
func (h *ProjectHandler) checkProjectCap(w http.ResponseWriter, user *models.User) bool {
	if user == nil {
		return false
	}
	if max := maxProjectsForTier(user.PlanTier); max >= 0 {
		if count, _ := h.db.CountProjectsForUser(user.ID); count >= max {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": projectLimitMessage(user.PlanTier)})
			return true
		}
	}
	return false
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
	if !h.db.IsProjectOwner(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only owners can invite members"}); return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	var req models.InviteMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"}); return
	}

	user, err := h.db.GetUserByID(userID)
	if err != nil || user == nil { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "user not found"}); return }

	// The invitee also counts against their own project cap (owned + joined) —
	// but a redundant re-invite of an existing member is a no-op, not a blocker.
	invitee, _ := h.db.GetUserByID(req.UserID)
	blocked := false
	if invitee != nil && !h.db.IsProjectMember(projectID, invitee.ID) {
		blocked = h.checkProjectCap(w, invitee)
	}
	if !blocked {
		if max := maxMembersForTier(user.PlanTier); max >= 0 {
			if err := h.db.InviteMemberWithLimit(projectID, req.UserID, max); err != nil {
				if errors.Is(err, repository.ErrMemberLimitReached) {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": memberLimitMessage(user.PlanTier)})
					return
				}
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
		} else {
			if err := h.db.InviteMember(projectID, req.UserID); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "invited"})
}

// Audit Fix #40: Invite token system — generates deterministic verifiable token
func (h *ProjectHandler) GenerateToken(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	if !h.db.IsProjectOwner(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only owners can generate invite tokens"}); return
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

	project, err := h.db.GetProject(projectID)
	if err != nil || project == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"}); return
	}

	owner, _ := h.db.GetUserByID(project.OwnerID)
	if owner == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "project owner not found"}); return
	}
	// The joiner counts against their own project cap too (owned + joined).
	joiner, _ := h.db.GetUserByID(userID)
	blocked := false
	if joiner != nil && !h.db.IsProjectMember(projectID, joiner.ID) {
		blocked = h.checkProjectCap(w, joiner)
	}
	if !blocked {
		if max := maxMembersForTier(owner.PlanTier); max >= 0 {
			if err := h.db.InviteMemberWithLimit(projectID, userID, max); err != nil {
				if errors.Is(err, repository.ErrMemberLimitReached) {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": memberLimitMessage(owner.PlanTier)})
					return
				}
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
			}
		} else {
			if err := h.db.InviteMember(projectID, userID); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "joined", "projectId": projectID})
}

// JoinProject allows an authenticated user to join a project by ID — but only
// when they present the project's E2EE token (orbit-sec-...) from an invite
// link. The token is compared in constant time so a guessed project ID alone
// cannot grant membership. JoinByToken (orbit_inv_...) remains for invite-coded
// links.
func (h *ProjectHandler) JoinProject(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	projectID := chi.URLParam(r, "id")
	if projectID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project ID required"})
		return
	}

	project, err := h.db.GetProject(projectID)
	if err != nil || project == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		return
	}

	// The E2EE project token is only distributed via invite links, so it must be
	// presented before membership (or re-verified membership) is granted.
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.Token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "E2EE project token (orbit-sec-...) is required to join this workspace"})
		return
	}
	if project.ProjectToken == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this workspace has no E2EE token on file; ask the owner to rotate the project token and try again"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(project.ProjectToken)) != 1 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "invalid project token"})
		return
	}

	// If already a member, return OK
	if h.db.IsProjectMember(projectID, userID) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "already_member", "projectId": projectID})
		return
	}

	owner, _ := h.db.GetUserByID(project.OwnerID)
	if owner == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "project owner not found"})
		return
	}
	// The joiner counts against their own project cap too (owned + joined).
	joiner, _ := h.db.GetUserByID(userID)
	blocked := false
	if joiner != nil && !h.db.IsProjectMember(projectID, joiner.ID) {
		blocked = h.checkProjectCap(w, joiner)
	}
	if !blocked {
		if max := maxMembersForTier(owner.PlanTier); max >= 0 {
			if err := h.db.InviteMemberWithLimit(projectID, userID, max); err != nil {
				if errors.Is(err, repository.ErrMemberLimitReached) {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": memberLimitMessage(owner.PlanTier)})
					return
				}
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
		} else {
			if err := h.db.InviteMember(projectID, userID); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
		}
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
	const maxDeltaDataSize = 25 * 1024 * 1024 // 25 MB — must stay under maxRequestBodySize (26 MiB)
	if len(req.Data) > maxDeltaDataSize {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "delta data exceeds 25 MB limit"}); return
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
	if req.ProjectToken != "" {
		project.ProjectToken = req.ProjectToken
	}
	if err := h.db.UpdateProject(project); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
	}

	writeJSON(w, http.StatusOK, project)
}

func (h *ProjectHandler) UpdateToken(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	if !h.db.IsProjectOwner(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "owner only"}); return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	var req models.UpdateTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"}); return
	}
	if req.ProjectToken == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "projectToken is required"}); return
	}

	if err := h.db.UpdateProjectToken(projectID, req.ProjectToken); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "token_updated"})
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

// RemoveMember lets the project owner evict a collaborator. The removed member's
// client detects the now-403 project endpoints on its next poll and performs a
// local graceful exit (drops the project + cleans .orbit).
func (h *ProjectHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	targetID := chi.URLParam(r, "userId")
	if projectID == "" || targetID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project ID and user ID required"}); return
	}
	if !h.db.IsProjectOwner(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "owner only"}); return
	}
	if targetID == userID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "owners cannot remove themselves; delete the project instead"}); return
	}

	if err := h.db.RemoveProjectMember(projectID, targetID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed", "projectId": projectID})
}

// LeaveProject is the self-serve exit for collaborators: it removes only the
// caller's membership and leaves the project + its data intact for the owner.
// The caller's client then drops the workspace locally and cleans .orbit.
func (h *ProjectHandler) LeaveProject(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	if projectID == "" { writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project ID required"}); return }
	if !h.db.IsProjectMember(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member"}); return
	}
	if h.db.IsProjectOwner(projectID, userID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "owners cannot leave; delete the project instead"}); return
	}

	if err := h.db.RemoveProjectMember(projectID, userID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "left", "projectId": projectID})
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

	// Gap detection: when the client sends a `since` cursor and the relay has
	// since purged deltas (all-ack or 3-day TTL), an incremental pull would
	// silently skip the blobs the cursor can never catch up with. Flag it via
	// the X-Orbit-Delta-Gap header so the client re-syncs full state from peers
	// instead of believing it is up to date. A missing cursor (bootstrap) is
	// never a gap — the client is already requesting complete history.
	if !since.IsZero() {
		wm, err := h.db.GetDeltaPurgeWatermark(projectID)
		if err == nil && !wm.IsZero() && since.Before(wm) {
			w.Header().Set("X-Orbit-Delta-Gap", "1")
		}
	}

	// Bound every pull response (default 200, hard max 500) so a long-running
	// project or a burst of pushes can never produce an unbounded reply. Deltas
	// are stored oldest-first and we keep the FIRST `limit` entries, so a client
	// that advances its `since` cursor past the last returned delta can chain
	// consecutive pages and drain the whole backlog without gaps. (Keeping the
	// newest `limit` instead would silently drop deltas that fall outside the
	// window whenever a backlog exceeds the bound.)
	//
	// Bootstrap carve-out: when NO `since` cursor is supplied the caller is a
	// fresh device rebuilding the project from scratch (relay-only clone), which
	// needs the complete history — so the much larger ceiling applies there.
	// Incremental polls always send a cursor and stay bounded to 200/500.
	const (
		defaultPullLimit = 200
		maxPullLimit     = 500
		bootstrapCeiling = 5000 // relay bootstrap: whole (bounded) history
	)
	limit := defaultPullLimit
	if q := r.URL.Query().Get("limit"); q != "" {
		if parsed, perr := strconv.Atoi(q); perr == nil && parsed > 0 {
			if parsed > maxPullLimit {
				parsed = maxPullLimit
			}
			limit = parsed
		}
	} else if sinceStr == "" {
		limit = bootstrapCeiling
	}
	if len(deltas) > limit {
		deltas = deltas[:limit]
	}

	sanitized := make([]models.ProjectDelta, len(deltas))
	for i, d := range deltas {
		sanitized[i] = d
		sanitized[i].Author = models.PublicUser{
			ID:          d.Author.ID,
			Name:        d.Author.Name,
			DisplayName: d.Author.Name,
			Status:      d.Author.Status,
		}
		sanitized[i].Author.Email = ""
		sanitized[i].Author.Bio = ""
	}

	writeJSON(w, http.StatusOK, sanitized)
}

func (h *ProjectHandler) AckDelta(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	if !h.db.IsProjectMember(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member"}); return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		DeltaID string `json:"deltaId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"}); return
	}
	if req.DeltaID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "deltaId is required"}); return
	}

	if err := h.db.AckDelta(projectID, req.DeltaID, userID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "acked"})
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
	if req.Priority == "" { req.Priority = "medium" }
	if req.Tag == "" { req.Tag = "feature" }

	task, err := h.db.CreateTask(projectID, req.Title, req.AssigneeID, userID, req.Priority, req.Tag)
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

func (h *ProjectHandler) UpdateTask(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	if userID == "" { writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"}); return }

	projectID := chi.URLParam(r, "id")
	if !h.db.IsProjectMember(projectID, userID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a member"}); return
	}

	taskID := chi.URLParam(r, "taskId")

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	var req models.UpdateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"}); return
	}

	task, err := h.db.UpdateTask(projectID, taskID, req)
	if err != nil { writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()}); return }

	writeJSON(w, http.StatusOK, task)
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

	cleaned := req.Path
	if strings.Contains(cleaned, "..") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path cannot contain '..' components"}); return
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
