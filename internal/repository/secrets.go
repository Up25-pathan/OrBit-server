package repository

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// GetOrCreateSecret returns the secret for name, honoring an explicit env
// override first, then the persisted value, otherwise generating and storing a
// fresh one. Postgres mode persists inside the kv table; file mode persists to
// a file next to the database. This keeps JWT/invite secrets stable across
// restarts without the operator managing another env var.
func (db *DB) GetOrCreateSecret(name string, byteLen int) string {
	if v := secretEnvOverride(name); v != "" {
		return v
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	if db.pg != nil {
		return db.pg.getOrCreateSecret(name, byteLen)
	}
	return fileGetOrCreateSecret(db.path, name, byteLen)
}

func secretEnvOverride(name string) string {
	switch name {
	case "jwt":
		return os.Getenv("ORBIT_JWT_SECRET")
	case "invite":
		return os.Getenv("ORBIT_INVITE_SALT")
	}
	return ""
}

func secretFileName(name string) string {
	switch name {
	case "invite":
		return "orbit.invite-salt"
	default:
		return "orbit.jwt-secret"
	}
}

// fileGetOrCreateSecret reads the secret from a file next to the database,
// generating and writing a fresh one when missing.
func fileGetOrCreateSecret(dbPath, name string, byteLen int) string {
	filePath := fileSecretPath(dbPath, name)
	if data, err := os.ReadFile(filePath); err == nil {
		if trimmed := strings.TrimSpace(string(data)); trimmed != "" {
			return trimmed
		}
	}
	secret := randomSecret(byteLen)
	if err := os.WriteFile(filePath, []byte(secret+"\n"), 0600); err != nil {
		log.Printf("[db] WARNING: %s secret generated but could not persist to %s: %v", name, filePath, err)
	} else {
		log.Printf("[db] %s secret persisted to %s. Sessions stay valid across restarts.", name, filePath)
	}
	return secret
}

func fileSecretPath(dbPath, name string) string {
	dir := filepath.Dir(dbPath)
	if dir == "." {
		return secretFileName(name)
	}
	return filepath.Join(dir, secretFileName(name))
}

func randomSecret(byteLen int) string {
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("[db] failed to generate secret: %v", err)
	}
	return hex.EncodeToString(b)
}
