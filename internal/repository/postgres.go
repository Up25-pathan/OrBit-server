package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
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
	connString = strings.TrimSpace(connString)
	if !strings.Contains(connString, "sslmode=") {
		if strings.Contains(connString, "?") {
			connString += "&sslmode=require"
		} else {
			connString += "?sslmode=require"
		}
	}
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	// Prefer IPv4 addresses when the hostname provides them.
	cfg.ConnConfig.LookupFunc = ipv4PreferredLookup
	
	// Always use Exec (simple protocol) mode for Supabase PgBouncer pooler compatibility
	// (PgBouncer does not support prepared statements / extended query protocol).
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec

	// Pool tuning for Supabase Transaction Pooler (prevents dead socket timeouts)
	cfg.MinConns = 0
	cfg.MaxConns = 10
	cfg.MaxConnIdleTime = 15 * time.Second
	cfg.MaxConnLifetime = 5 * time.Minute

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, data JSONB NOT NULL)`); err != nil {
		pool.Close()
		return nil, fmt.Errorf("create kv table: %w", err)
	}
	return &pgStore{pool: pool}, nil
}

func (p *pgStore) close() {
	p.pool.Close()
}

// load fills the (already initialized) store maps from the kv table. Keys that
// have no row simply leave their map/slice as the zero value initialized by New.
func (p *pgStore) load(s *store) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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

// save upserts the whole store in a single transaction so a crash can never
// leave the tables half-updated. All keys are written unconditionally, keeping
// the semantics identical to the old "rewrite the whole JSON file" save.
func (p *pgStore) save(s *store) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, r := range kvRows(s) {
		data, err := json.Marshal(r.val)
		if err != nil {
			return fmt.Errorf("marshal %s: %w", r.key, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO kv (key, data) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET data = EXCLUDED.data`,
			r.key, string(data)); err != nil {
			return fmt.Errorf("upsert %s: %w", r.key, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
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

const secretsKey = "secrets"

func (p *pgStore) loadSecrets(ctx context.Context) (map[string]string, error) {
	secrets := map[string]string{}
	var data []byte
	err := p.pool.QueryRow(ctx, `SELECT data FROM kv WHERE key = $1`, secretsKey).Scan(&data)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return secrets, nil
		}
		return nil, err
	}
	if len(data) > 0 {
		if jerr := json.Unmarshal(data, &secrets); jerr != nil {
			return nil, jerr
		}
	}
	return secrets, nil
}

func (p *pgStore) storeSecrets(ctx context.Context, secrets map[string]string) error {
	data, err := json.Marshal(secrets)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(ctx,
		`INSERT INTO kv (key, data) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET data = EXCLUDED.data`,
		secretsKey, string(data))
	return err
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
