package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgStore persists the in-memory store to Postgres using a single key/value
// JSONB table. Each top-level store field is one row, mirroring the JSON file
// format exactly. This keeps every business operation (sync, relay, friends,
// presence, chat, signals) unchanged — only load/save talk to Postgres.
//
// The kv table is also used to persist the JWT/invite secrets (key "secrets")
// so sessions and invite tokens survive server restarts/redeploys.
type pgStore struct {
	pool *pgxpool.Pool
}

func newPgStore(connString string) (*pgStore, error) {
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	// Prefer IPv4 addresses when the hostname provides them.
	cfg.ConnConfig.LookupFunc = ipv4PreferredLookup
	cfg.MaxConns = 10
	cfg.MinConns = 2
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.MaxConnLifetime = 30 * time.Minute
	
	// If connecting to a transaction pooler (e.g. Supabase port 6543), prepared statements will fail.
	// We force Exec mode to disable prepared statements automatically so the user doesn't have to
	// worry about appending obscure ?default_query_exec_mode=exec flags to their Render config.
	if cfg.ConnConfig.Port == 6543 {
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if _, err := pool.Exec(ctx, normalizedSchemaSQL); err != nil {
		pool.Close()
		return nil, fmt.Errorf("create postgres schema: %w", err)
	}
	pg := &pgStore{pool: pool}
	if err := pg.migrateLegacyKV(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate legacy kv data: %w", err)
	}
	return pg, nil
}

const normalizedSchemaSQL = `
CREATE TABLE IF NOT EXISTS kv (
	key TEXT PRIMARY KEY,
	data JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS secrets (
	name TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
	display_name TEXT NOT NULL,
	email TEXT NOT NULL,
	plan_tier TEXT NOT NULL,
	bio TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'offline',
	activity TEXT NOT NULL DEFAULT '',
	avatar_url TEXT NOT NULL DEFAULT '',
	public_key_fingerprint TEXT NOT NULL DEFAULT '',
	machine_id TEXT NOT NULL DEFAULT '',
	last_seen TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS license_index (
	license_key TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_license_index_user_id ON license_index(user_id);

CREATE TABLE IF NOT EXISTS friend_requests (
	id TEXT PRIMARY KEY,
	from_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	to_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	status TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL,
	UNIQUE(from_id, to_id)
);
CREATE INDEX IF NOT EXISTS idx_friend_requests_to_status ON friend_requests(to_id, status);

CREATE TABLE IF NOT EXISTS friends (
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	friend_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	PRIMARY KEY(user_id, friend_id)
);

CREATE TABLE IF NOT EXISTS projects (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	language TEXT NOT NULL,
	domain TEXT NOT NULL,
	owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_projects_owner_id ON projects(owner_id);

CREATE TABLE IF NOT EXISTS project_members (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	role TEXT NOT NULL,
	path TEXT NOT NULL DEFAULT '',
	PRIMARY KEY(project_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_project_members_user_id ON project_members(user_id);

CREATE TABLE IF NOT EXISTS tasks (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	title TEXT NOT NULL,
	assignee_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	creator_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	status TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL,
	completed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_tasks_project_id ON tasks(project_id);

CREATE TABLE IF NOT EXISTS deltas (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	author_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	data TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_deltas_project_created ON deltas(project_id, created_at);

CREATE TABLE IF NOT EXISTS delta_acks (
	delta_id TEXT NOT NULL REFERENCES deltas(id) ON DELETE CASCADE,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	PRIMARY KEY(delta_id, user_id)
);

CREATE TABLE IF NOT EXISTS activity_logs (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	action TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_activity_user_created ON activity_logs(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_activity_project_created ON activity_logs(project_id, created_at);

CREATE TABLE IF NOT EXISTS messages (
	id TEXT PRIMARY KEY,
	channel_id TEXT NOT NULL,
	author_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	text TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_channel_created ON messages(channel_id, created_at);

CREATE TABLE IF NOT EXISTS signals (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	from_peer TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	to_peer TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	type TEXT NOT NULL,
	payload TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_signals_project_to_created ON signals(project_id, to_peer, created_at);
`

func (p *pgStore) close() {
	p.pool.Close()
}

// load fills the (already initialized) store maps from the kv table. Keys that
// have no row simply leave their map/slice as the zero value initialized by New.
func (p *pgStore) load(s *store) error {
	return nil
}

// save is intentionally a no-op in Postgres mode. Production persistence uses
// table-backed repository methods, so a generic whole-store rewrite would
// reintroduce the scaling problem this store replaces.
func (p *pgStore) save(s *store) error {
	return nil
}

type kvRow struct {
	key string
	val any
}

func kvRows(s *store) []kvRow {
	return []kvRow{
		{"users", s.Users},
		{"licenseIndex", s.LicenseIndex},
		{"friendRequests", s.FriendRequests},
		{"friends", s.Friends},
		{"projects", s.Projects},
		{"projectMembers", s.ProjectMembers},
		{"tasks", s.Tasks},
		{"deltas", s.Deltas},
		{"activityLogs", s.ActivityLogs},
		{"messages", s.Messages},
		{"signals", s.Signals},
	}
}

// ipv4PreferredLookup resolves a host to IP strings, preferring IPv4 addresses
// when DNS provides them and falling back to all results (typically IPv6) when
// it does not. pgconn calls this instead of the default resolver and dials the
// returned literals, so this is the right place to steer address families.
func ipv4PreferredLookup(ctx context.Context, host string) ([]string, error) {
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	var v4 []string
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
			v4 = append(v4, a)
		}
	}
	if len(v4) > 0 {
		return v4, nil
	}
	return addrs, nil
}

func unmarshalKey(key string, data []byte, s *store) error {
	switch key {
	case "users":
		return json.Unmarshal(data, &s.Users)
	case "licenseIndex":
		return json.Unmarshal(data, &s.LicenseIndex)
	case "friendRequests":
		return json.Unmarshal(data, &s.FriendRequests)
	case "friends":
		return json.Unmarshal(data, &s.Friends)
	case "projects":
		return json.Unmarshal(data, &s.Projects)
	case "projectMembers":
		return json.Unmarshal(data, &s.ProjectMembers)
	case "tasks":
		return json.Unmarshal(data, &s.Tasks)
	case "deltas":
		return json.Unmarshal(data, &s.Deltas)
	case "activityLogs":
		return json.Unmarshal(data, &s.ActivityLogs)
	case "messages":
		return json.Unmarshal(data, &s.Messages)
	case "signals":
		return json.Unmarshal(data, &s.Signals)
	default:
		// Unknown rows are ignored so a future schema extension never breaks
		// an older binary on startup.
		return nil
	}
}

// --- Persisted secrets (JWT / invite salt) ---

func (p *pgStore) loadSecrets(ctx context.Context) (map[string]string, error) {
	secrets := map[string]string{}
	rows, err := p.pool.Query(ctx, `SELECT name, value FROM secrets`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		secrets[name] = value
	}
	return secrets, rows.Err()
}

func (p *pgStore) storeSecrets(ctx context.Context, secrets map[string]string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for name, value := range secrets {
		if _, err := tx.Exec(ctx, `INSERT INTO secrets (name, value) VALUES ($1,$2) ON CONFLICT (name) DO UPDATE SET value = EXCLUDED.value`, name, value); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// getOrCreateSecret returns the persisted secret for name, generating and
// storing a fresh one when absent. Postgres is the durable home for secrets in
// this mode, so JWT/invite values survive restarts and redeploys.
func (p *pgStore) getOrCreateSecret(name string, byteLen int) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	secrets, err := p.loadSecrets(ctx)
	if err != nil {
		log.Printf("[db] failed to read persisted secret %q: %v", name, err)
		return randomSecret(byteLen)
	}
	if v, ok := secrets[name]; ok && v != "" {
		return v
	}
	v := randomSecret(byteLen)
	secrets[name] = v
	if err := p.storeSecrets(ctx, secrets); err != nil {
		log.Printf("[db] failed to persist new secret %q: %v", name, err)
	}
	return v
}
