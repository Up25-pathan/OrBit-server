package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/orbit/control-server/internal/models"
)

func pgCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func (p *pgStore) migrateLegacyKV(ctx context.Context) error {
	var existing int
	if err := p.pool.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}

	legacy := &store{
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
	}
	if err := p.loadKV(ctx, legacy); err != nil {
		return err
	}
	if len(legacy.Users) == 0 && len(legacy.Projects) == 0 {
		return nil
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, u := range legacy.Users {
		if u == nil {
			continue
		}
		if u.CreatedAt.IsZero() {
			u.CreatedAt = time.Now().UTC()
		}
		if u.UpdatedAt.IsZero() {
			u.UpdatedAt = u.CreatedAt
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO users (id, display_name, email, plan_tier, bio, status, activity, avatar_url, public_key_fingerprint, machine_id, last_seen, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			ON CONFLICT (id) DO NOTHING`,
			u.ID, u.DisplayName, u.Email, u.PlanTier, u.Bio, u.Status, u.Activity, u.AvatarURL, u.PublicKeyFingerprint, u.MachineID, nullableTime(u.LastSeen), u.CreatedAt, u.UpdatedAt); err != nil {
			return err
		}
	}
	for licenseKey, userID := range legacy.LicenseIndex {
		if _, err := tx.Exec(ctx, `INSERT INTO license_index (license_key, user_id) VALUES ($1,$2) ON CONFLICT (license_key) DO UPDATE SET user_id = EXCLUDED.user_id`, licenseKey, userID); err != nil {
			return err
		}
	}
	for _, fr := range legacy.FriendRequests {
		if fr == nil {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO friend_requests (id, from_id, to_id, status, created_at)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (from_id, to_id) DO UPDATE SET id = EXCLUDED.id, status = EXCLUDED.status, created_at = EXCLUDED.created_at`,
			fr.ID, fr.FromID, fr.ToID, fr.Status, fr.CreatedAt); err != nil {
			return err
		}
	}
	for userID, friendIDs := range legacy.Friends {
		for _, friendID := range friendIDs {
			if _, err := tx.Exec(ctx, `INSERT INTO friends (user_id, friend_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, userID, friendID); err != nil {
				return err
			}
		}
	}
	for _, prj := range legacy.Projects {
		if prj == nil {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO projects (id, name, language, domain, owner_id, created_at)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (id) DO NOTHING`,
			prj.ID, prj.Name, prj.Language, prj.Domain, prj.OwnerID, prj.CreatedAt); err != nil {
			return err
		}
	}
	for projectID, members := range legacy.ProjectMembers {
		for _, m := range members {
			if _, err := tx.Exec(ctx, `
				INSERT INTO project_members (project_id, user_id, role, path)
				VALUES ($1,$2,$3,$4)
				ON CONFLICT (project_id, user_id) DO UPDATE SET role = EXCLUDED.role, path = EXCLUDED.path`,
				projectID, m.UserID, m.Role, m.Path); err != nil {
				return err
			}
		}
	}
	for _, tasks := range legacy.Tasks {
		for _, t := range tasks {
			if t == nil {
				continue
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO tasks (id, project_id, title, assignee_id, creator_id, status, created_at, completed_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
				ON CONFLICT (id) DO NOTHING`,
				t.ID, t.ProjectID, t.Title, t.AssigneeID, t.CreatorID, t.Status, t.CreatedAt, t.CompletedAt); err != nil {
				return err
			}
		}
	}
	for _, deltas := range legacy.Deltas {
		for _, d := range deltas {
			if _, err := tx.Exec(ctx, `
				INSERT INTO deltas (id, project_id, author_id, data, created_at)
				VALUES ($1,$2,$3,$4,$5)
				ON CONFLICT (id) DO NOTHING`,
				d.ID, d.ProjectID, d.AuthorID, d.Data, d.CreatedAt); err != nil {
				return err
			}
			for _, ack := range d.AckedBy {
				if _, err := tx.Exec(ctx, `INSERT INTO delta_acks (delta_id, user_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, d.ID, ack); err != nil {
					return err
				}
			}
		}
	}
	for _, a := range legacy.ActivityLogs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity_logs (id, user_id, project_id, action, created_at)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (id) DO NOTHING`,
			a.ID, a.UserID, a.ProjectID, a.Action, a.CreatedAt); err != nil {
			return err
		}
	}
	for channelID, messages := range legacy.Messages {
		for _, m := range messages {
			if m == nil {
				continue
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO messages (id, channel_id, author_id, text, created_at)
				VALUES ($1,$2,$3,$4,$5)
				ON CONFLICT (id) DO NOTHING`,
				m.ID, channelID, m.AuthorID, m.Text, m.CreatedAt); err != nil {
				return err
			}
		}
	}
	for _, s := range legacy.Signals {
		if _, err := tx.Exec(ctx, `
			INSERT INTO signals (id, project_id, from_peer, to_peer, type, payload, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (id) DO NOTHING`,
			s.ID, s.ProjectID, s.FromPeer, s.ToPeer, s.Type, s.Payload, s.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func (p *pgStore) loadKV(ctx context.Context, s *store) error {
	rows, err := p.pool.Query(ctx, `SELECT key, data FROM kv`)
	if err != nil {
		return fmt.Errorf("query kv: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var key string
		var data []byte
		if err := rows.Scan(&key, &data); err != nil {
			return err
		}
		if err := unmarshalKey(key, data, s); err != nil {
			return fmt.Errorf("kv key %q: %w", key, err)
		}
	}
	return rows.Err()
}

func (p *pgStore) upsertUser(id, name, email, avatarURL, planTier, licenseKey, machineID string) (*models.User, error) {
	ctx, cancel := pgCtx()
	defer cancel()

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var existingMachineID string
	err = tx.QueryRow(ctx, `SELECT machine_id FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&existingMachineID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil && existingMachineID != "" && existingMachineID != machineID {
		return nil, fmt.Errorf("license is already bound to another device")
	}

	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `
		INSERT INTO users (id, display_name, email, plan_tier, bio, status, activity, avatar_url, public_key_fingerprint, machine_id, created_at, updated_at)
		VALUES ($1,$2,$3,$4,'','online','',$5,'',$6,$7,$7)
		ON CONFLICT (id) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			email = EXCLUDED.email,
			plan_tier = EXCLUDED.plan_tier,
			avatar_url = CASE WHEN EXCLUDED.avatar_url <> '' THEN EXCLUDED.avatar_url ELSE users.avatar_url END,
			machine_id = CASE WHEN users.machine_id = '' THEN EXCLUDED.machine_id ELSE users.machine_id END,
			updated_at = EXCLUDED.updated_at`,
		id, name, email, planTier, avatarURL, machineID, now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO license_index (license_key, user_id) VALUES ($1,$2) ON CONFLICT (license_key) DO UPDATE SET user_id = EXCLUDED.user_id`, licenseKey, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return p.getUserByID(id)
}

func (p *pgStore) getUserByID(id string) (*models.User, error) {
	ctx, cancel := pgCtx()
	defer cancel()

	var u models.User
	var lastSeen *time.Time
	err := p.pool.QueryRow(ctx, `
		SELECT id, display_name, email, plan_tier, bio, status, activity, avatar_url, public_key_fingerprint, machine_id, last_seen, created_at, updated_at
		FROM users WHERE id = $1`, id).Scan(
		&u.ID, &u.DisplayName, &u.Email, &u.PlanTier, &u.Bio, &u.Status, &u.Activity, &u.AvatarURL, &u.PublicKeyFingerprint, &u.MachineID, &lastSeen, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if lastSeen != nil {
		u.LastSeen = *lastSeen
	}
	return &u, nil
}

func (p *pgStore) getUserByLicenseKey(key string) (*models.User, error) {
	ctx, cancel := pgCtx()
	defer cancel()

	var id string
	err := p.pool.QueryRow(ctx, `SELECT user_id FROM license_index WHERE license_key = $1`, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p.getUserByID(id)
}

func (p *pgStore) getLicenseKeyByUserID(userID string) string {
	ctx, cancel := pgCtx()
	defer cancel()

	var key string
	if err := p.pool.QueryRow(ctx, `SELECT license_key FROM license_index WHERE user_id = $1 LIMIT 1`, userID).Scan(&key); err != nil {
		return ""
	}
	return key
}

func (p *pgStore) searchUsers(query string, limit int) ([]models.UserSearchResult, error) {
	ctx, cancel := pgCtx()
	defer cancel()

	if limit <= 0 || limit > 50 {
		limit = 20
	}
	pattern := "%" + strings.ToLower(query) + "%"
	rows, err := p.pool.Query(ctx, `
		SELECT id, display_name, email, bio, status, activity, avatar_url, public_key_fingerprint
		FROM users
		WHERE $1 = '%%' OR lower(id) LIKE $1 OR lower(display_name) LIKE $1 OR lower(email) LIKE $1
		ORDER BY display_name ASC
		LIMIT $2`, pattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []models.UserSearchResult
	for rows.Next() {
		var u models.UserSearchResult
		if err := rows.Scan(&u.ID, &u.DisplayName, &u.Email, &u.Bio, &u.Status, &u.Activity, &u.AvatarURL, &u.PublicKeyFingerprint); err != nil {
			return nil, err
		}
		result = append(result, u)
	}
	return result, rows.Err()
}

func (p *pgStore) updateProfile(id, displayName, bio, avatarURL string) error {
	ctx, cancel := pgCtx()
	defer cancel()
	ct, err := p.pool.Exec(ctx, `UPDATE users SET display_name = $2, bio = $3, avatar_url = $4, updated_at = $5 WHERE id = $1`, id, displayName, bio, avatarURL, time.Now().UTC())
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

func (p *pgStore) updatePublicKey(id, fingerprint string) error {
	ctx, cancel := pgCtx()
	defer cancel()
	ct, err := p.pool.Exec(ctx, `UPDATE users SET public_key_fingerprint = $2, updated_at = $3 WHERE id = $1`, id, fingerprint, time.Now().UTC())
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

func (p *pgStore) updatePresence(userID, activity string) error {
	ctx, cancel := pgCtx()
	defer cancel()
	status := "online"
	if strings.EqualFold(activity, "offline") || strings.EqualFold(activity, "quitting") {
		status = "offline"
	}
	ct, err := p.pool.Exec(ctx, `UPDATE users SET activity = $2, status = $3, last_seen = $4, updated_at = $4 WHERE id = $1`, userID, activity, status, time.Now().UTC())
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

func (p *pgStore) heartbeatSweep() {
	ctx, cancel := pgCtx()
	defer cancel()
	_, _ = p.pool.Exec(ctx, `
		UPDATE users
		SET status = 'offline', updated_at = NOW()
		WHERE status <> 'offline' AND (last_seen IS NULL OR last_seen < NOW() - INTERVAL '25 seconds')`)
}

func (p *pgStore) sendFriendRequest(fromID, toID string) (*models.FriendRequest, error) {
	if fromID == toID {
		return nil, fmt.Errorf("cannot send request to yourself")
	}
	ctx, cancel := pgCtx()
	defer cancel()

	var found int
	if err := p.pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE id IN ($1,$2)`, fromID, toID).Scan(&found); err != nil {
		return nil, err
	}
	if found < 2 {
		return nil, fmt.Errorf("recipient not found")
	}
	fr := &models.FriendRequest{ID: generateID("frq"), FromID: fromID, ToID: toID, Status: "pending", CreatedAt: time.Now().UTC()}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO friend_requests (id, from_id, to_id, status, created_at)
		VALUES ($1,$2,$3,'pending',$4)
		ON CONFLICT (from_id, to_id) DO UPDATE SET id = EXCLUDED.id, status = EXCLUDED.status, created_at = EXCLUDED.created_at
		WHERE friend_requests.status <> 'pending'`, fr.ID, fromID, toID, fr.CreatedAt)
	if err != nil {
		return nil, err
	}
	return fr, nil
}

func (p *pgStore) acceptFriendRequest(requestID, userID string) error {
	ctx, cancel := pgCtx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var fromID, toID string
	err = tx.QueryRow(ctx, `UPDATE friend_requests SET status = 'accepted' WHERE id = $1 AND status = 'pending' RETURNING from_id, to_id`, requestID).Scan(&fromID, &toID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("pending request not found")
	}
	if err != nil {
		return err
	}
	if toID != userID {
		return fmt.Errorf("not authorized to accept this request")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO friends (user_id, friend_id) VALUES ($1,$2),($2,$1) ON CONFLICT DO NOTHING`, fromID, toID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *pgStore) rejectFriendRequest(requestID, userID string) error {
	ctx, cancel := pgCtx()
	defer cancel()
	ct, err := p.pool.Exec(ctx, `UPDATE friend_requests SET status = 'rejected' WHERE id = $1 AND to_id = $2 AND status = 'pending'`, requestID, userID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("pending request not found")
	}
	return nil
}

func (p *pgStore) getPendingRequests(userID string) ([]models.FriendRequest, error) {
	ctx, cancel := pgCtx()
	defer cancel()
	rows, err := p.pool.Query(ctx, `
		SELECT fr.id, fr.from_id, fr.to_id, fr.status, fr.created_at,
		       u.id, u.display_name, u.email, u.bio, u.status, u.activity, u.avatar_url, u.public_key_fingerprint
		FROM friend_requests fr
		JOIN users u ON u.id = fr.from_id
		WHERE fr.to_id = $1 AND fr.status = 'pending'
		ORDER BY fr.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.FriendRequest
	for rows.Next() {
		var fr models.FriendRequest
		if err := rows.Scan(&fr.ID, &fr.FromID, &fr.ToID, &fr.Status, &fr.CreatedAt, &fr.From.ID, &fr.From.DisplayName, &fr.From.Email, &fr.From.Bio, &fr.From.Status, &fr.From.Activity, &fr.From.AvatarURL, &fr.From.PublicKeyFingerprint); err != nil {
			return nil, err
		}
		result = append(result, fr)
	}
	return result, rows.Err()
}

func (p *pgStore) getFriends(userID string) ([]models.Friend, error) {
	ctx, cancel := pgCtx()
	defer cancel()
	rows, err := p.pool.Query(ctx, `
		SELECT u.id, u.display_name, u.email, u.bio, u.status, u.activity, u.avatar_url, u.public_key_fingerprint
		FROM friends f
		JOIN users u ON u.id = f.friend_id
		WHERE f.user_id = $1
		ORDER BY u.display_name ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.Friend
	for rows.Next() {
		var f models.Friend
		if err := rows.Scan(&f.ID, &f.DisplayName, &f.Email, &f.Bio, &f.Status, &f.Activity, &f.AvatarURL, &f.PublicKeyFingerprint); err != nil {
			return nil, err
		}
		f.Online = f.Status == "online"
		result = append(result, f)
	}
	return result, rows.Err()
}

func (p *pgStore) createProject(name, language, domain, ownerID string) (*models.Project, error) {
	ctx, cancel := pgCtx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	prj := &models.Project{ID: generateID("prj"), Name: name, Language: language, Domain: domain, OwnerID: ownerID, CreatedAt: time.Now().UTC()}
	if _, err := tx.Exec(ctx, `INSERT INTO projects (id, name, language, domain, owner_id, created_at) VALUES ($1,$2,$3,$4,$5,$6)`, prj.ID, prj.Name, prj.Language, prj.Domain, prj.OwnerID, prj.CreatedAt); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role, path) VALUES ($1,$2,'owner','')`, prj.ID, ownerID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return prj, nil
}

func (p *pgStore) getProject(id string) (*models.Project, error) {
	ctx, cancel := pgCtx()
	defer cancel()
	var prj models.Project
	err := p.pool.QueryRow(ctx, `SELECT id, name, language, domain, owner_id, created_at FROM projects WHERE id = $1`, id).Scan(&prj.ID, &prj.Name, &prj.Language, &prj.Domain, &prj.OwnerID, &prj.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &prj, nil
}

func (p *pgStore) listProjectsForUser(userID string) ([]models.Project, error) {
	ctx, cancel := pgCtx()
	defer cancel()
	rows, err := p.pool.Query(ctx, `
		SELECT p.id, p.name, p.language, p.domain, p.owner_id, p.created_at
		FROM projects p
		JOIN project_members pm ON pm.project_id = p.id
		WHERE pm.user_id = $1
		ORDER BY p.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.Project
	for rows.Next() {
		var prj models.Project
		if err := rows.Scan(&prj.ID, &prj.Name, &prj.Language, &prj.Domain, &prj.OwnerID, &prj.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, prj)
	}
	return result, rows.Err()
}

func (p *pgStore) inviteMember(projectID, userID string) error {
	ctx, cancel := pgCtx()
	defer cancel()
	var exists bool
	if err := p.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, userID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("user not found")
	}
	_, err := p.pool.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role, path) VALUES ($1,$2,'member','') ON CONFLICT DO NOTHING`, projectID, userID)
	return err
}

func (p *pgStore) inviteMemberWithLimit(projectID, userID string, maxMembers int) error {
	ctx, cancel := pgCtx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, userID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("user not found")
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM project_members WHERE project_id = $1 FOR UPDATE`, projectID).Scan(&count); err != nil {
		return err
	}
	if count >= maxMembers {
		return fmt.Errorf("member limit reached")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role, path) VALUES ($1,$2,'member','') ON CONFLICT DO NOTHING`, projectID, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *pgStore) getProjectMembers(projectID string) ([]models.ProjectMember, error) {
	ctx, cancel := pgCtx()
	defer cancel()
	rows, err := p.pool.Query(ctx, `
		SELECT pm.project_id, pm.user_id, pm.role, pm.path,
		       u.id, u.display_name, u.email, u.bio, u.status, u.activity, u.avatar_url, u.public_key_fingerprint
		FROM project_members pm
		JOIN users u ON u.id = pm.user_id
		WHERE pm.project_id = $1
		ORDER BY CASE WHEN pm.role = 'owner' THEN 0 ELSE 1 END, u.display_name ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.ProjectMember
	for rows.Next() {
		var m models.ProjectMember
		if err := rows.Scan(&m.ProjectID, &m.UserID, &m.Role, &m.Path, &m.User.ID, &m.User.DisplayName, &m.User.Email, &m.User.Bio, &m.User.Status, &m.User.Activity, &m.User.AvatarURL, &m.User.PublicKeyFingerprint); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

func (p *pgStore) updateProject(prj *models.Project) error {
	ctx, cancel := pgCtx()
	defer cancel()
	ct, err := p.pool.Exec(ctx, `UPDATE projects SET name = $2 WHERE id = $1`, prj.ID, prj.Name)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("project not found")
	}
	return nil
}

func (p *pgStore) storeDelta(projectID, authorID, data string) (*models.ProjectDelta, error) {
	ctx, cancel := pgCtx()
	defer cancel()
	d := &models.ProjectDelta{ID: generateID("dlt"), ProjectID: projectID, AuthorID: authorID, Data: data, CreatedAt: time.Now().UTC()}
	_, err := p.pool.Exec(ctx, `INSERT INTO deltas (id, project_id, author_id, data, created_at) VALUES ($1,$2,$3,$4,$5)`, d.ID, d.ProjectID, d.AuthorID, d.Data, d.CreatedAt)
	return d, err
}

func (p *pgStore) ackDelta(projectID, deltaID, userID string) error {
	ctx, cancel := pgCtx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var authorID string
	err = tx.QueryRow(ctx, `SELECT author_id FROM deltas WHERE id = $1 AND project_id = $2 FOR UPDATE`, deltaID, projectID).Scan(&authorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if authorID != userID {
		if _, err := tx.Exec(ctx, `INSERT INTO delta_acks (delta_id, user_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, deltaID, userID); err != nil {
			return err
		}
	}
	var needed, acked int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM project_members WHERE project_id = $1 AND user_id <> $2`, projectID, authorID).Scan(&needed); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM delta_acks WHERE delta_id = $1`, deltaID).Scan(&acked); err != nil {
		return err
	}
	if acked >= needed {
		if _, err := tx.Exec(ctx, `DELETE FROM deltas WHERE id = $1`, deltaID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (p *pgStore) getDeltas(projectID string, since time.Time) ([]models.ProjectDelta, error) {
	ctx, cancel := pgCtx()
	defer cancel()
	rows, err := p.pool.Query(ctx, `
		SELECT d.id, d.project_id, d.author_id, d.data, d.created_at,
		       u.id, u.display_name, u.status,
		       COALESCE(array_agg(da.user_id) FILTER (WHERE da.user_id IS NOT NULL), ARRAY[]::text[])
		FROM deltas d
		JOIN users u ON u.id = d.author_id
		LEFT JOIN delta_acks da ON da.delta_id = d.id
		WHERE d.project_id = $1 AND d.created_at > $2
		GROUP BY d.id, u.id, u.display_name, u.status
		ORDER BY d.created_at ASC`, projectID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.ProjectDelta
	for rows.Next() {
		var d models.ProjectDelta
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.AuthorID, &d.Data, &d.CreatedAt, &d.Author.ID, &d.Author.Name, &d.Author.Status, &d.AckedBy); err != nil {
			return nil, err
		}
		d.Author.DisplayName = d.Author.Name
		result = append(result, d)
	}
	return result, rows.Err()
}

func (p *pgStore) sweepExpiredDeltas(ttl time.Duration) int {
	ctx, cancel := pgCtx()
	defer cancel()
	ct, err := p.pool.Exec(ctx, `DELETE FROM deltas WHERE created_at < $1`, time.Now().UTC().Add(-ttl))
	if err != nil {
		return 0
	}
	return int(ct.RowsAffected())
}

func (p *pgStore) createTask(projectID, title, assigneeID, creatorID, priority, tag string) (*models.Task, error) {
	ctx, cancel := pgCtx()
	defer cancel()
	t := &models.Task{ID: generateID("tsk"), ProjectID: projectID, Title: title, AssigneeID: assigneeID, CreatorID: creatorID, Status: "open", Stage: "backlog", Priority: priority, Tag: tag, CreatedAt: time.Now().UTC()}
	_, err := p.pool.Exec(ctx, `INSERT INTO tasks (id, project_id, title, assignee_id, creator_id, status, stage, priority, tag, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, t.ID, t.ProjectID, t.Title, t.AssigneeID, t.CreatorID, t.Status, t.Stage, t.Priority, t.Tag, t.CreatedAt)
	return t, err
}

func (p *pgStore) getTasks(projectID string) ([]models.Task, error) {
	ctx, cancel := pgCtx()
	defer cancel()
	rows, err := p.pool.Query(ctx, `
		SELECT t.id, t.project_id, t.title, t.assignee_id, t.creator_id, t.status, t.stage, t.priority, t.tag, t.created_at, t.completed_at,
		       u.id, u.display_name, u.email, u.avatar_url
		FROM tasks t
		LEFT JOIN users u ON u.id = t.assignee_id
		WHERE t.project_id = $1
		ORDER BY t.created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.Task
	for rows.Next() {
		var t models.Task
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.AssigneeID, &t.CreatorID, &t.Status, &t.Stage, &t.Priority, &t.Tag, &t.CreatedAt, &t.CompletedAt, &t.Assignee.ID, &t.Assignee.DisplayName, &t.Assignee.Email, &t.Assignee.AvatarURL); err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

func (p *pgStore) completeTask(projectID, taskID string) (*models.Task, error) {
	ctx, cancel := pgCtx()
	defer cancel()
	now := time.Now().UTC()
	var t models.Task
	err := p.pool.QueryRow(ctx, `
		UPDATE tasks SET status = 'completed', stage = 'completed', completed_at = $3
		WHERE project_id = $1 AND id = $2
		RETURNING id, project_id, title, assignee_id, creator_id, status, stage, priority, tag, created_at, completed_at`,
		projectID, taskID, now).Scan(&t.ID, &t.ProjectID, &t.Title, &t.AssigneeID, &t.CreatorID, &t.Status, &t.Stage, &t.Priority, &t.Tag, &t.CreatedAt, &t.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("task not found")
	}
	return &t, err
}

func (p *pgStore) updateTask(projectID, taskID string, req models.UpdateTaskRequest) (*models.Task, error) {
	ctx, cancel := pgCtx()
	defer cancel()
	var sets []string
	var args []interface{}
	argIdx := 3
	if req.Stage != "" {
		sets = append(sets, fmt.Sprintf("stage = $%d", argIdx))
		args = append(args, req.Stage)
		argIdx++
		if req.Stage == "completed" {
			sets = append(sets, "status = 'completed'")
			now := time.Now().UTC()
			sets = append(sets, fmt.Sprintf("completed_at = $%d", argIdx))
			args = append(args, now)
			argIdx++
		} else if req.Stage == "backlog" || req.Stage == "in_progress" || req.Stage == "review" {
			sets = append(sets, "status = 'open'")
			sets = append(sets, fmt.Sprintf("completed_at = NULL"))
		}
	}
	if req.Priority != "" {
		sets = append(sets, fmt.Sprintf("priority = $%d", argIdx))
		args = append(args, req.Priority)
		argIdx++
	}
	if req.Tag != "" {
		sets = append(sets, fmt.Sprintf("tag = $%d", argIdx))
		args = append(args, req.Tag)
		argIdx++
	}
	if len(sets) == 0 {
		return nil, fmt.Errorf("no fields to update")
	}
	query := fmt.Sprintf("UPDATE tasks SET %s WHERE project_id = $1 AND id = $2 RETURNING id, project_id, title, assignee_id, creator_id, status, stage, priority, tag, created_at, completed_at", strings.Join(sets, ", "))
	args = append([]interface{}{projectID, taskID}, args...)
	var t models.Task
	err := p.pool.QueryRow(ctx, query, args...).Scan(&t.ID, &t.ProjectID, &t.Title, &t.AssigneeID, &t.CreatorID, &t.Status, &t.Stage, &t.Priority, &t.Tag, &t.CreatedAt, &t.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("task not found")
	}
	return &t, err
}

func (p *pgStore) logActivity(userID, projectID, action string) error {
	ctx, cancel := pgCtx()
	defer cancel()
	_, err := p.pool.Exec(ctx, `INSERT INTO activity_logs (id, user_id, project_id, action, created_at) VALUES ($1,$2,$3,$4,$5)`, generateID("act"), userID, projectID, action, time.Now().UTC())
	return err
}

func (p *pgStore) getPulse(userID string) ([]models.PulseEntry, error) {
	ctx, cancel := pgCtx()
	defer cancel()
	rows, err := p.pool.Query(ctx, `
		SELECT to_char(created_at::date, 'YYYY-MM-DD') AS day,
		       COUNT(*) FILTER (WHERE action = 'task_completed') AS tasks_completed,
		       COUNT(*) FILTER (WHERE action = 'delta_pushed') AS deltas_pushed
		FROM activity_logs
		WHERE user_id = $1 AND created_at > NOW() - INTERVAL '30 days'
		GROUP BY day
		ORDER BY day ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.PulseEntry
	for rows.Next() {
		var e models.PulseEntry
		if err := rows.Scan(&e.Date, &e.TasksCompleted, &e.DeltasPushed); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func (p *pgStore) getLeaderboard(projectID string) ([]models.LeaderboardEntry, error) {
	ctx, cancel := pgCtx()
	defer cancel()
	rows, err := p.pool.Query(ctx, `
		SELECT u.id, u.display_name, u.email, u.avatar_url,
		       COUNT(*) FILTER (WHERE a.action = 'task_completed') AS tasks_completed,
		       COUNT(*) FILTER (WHERE a.action = 'delta_pushed') AS deltas_pushed
		FROM activity_logs a
		JOIN users u ON u.id = a.user_id
		WHERE a.project_id = $1 AND a.created_at > NOW() - INTERVAL '7 days'
		GROUP BY u.id, u.display_name, u.email, u.avatar_url
		ORDER BY (COUNT(*) FILTER (WHERE a.action = 'task_completed') * 10 + COUNT(*) FILTER (WHERE a.action = 'delta_pushed') * 5) DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.LeaderboardEntry
	for rows.Next() {
		var e models.LeaderboardEntry
		if err := rows.Scan(&e.User.ID, &e.User.DisplayName, &e.User.Email, &e.User.AvatarURL, &e.TasksCompleted, &e.DeltasPushed); err != nil {
			return nil, err
		}
		e.TotalScore = e.TasksCompleted*10 + e.DeltasPushed*5
		result = append(result, e)
	}
	return result, rows.Err()
}

func (p *pgStore) isProjectMember(projectID, userID string) bool {
	ctx, cancel := pgCtx()
	defer cancel()
	var ok bool
	_ = p.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members WHERE project_id = $1 AND user_id = $2)`, projectID, userID).Scan(&ok)
	return ok
}

func (p *pgStore) isProjectOwner(projectID, userID string) bool {
	ctx, cancel := pgCtx()
	defer cancel()
	var ok bool
	_ = p.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members WHERE project_id = $1 AND user_id = $2 AND role = 'owner')`, projectID, userID).Scan(&ok)
	return ok
}

func (p *pgStore) updateMemberPath(projectID, userID, path string) error {
	ctx, cancel := pgCtx()
	defer cancel()
	ct, err := p.pool.Exec(ctx, `UPDATE project_members SET path = $3 WHERE project_id = $1 AND user_id = $2`, projectID, userID, path)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("member not found in project")
	}
	return nil
}

func (p *pgStore) saveMessage(channelID, authorID, text string) (*models.ChatMessage, error) {
	ctx, cancel := pgCtx()
	defer cancel()
	m := &models.ChatMessage{ID: generateID("msg"), ProjectID: channelID, AuthorID: authorID, Text: text, CreatedAt: time.Now().UTC()}
	if _, err := p.pool.Exec(ctx, `INSERT INTO messages (id, channel_id, author_id, text, created_at) VALUES ($1,$2,$3,$4,$5)`, m.ID, channelID, authorID, text, m.CreatedAt); err != nil {
		return nil, err
	}
	if u, err := p.getUserByID(authorID); err == nil && u != nil {
		m.Author = models.UserSearchResult{ID: u.ID, DisplayName: u.DisplayName, AvatarURL: u.AvatarURL}
	}
	return m, nil
}

func (p *pgStore) getMessages(channelID string, offset, limit int) ([]models.ChatMessage, error) {
	ctx, cancel := pgCtx()
	defer cancel()
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := p.pool.Query(ctx, `
		SELECT m.id, m.channel_id, m.author_id, m.text, m.created_at,
		       u.id, u.display_name, u.avatar_url
		FROM messages m
		LEFT JOIN users u ON u.id = m.author_id
		WHERE m.channel_id = $1
		ORDER BY m.created_at ASC
		OFFSET $2 LIMIT $3`, channelID, offset, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.ChatMessage
	for rows.Next() {
		var m models.ChatMessage
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.AuthorID, &m.Text, &m.CreatedAt, &m.Author.ID, &m.Author.DisplayName, &m.Author.AvatarURL); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

func (p *pgStore) deleteTask(projectID, taskID string) error {
	ctx, cancel := pgCtx()
	defer cancel()
	_, err := p.pool.Exec(ctx, `DELETE FROM tasks WHERE project_id = $1 AND id = $2`, projectID, taskID)
	return err
}

func (p *pgStore) deleteProject(projectID string) error {
	ctx, cancel := pgCtx()
	defer cancel()
	_, err := p.pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)
	return err
}

func (p *pgStore) messageSweep() int {
	ctx, cancel := pgCtx()
	defer cancel()
	ct, err := p.pool.Exec(ctx, `DELETE FROM messages WHERE created_at < $1`, time.Now().UTC().Add(-30*24*time.Hour))
	if err != nil {
		return 0
	}
	return int(ct.RowsAffected())
}

func (p *pgStore) activityLogSweep() int {
	ctx, cancel := pgCtx()
	defer cancel()
	ct, err := p.pool.Exec(ctx, `DELETE FROM activity_logs WHERE created_at < $1`, time.Now().UTC().Add(-90*24*time.Hour))
	if err != nil {
		return 0
	}
	return int(ct.RowsAffected())
}

func (p *pgStore) saveSignal(projectID, fromPeer, toPeer, signalType, payload string) error {
	ctx, cancel := pgCtx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM signals WHERE created_at < $1`, time.Now().UTC().Add(-30*time.Minute)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO signals (id, project_id, from_peer, to_peer, type, payload, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`, generateID("sig"), projectID, fromPeer, toPeer, signalType, payload, time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *pgStore) getPendingSignalsForPeer(projectID, toPeer string) ([]Signal, error) {
	ctx, cancel := pgCtx()
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `SELECT id, project_id, from_peer, to_peer, type, payload, created_at FROM signals WHERE project_id = $1 AND to_peer = $2 ORDER BY created_at ASC`, projectID, toPeer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Signal
	for rows.Next() {
		var s Signal
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.FromPeer, &s.ToPeer, &s.Type, &s.Payload, &s.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, s)
	}

	if len(result) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM signals WHERE project_id = $1 AND to_peer = $2`, projectID, toPeer); err != nil {
			return nil, err
		}
	}

	return result, tx.Commit(ctx)
}

func (p *pgStore) clearSignalsForPeer(projectID, toPeer string) error {
	ctx, cancel := pgCtx()
	defer cancel()
	_, err := p.pool.Exec(ctx, `DELETE FROM signals WHERE project_id = $1 AND to_peer = $2`, projectID, toPeer)
	return err
}

func (p *pgStore) sweepExpiredSignals(ttl time.Duration) int {
	ctx, cancel := pgCtx()
	defer cancel()
	ct, err := p.pool.Exec(ctx, `DELETE FROM signals WHERE created_at < $1`, time.Now().UTC().Add(-ttl))
	if err != nil {
		return 0
	}
	return int(ct.RowsAffected())
}

func (p *pgStore) telemetryStats() TelemetryStats {
	ctx, cancel := pgCtx()
	defer cancel()
	var stats TelemetryStats
	_ = p.pool.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&stats.UsersCount)
	_ = p.pool.QueryRow(ctx, `SELECT COUNT(*) FROM projects`).Scan(&stats.ProjectsCount)
	_ = p.pool.QueryRow(ctx, `SELECT COUNT(*) FROM messages`).Scan(&stats.MessagesCount)
	_ = p.pool.QueryRow(ctx, `SELECT COUNT(*) FROM license_index`).Scan(&stats.LicensesCount)
	_ = p.pool.QueryRow(ctx, `SELECT COUNT(*), COALESCE(SUM(length(data)), 0) FROM deltas`).Scan(&stats.DeltaBlobsCount, &stats.DeltaSizeBytes)
	_ = p.pool.QueryRow(ctx, `SELECT COUNT(*) FROM signals`).Scan(&stats.WebRTCSignalsCount)
	return stats
}
