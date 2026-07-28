package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/orbit/control-server/internal/models"
)

type store struct {
	Users          map[string]*models.User           `json:"users"`
	LicenseIndex   map[string]string                 `json:"licenseIndex"` // licenseKey → userID
	FriendRequests map[string]*models.FriendRequest   `json:"friendRequests"`
	Friends        map[string][]string                `json:"friends"`
	Projects       map[string]*models.Project         `json:"projects"`
	ProjectMembers map[string][]models.ProjectMember  `json:"projectMembers"`
	Tasks          map[string][]*models.Task          `json:"tasks"`
	Deltas         map[string][]models.ProjectDelta   `json:"deltas"`
	ActivityLogs   []models.ActivityLog               `json:"activityLogs"`
	Messages       map[string][]*models.ChatMessage   `json:"messages"`
	Signals        []Signal                           `json:"signals"`
}

type Signal struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	FromPeer  string `json:"fromPeer"`
	ToPeer    string `json:"toPeer"`
	Type      string `json:"type"`
	Payload   string `json:"payload"`
	CreatedAt time.Time `json:"createdAt"`
}

type DB struct {
	mu   sync.RWMutex
	path string
	data *store
}

func New(path string) (*DB, error) {
	db := &DB{path: path, data: &store{
		Users:          make(map[string]*models.User),
		LicenseIndex:   make(map[string]string),
		FriendRequests: make(map[string]*models.FriendRequest),
		Friends:        make(map[string][]string),
		Projects:       make(map[string]*models.Project),
		ProjectMembers: make(map[string][]models.ProjectMember),
		Tasks:          make(map[string][]*models.Task),
		Deltas:         make(map[string][]models.ProjectDelta),
		ActivityLogs:   []models.ActivityLog{},
		Messages:       make(map[string][]*models.ChatMessage),
		Signals:        []Signal{},
	}}
	if err := db.load(); err != nil {
		return nil, fmt.Errorf("load db: %w", err)
	}
	// Ensure LicenseIndex is initialized after loading legacy data
	if db.data.LicenseIndex == nil {
		db.data.LicenseIndex = make(map[string]string)
	}
	return db, nil
}

func (db *DB) Close() error {
	return db.save()
}

// Shutdown performs a final save under write lock to guarantee all modifications
// are persisted. Call during graceful shutdown after background goroutines stop.
func (db *DB) Shutdown() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.saveUnderLock()
}

// saveUnderLock marshals and writes while caller holds the write lock.
// Used only by Shutdown to ensure a consistent final snapshot.
func (db *DB) saveUnderLock() error {
	data, err := json.MarshalIndent(db.data, "", "  ")
	if err != nil { return err }
	dir := filepath.Dir(db.path)
	tmp, err := os.CreateTemp(dir, "orbit-*.tmp")
	if err != nil { return err }
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close(); os.Remove(tmpPath)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close(); os.Remove(tmpPath)
		return err
	}
	tmp.Close()
	return os.Rename(tmpPath, db.path)
}

func (db *DB) load() error {
	data, err := os.ReadFile(db.path)
	if err != nil {
		if os.IsNotExist(err) { return nil }
		return err
	}
	return json.Unmarshal(data, db.data)
}

func (db *DB) save() error {
	db.mu.RLock()
	data, err := json.MarshalIndent(db.data, "", "  ")
	db.mu.RUnlock()
	if err != nil { return err }

	dir := filepath.Dir(db.path)
	tmp, err := os.CreateTemp(dir, "orbit-*.tmp")
	if err != nil { return err }
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	tmp.Close()

	return os.Rename(tmpPath, db.path)
}

func generateID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		log.Printf("[CRYPTO] rand.Read failed: %v — using timestamp fallback", err)
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(b)
}

// UpsertUser creates a new user or updates an existing one based on the stable UserID
// from the license validator. No passwords. No bcrypt.
func (db *DB) UpsertUser(id, name, email, planTier, licenseKey string) (*models.User, error) {
	db.mu.Lock()
	now := time.Now().UTC()
	existing := db.data.Users[id]
	if existing != nil {
		existing.DisplayName = name
		existing.Email = email
		existing.PlanTier = planTier
		existing.UpdatedAt = now
	} else {
		existing = &models.User{
			ID:          id,
			DisplayName: name,
			Email:       email,
			PlanTier:    planTier,
			Bio:         "",
			Status:      "online",
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		db.data.Users[id] = existing
	}
	db.data.LicenseIndex[licenseKey] = id
	u := *existing
	db.mu.Unlock()

	if err := db.save(); err != nil { return nil, err }
	return &u, nil
}

// GetUserByLicenseKey looks up a user by their license key.
func (db *DB) GetUserByLicenseKey(key string) (*models.User, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	id, ok := db.data.LicenseIndex[key]
	if !ok { return nil, nil }
	user := db.data.Users[id]
	if user == nil { return nil, nil }
	u := *user
	return &u, nil
}

func (db *DB) GetUserByID(id string) (*models.User, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	user := db.data.Users[id]
	if user == nil { return nil, nil }
	u := *user
	return &u, nil
}

func (db *DB) SearchUsers(query string, limit int) ([]models.UserSearchResult, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	if limit <= 0 || limit > 50 { limit = 20 }
	pattern := query

	var results []models.UserSearchResult
	for _, u := range db.data.Users {
		if pattern == "" || contains(u.ID, pattern) || contains(u.DisplayName, pattern) || contains(u.Email, pattern) {
			results = append(results, models.UserSearchResult{
				ID: u.ID, DisplayName: u.DisplayName, Email: u.Email,
				Bio: u.Bio, Status: u.Status, AvatarURL: u.AvatarURL,
				PublicKeyFingerprint: u.PublicKeyFingerprint,
			})
			if len(results) >= limit { break }
		}
	}
	return results, nil
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSub(s, substr)
}

func searchSub(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			ci := s[i+j]
			cj := sub[j]
			if ci >= 'A' && ci <= 'Z' { ci += 32 }
			if cj >= 'A' && cj <= 'Z' { cj += 32 }
			if ci != cj { match = false; break }
		}
		if match { return true }
	}
	return false
}

func (db *DB) UpdateProfile(id, displayName, bio, avatarURL string) error {
	db.mu.Lock()
	u := db.data.Users[id]
	if u == nil { db.mu.Unlock(); return fmt.Errorf("user not found") }
	u.DisplayName = displayName
	u.Bio = bio
	u.AvatarURL = avatarURL
	u.UpdatedAt = time.Now().UTC()
	db.mu.Unlock()
	return db.save()
}

func (db *DB) UpdateStatus(id, status string) error {
	db.mu.Lock()
	u := db.data.Users[id]
	if u == nil { db.mu.Unlock(); return fmt.Errorf("user not found") }
	u.Status = status
	db.mu.Unlock()
	return db.save()
}

func (db *DB) UpdatePublicKey(id, fingerprint string) error {
	db.mu.Lock()
	u := db.data.Users[id]
	if u == nil { db.mu.Unlock(); return fmt.Errorf("user not found") }
	u.PublicKeyFingerprint = fingerprint
	db.mu.Unlock()
	return db.save()
}

func (db *DB) SendFriendRequest(fromID, toID string) (*models.FriendRequest, error) {
	db.mu.Lock()
	if fromID == toID { db.mu.Unlock(); return nil, fmt.Errorf("cannot send request to yourself") }
	if db.data.Users[fromID] == nil { db.mu.Unlock(); return nil, fmt.Errorf("sender not found") }
	if db.data.Users[toID] == nil { db.mu.Unlock(); return nil, fmt.Errorf("recipient not found") }

	fr := &models.FriendRequest{
		ID: generateID("frq"), FromID: fromID, ToID: toID,
		Status: "pending", CreatedAt: time.Now().UTC(),
	}
	if existing, exists := db.data.FriendRequests[fromID+":"+toID]; exists {
		if existing.Status == "pending" {
			db.mu.Unlock()
			return nil, fmt.Errorf("friend request already exists")
		}
	}
	db.data.FriendRequests[fromID+":"+toID] = fr
	db.mu.Unlock()
	return fr, db.save()
}

func sliceContains(slice []string, val string) bool {
	for _, item := range slice {
		if item == val { return true }
	}
	return false
}

func (db *DB) AcceptFriendRequest(requestID, userID string) error {
	db.mu.Lock()
	for _, fr := range db.data.FriendRequests {
		if fr.ID == requestID && fr.Status == "pending" {
			if fr.ToID != userID { db.mu.Unlock(); return fmt.Errorf("not authorized to accept this request") }
			fr.Status = "accepted"
			if !sliceContains(db.data.Friends[fr.FromID], fr.ToID) {
				db.data.Friends[fr.FromID] = append(db.data.Friends[fr.FromID], fr.ToID)
			}
			if !sliceContains(db.data.Friends[fr.ToID], fr.FromID) {
				db.data.Friends[fr.ToID] = append(db.data.Friends[fr.ToID], fr.FromID)
			}
			db.mu.Unlock()
			return db.save()
		}
	}
	db.mu.Unlock()
	return fmt.Errorf("pending request not found")
}

func (db *DB) RejectFriendRequest(requestID, userID string) error {
	db.mu.Lock()
	for _, fr := range db.data.FriendRequests {
		if fr.ID == requestID && fr.Status == "pending" {
			if fr.ToID != userID { db.mu.Unlock(); return fmt.Errorf("not authorized to decline this request") }
			fr.Status = "rejected"
			db.mu.Unlock()
			return db.save()
		}
	}
	db.mu.Unlock()
	return fmt.Errorf("pending request not found")
}

func (db *DB) GetPendingRequests(userID string) ([]models.FriendRequest, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var requests []models.FriendRequest
	for _, fr := range db.data.FriendRequests {
		if fr.ToID == userID && fr.Status == "pending" {
			from := db.data.Users[fr.FromID]
			req := *fr
			if from != nil {
				req.From = models.UserSearchResult{
					ID: from.ID, DisplayName: from.DisplayName, Email: from.Email,
					Bio: from.Bio, Status: from.Status, AvatarURL: from.AvatarURL,
				}
			}
			requests = append(requests, req)
		}
	}
	return requests, nil
}

func (db *DB) GetFriends(userID string) ([]models.Friend, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	seen := make(map[string]bool)
	var friends []models.Friend
	for _, friendID := range db.data.Friends[userID] {
		if seen[friendID] { continue }
		seen[friendID] = true
		u := db.data.Users[friendID]
		if u == nil { continue }
		friends = append(friends, models.Friend{
			ID: u.ID, DisplayName: u.DisplayName, Email: u.Email,
			Bio: u.Bio, Status: u.Status, AvatarURL: u.AvatarURL, Online: u.Status == "online",
			PublicKeyFingerprint: u.PublicKeyFingerprint,
		})
	}
	return friends, nil
}

func (db *DB) CreateProject(name, language, domain, ownerID string) (*models.Project, error) {
	db.mu.Lock()
	p := &models.Project{
		ID: generateID("prj"), Name: name, Language: language, Domain: domain, OwnerID: ownerID, CreatedAt: time.Now().UTC(),
	}
	db.data.Projects[p.ID] = p
	db.data.ProjectMembers[p.ID] = []models.ProjectMember{
		{ProjectID: p.ID, UserID: ownerID, Role: "owner"},
	}
	db.mu.Unlock()
	return p, db.save()
}

func (db *DB) GetProject(id string) (*models.Project, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	p := db.data.Projects[id]
	if p == nil { return nil, nil }
	cp := *p
	return &cp, nil
}

func (db *DB) ListProjectsForUser(userID string) ([]models.Project, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var projects []models.Project
	for _, p := range db.data.Projects {
		members := db.data.ProjectMembers[p.ID]
		for _, m := range members {
			if m.UserID == userID {
				projects = append(projects, *p)
				break
			}
		}
	}
	return projects, nil
}

func (db *DB) InviteMember(projectID, userID string) error {
	db.mu.Lock()
	if db.data.Users[userID] == nil { db.mu.Unlock(); return fmt.Errorf("user not found") }
	members := db.data.ProjectMembers[projectID]
	for _, m := range members {
		if m.UserID == userID { db.mu.Unlock(); return nil }
	}
	db.data.ProjectMembers[projectID] = append(members, models.ProjectMember{
		ProjectID: projectID, UserID: userID, Role: "member", Path: "",
	})
	db.mu.Unlock()
	return db.save()
}

func (db *DB) GetProjectMembers(projectID string) ([]models.ProjectMember, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	members := db.data.ProjectMembers[projectID]
	if members == nil { return []models.ProjectMember{}, nil }

	result := make([]models.ProjectMember, len(members))
	for i, m := range members {
		u := db.data.Users[m.UserID]
		result[i] = m
		if u != nil {
			result[i].User = models.UserSearchResult{
				ID: u.ID, DisplayName: u.DisplayName, Email: u.Email,
				Bio: u.Bio, Status: u.Status, AvatarURL: u.AvatarURL,
				PublicKeyFingerprint: u.PublicKeyFingerprint,
			}
		}
	}
	return result, nil
}

func (db *DB) UpdateProject(p *models.Project) error {
	db.mu.Lock()
	existing := db.data.Projects[p.ID]
	if existing == nil { db.mu.Unlock(); return fmt.Errorf("project not found") }
	existing.Name = p.Name
	db.mu.Unlock()
	return db.save()
}

func (db *DB) StoreDelta(projectID, authorID, data string) (*models.ProjectDelta, error) {
	db.mu.Lock()
	d := &models.ProjectDelta{
		ID: generateID("dlt"), ProjectID: projectID,
		AuthorID: authorID, Data: data, CreatedAt: time.Now().UTC(),
	}
	db.data.Deltas[projectID] = append(db.data.Deltas[projectID], *d)
	db.mu.Unlock()
	return d, db.save()
}

func (db *DB) GetDeltas(projectID string, since time.Time) ([]models.ProjectDelta, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	all := db.data.Deltas[projectID]
	var result []models.ProjectDelta
	for _, d := range all {
		if d.CreatedAt.After(since) {
			u := db.data.Users[d.AuthorID]
			if u != nil {
				d.Author = models.PublicUser{
					ID: u.ID, Name: u.DisplayName, Status: u.Status,
				}
			}
			result = append(result, d)
		}
	}
	return result, nil
}

// SweepExpiredDeltas removes all encrypted relay blobs older than the TTL (7 days).
// This is the "Rolling Window" approach from the Encrypted Cloud Relay spec.
func (db *DB) SweepExpiredDeltas(ttl time.Duration) int {
	db.mu.Lock()
	cutoff := time.Now().UTC().Add(-ttl)
	swept := 0

	for projectID, deltas := range db.data.Deltas {
		var kept []models.ProjectDelta
		for _, d := range deltas {
			if d.CreatedAt.After(cutoff) {
				kept = append(kept, d)
			} else {
				swept++
			}
		}
		if len(kept) == 0 {
			delete(db.data.Deltas, projectID)
		} else {
			db.data.Deltas[projectID] = kept
		}
	}
	db.mu.Unlock()

	if swept > 0 {
		if err := db.save(); err != nil {
			log.Printf("[delta-sweep] save failed: %v", err)
		}
	}
	return swept
}

// StartDeltaSweeperWithCtx launches a background goroutine that periodically purges
// expired encrypted relay blobs. It runs every hour with a 7-day TTL.
// The goroutine stops when ctx is cancelled.
func (db *DB) StartDeltaSweeperWithCtx(ctx context.Context) {
	const deltaTTL = 7 * 24 * time.Hour
	const sweepInterval = 1 * time.Hour

	go func() {
		// Run an initial sweep on startup
		if n := db.SweepExpiredDeltas(deltaTTL); n > 0 {
			log.Printf("[relay-gc] Startup sweep: purged %d expired delta(s)\n", n)
		}

		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if n := db.SweepExpiredDeltas(deltaTTL); n > 0 {
					log.Printf("[relay-gc] Periodic sweep: purged %d expired delta(s)\n", n)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (db *DB) CreateTask(projectID, title, assigneeID, creatorID string) (*models.Task, error) {
	db.mu.Lock()
	task := &models.Task{
		ID: generateID("tsk"), ProjectID: projectID, Title: title, AssigneeID: assigneeID, CreatorID: creatorID, Status: "open", CreatedAt: time.Now().UTC(),
	}
	db.data.Tasks[projectID] = append(db.data.Tasks[projectID], task)
	ct := *task
	db.mu.Unlock()

	if err := db.save(); err != nil { return nil, err }
	return &ct, nil
}

func (db *DB) GetTasks(projectID string) ([]models.Task, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	tasks := db.data.Tasks[projectID]
	var result []models.Task
	for _, t := range tasks {
		ct := *t
		if u := db.data.Users[ct.AssigneeID]; u != nil {
			ct.Assignee = models.UserSearchResult{
				ID: u.ID, DisplayName: u.DisplayName, Email: u.Email, AvatarURL: u.AvatarURL,
			}
		}
		result = append(result, ct)
	}
	return result, nil
}

func (db *DB) CompleteTask(projectID, taskID string) (*models.Task, error) {
	db.mu.Lock()
	tasks := db.data.Tasks[projectID]
	for _, t := range tasks {
		if t.ID == taskID {
			t.Status = "completed"
			now := time.Now().UTC()
			t.CompletedAt = &now
			ct := *t
			db.mu.Unlock()
			if err := db.save(); err != nil { return nil, err }
			return &ct, nil
		}
	}
	db.mu.Unlock()
	return nil, fmt.Errorf("task not found")
}

func (db *DB) LogActivity(userID, projectID, action string) error {
	db.mu.Lock()
	log := models.ActivityLog{
		ID: generateID("act"), UserID: userID, ProjectID: projectID, Action: action, CreatedAt: time.Now().UTC(),
	}
	db.data.ActivityLogs = append(db.data.ActivityLogs, log)
	db.mu.Unlock()
	return db.save()
}

func (db *DB) UpdatePresence(userID, activity string) error {
	db.mu.Lock()
	u := db.data.Users[userID]
	if u == nil { db.mu.Unlock(); return fmt.Errorf("user not found") }
	u.Activity = activity
	u.Status = "online"
	u.LastSeen = time.Now().UTC()
	db.mu.Unlock()
	return db.save()
}

// HeartbeatSweep marks all users offline, then each active client's
// periodic presence call brings them back online.
func (db *DB) HeartbeatSweep() {
	db.mu.Lock()
	cutoff := time.Now().UTC().Add(-90 * time.Second)
	for _, u := range db.data.Users {
		if u.LastSeen.IsZero() || u.LastSeen.Before(cutoff) {
			u.Status = "offline"
		}
	}
	db.mu.Unlock()

	if err := db.save(); err != nil {
		log.Printf("[heartbeat] save failed: %v", err)
	}
}

func (db *DB) GetPulse(userID string) ([]models.PulseEntry, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	thirtyDaysAgo := time.Now().Add(-30 * 24 * time.Hour)
	daily := make(map[string]*models.PulseEntry)

	for _, log := range db.data.ActivityLogs {
		if log.UserID == userID && log.CreatedAt.After(thirtyDaysAgo) {
			dateStr := log.CreatedAt.Format("2006-01-02")
			if daily[dateStr] == nil {
				daily[dateStr] = &models.PulseEntry{Date: dateStr}
			}
			if log.Action == "task_completed" {
				daily[dateStr].TasksCompleted++
			} else if log.Action == "delta_pushed" {
				daily[dateStr].DeltasPushed++
			}
		}
	}

	var results []models.PulseEntry
	for _, entry := range daily {
		results = append(results, *entry)
	}
	return results, nil
}

func (db *DB) GetLeaderboard(projectID string) ([]models.LeaderboardEntry, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	sevenDaysAgo := time.Now().Add(-7 * 24 * time.Hour)
	userStats := make(map[string]*models.LeaderboardEntry)

	for _, log := range db.data.ActivityLogs {
		if log.ProjectID == projectID && log.CreatedAt.After(sevenDaysAgo) {
			if userStats[log.UserID] == nil {
				u := db.data.Users[log.UserID]
				if u == nil { continue }
				userStats[log.UserID] = &models.LeaderboardEntry{
					User: models.UserSearchResult{
						ID: u.ID, DisplayName: u.DisplayName, Email: u.Email, AvatarURL: u.AvatarURL,
					},
				}
			}
			if log.Action == "task_completed" {
				userStats[log.UserID].TasksCompleted++
				userStats[log.UserID].TotalScore += 10
			} else if log.Action == "delta_pushed" {
				userStats[log.UserID].DeltasPushed++
				userStats[log.UserID].TotalScore += 5
			}
		}
	}

	var results []models.LeaderboardEntry
	for _, stat := range userStats {
		results = append(results, *stat)
	}
	return results, nil
}

func (db *DB) IsProjectMember(projectID, userID string) bool {
	db.mu.RLock()
	defer db.mu.RUnlock()

	for _, m := range db.data.ProjectMembers[projectID] {
		if m.UserID == userID {
			return true
		}
	}
	return false
}

func (db *DB) IsProjectOwner(projectID, userID string) bool {
	db.mu.RLock()
	defer db.mu.RUnlock()

	for _, m := range db.data.ProjectMembers[projectID] {
		if m.UserID == userID && m.Role == "owner" {
			return true
		}
	}
	return false
}

func (db *DB) UpdateMemberPath(projectID, userID, path string) error {
	db.mu.Lock()
	members := db.data.ProjectMembers[projectID]
	updated := false
	for i, m := range members {
		if m.UserID == userID {
			members[i].Path = path
			updated = true
			break
		}
	}
	if !updated {
		db.mu.Unlock()
		return fmt.Errorf("member not found in project")
	}
	db.data.ProjectMembers[projectID] = members
	db.mu.Unlock()
	return db.save()
}

func (db *DB) SaveMessage(projectID, authorID, text string) (*models.ChatMessage, error) {
	db.mu.Lock()
	m := &models.ChatMessage{
		ID:        generateID("msg"),
		ProjectID: projectID,
		AuthorID:  authorID,
		Text:      text,
		CreatedAt: time.Now().UTC(),
	}
	db.data.Messages[projectID] = append(db.data.Messages[projectID], m)
	u := db.data.Users[authorID]
	if u != nil {
		m.Author = models.UserSearchResult{
			ID:          u.ID,
			DisplayName: u.DisplayName,
			AvatarURL:   u.AvatarURL,
		}
	}
	db.mu.Unlock()

	if err := db.save(); err != nil {
		return nil, err
	}
	return m, nil
}

func (db *DB) GetMessages(projectID string, offset, limit int) ([]models.ChatMessage, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	if limit <= 0 || limit > 200 { limit = 100 }
	if offset < 0 { offset = 0 }

	msgs := db.data.Messages[projectID]
	var result []models.ChatMessage
	for i, m := range msgs {
		if i < offset { continue }
		if len(result) >= limit { break }
		cm := *m
		u := db.data.Users[cm.AuthorID]
		if u != nil {
			cm.Author = models.UserSearchResult{
				ID:          u.ID,
				DisplayName: u.DisplayName,
				AvatarURL:   u.AvatarURL,
			}
		}
		result = append(result, cm)
	}
	return result, nil
}

func (db *DB) DeleteTask(projectID, taskID string) error {
	db.mu.Lock()
	tasks := db.data.Tasks[projectID]
	var kept []*models.Task
	for _, t := range tasks {
		if t.ID != taskID {
			kept = append(kept, t)
		}
	}
	db.data.Tasks[projectID] = kept
	db.mu.Unlock()
	return db.save()
}

func (db *DB) DeleteProject(projectID string) error {
	db.mu.Lock()
	delete(db.data.Projects, projectID)
	delete(db.data.ProjectMembers, projectID)
	delete(db.data.Tasks, projectID)
	delete(db.data.Deltas, projectID)
	delete(db.data.Messages, projectID)

	var keptSignals []Signal
	for _, s := range db.data.Signals {
		if s.ProjectID != projectID {
			keptSignals = append(keptSignals, s)
		}
	}
	db.data.Signals = keptSignals
	db.mu.Unlock()
	return db.save()
}

// MessageSweep deletes chat messages older than 30 days.
func (db *DB) MessageSweep() {
	db.mu.Lock()
	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
	dirty := false
	for pid, msgs := range db.data.Messages {
		var kept []*models.ChatMessage
		for _, m := range msgs {
			if m.CreatedAt.After(cutoff) {
				kept = append(kept, m)
			}
		}
		if len(kept) != len(msgs) {
			db.data.Messages[pid] = kept
			dirty = true
		}
	}
	db.mu.Unlock()

	if dirty {
		if err := db.save(); err != nil {
			log.Printf("[message-sweep] save failed: %v", err)
		}
	}
}

// ActivityLogSweep prunes activity logs older than 90 days.
func (db *DB) ActivityLogSweep() {
	db.mu.Lock()
	cutoff := time.Now().UTC().Add(-90 * 24 * time.Hour)
	var kept []models.ActivityLog
	for _, l := range db.data.ActivityLogs {
		if l.CreatedAt.After(cutoff) {
			kept = append(kept, l)
		}
	}
	dirty := len(kept) != len(db.data.ActivityLogs)
	if dirty {
		db.data.ActivityLogs = kept
	}
	db.mu.Unlock()

	if dirty {
		if err := db.save(); err != nil {
			log.Printf("[activity-sweep] save failed: %v", err)
		}
	}
}
