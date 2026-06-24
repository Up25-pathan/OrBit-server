package repository

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/orbit/control-server/internal/models"
	"golang.org/x/crypto/bcrypt"
)

type store struct {
	Users          map[string]*models.User           `json:"users"`
	Emails         map[string]string                  `json:"emails"`
	FriendRequests map[string]*models.FriendRequest   `json:"friendRequests"`
	Friends        map[string][]string                `json:"friends"`
	Projects       map[string]*models.Project         `json:"projects"`
	ProjectMembers map[string][]models.ProjectMember  `json:"projectMembers"`
	Deltas         map[string][]models.ProjectDelta   `json:"deltas"`
}

type DB struct {
	mu   sync.RWMutex
	path string
	data *store
}

func New(path string) (*DB, error) {
	db := &DB{path: path, data: &store{
		Users:          make(map[string]*models.User),
		Emails:         make(map[string]string),
		FriendRequests: make(map[string]*models.FriendRequest),
		Friends:        make(map[string][]string),
		Projects:       make(map[string]*models.Project),
		ProjectMembers: make(map[string][]models.ProjectMember),
		Deltas:         make(map[string][]models.ProjectDelta),
	}}
	if err := db.load(); err != nil {
		return nil, fmt.Errorf("load db: %w", err)
	}
	return db, nil
}

func (db *DB) Close() error {
	return db.save()
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
	data, err := json.MarshalIndent(db.data, "", "  ")
	if err != nil { return err }
	return os.WriteFile(db.path, data, 0644)
}

func generateID(prefix string) string {
	b := make([]byte, 8)
	rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

func (db *DB) CreateUser(req models.SignupRequest) (*models.User, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	if _, exists := db.data.Emails[req.Email]; exists {
		return nil, fmt.Errorf("email already registered")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil { return nil, fmt.Errorf("hash password: %w", err) }

	user := &models.User{
		ID:           generateID("usr"),
		DisplayName:  req.DisplayName,
		Email:        req.Email,
		PasswordHash: string(hash),
		Bio:          "",
		Status:       "offline",
		CreatedAt:    time.Now().UTC(),
	}

	db.data.Users[user.ID] = user
	db.data.Emails[user.Email] = user.ID
	if err := db.save(); err != nil { return nil, err }
	return user, nil
}

func (db *DB) GetUserByEmail(email string) (*models.User, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	id, ok := db.data.Emails[email]
	if !ok { return nil, nil }
	user := db.data.Users[id]
	if user == nil { return nil, nil }

	// Return a copy
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

func (db *DB) VerifyPassword(user *models.User, password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password))
	return err == nil
}

func (db *DB) SearchUsers(query string, limit int) ([]models.UserSearchResult, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	if limit <= 0 || limit > 50 { limit = 20 }
	pattern := query

	var results []models.UserSearchResult
	for _, u := range db.data.Users {
		if contains(u.ID, pattern) || contains(u.DisplayName, pattern) || contains(u.Email, pattern) {
			results = append(results, models.UserSearchResult{
				ID: u.ID, DisplayName: u.DisplayName, Email: u.Email,
				Bio: u.Bio, Status: u.Status, AvatarURL: u.AvatarURL,
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
	defer db.mu.Unlock()

	u := db.data.Users[id]
	if u == nil { return fmt.Errorf("user not found") }
	u.DisplayName = displayName
	u.Bio = bio
	u.AvatarURL = avatarURL
	return db.save()
}

func (db *DB) UpdateStatus(id, status string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	u := db.data.Users[id]
	if u == nil { return fmt.Errorf("user not found") }
	u.Status = status
	return db.save()
}

func (db *DB) UpdatePublicKey(id, fingerprint string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	u := db.data.Users[id]
	if u == nil { return fmt.Errorf("user not found") }
	u.PublicKeyFingerprint = fingerprint
	return db.save()
}

func (db *DB) SendFriendRequest(fromID, toID string) (*models.FriendRequest, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	if fromID == toID { return nil, fmt.Errorf("cannot send request to yourself") }
	if db.data.Users[fromID] == nil { return nil, fmt.Errorf("sender not found") }
	if db.data.Users[toID] == nil { return nil, fmt.Errorf("recipient not found") }

	fr := &models.FriendRequest{
		ID: generateID("frq"), FromID: fromID, ToID: toID,
		Status: "pending", CreatedAt: time.Now().UTC(),
	}
	if _, exists := db.data.FriendRequests[fromID+":"+toID]; exists {
		return nil, fmt.Errorf("friend request already exists")
	}
	db.data.FriendRequests[fromID+":"+toID] = fr
	return fr, db.save()
}

func (db *DB) AcceptFriendRequest(requestID string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	for _, fr := range db.data.FriendRequests {
		if fr.ID == requestID && fr.Status == "pending" {
			fr.Status = "accepted"
			db.data.Friends[fr.FromID] = append(db.data.Friends[fr.FromID], fr.ToID)
			db.data.Friends[fr.ToID] = append(db.data.Friends[fr.ToID], fr.FromID)
			return db.save()
		}
	}
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

	var friends []models.Friend
	for _, friendID := range db.data.Friends[userID] {
		u := db.data.Users[friendID]
		if u == nil { continue }
		friends = append(friends, models.Friend{
			ID: u.ID, DisplayName: u.DisplayName, Email: u.Email,
			Bio: u.Bio, Status: u.Status, AvatarURL: u.AvatarURL, Online: u.Status == "online",
		})
	}
	return friends, nil
}

func (db *DB) CreateProject(name, ownerID string) (*models.Project, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	p := &models.Project{
		ID: generateID("prj"), Name: name, OwnerID: ownerID, CreatedAt: time.Now().UTC(),
	}
	db.data.Projects[p.ID] = p
	db.data.ProjectMembers[p.ID] = []models.ProjectMember{
		{ProjectID: p.ID, UserID: ownerID, Role: "owner"},
	}
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
	defer db.mu.Unlock()

	members := db.data.ProjectMembers[projectID]
	for _, m := range members {
		if m.UserID == userID { return nil }
	}
	db.data.ProjectMembers[projectID] = append(members, models.ProjectMember{
		ProjectID: projectID, UserID: userID, Role: "member",
	})
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
			}
		}
	}
	return result, nil
}

func (db *DB) StoreDelta(projectID, authorID, data string) (*models.ProjectDelta, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	d := &models.ProjectDelta{
		ID: generateID("dlt"), ProjectID: projectID,
		AuthorID: authorID, Data: data, CreatedAt: time.Now().UTC(),
	}
	db.data.Deltas[projectID] = append(db.data.Deltas[projectID], *d)
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
				d.Author = models.UserSearchResult{
					ID: u.ID, DisplayName: u.DisplayName, Email: u.Email,
					Bio: u.Bio, Status: u.Status, AvatarURL: u.AvatarURL,
				}
			}
			result = append(result, d)
		}
	}
	return result, nil
}
