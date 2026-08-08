package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	Port           string
	DatabasePath   string
	JWTSecret      string
	JWTExpiry      time.Duration
	WebsiteURL     string
	ServerSecret   string
	EnableMockKeys bool
	InviteSalt     string
	Backup         BackupConfig
}

// BackupConfig configures the optional durable backup of the database and
// persisted secrets to a Cloudflare R2 bucket (or any S3-compatible store).
// When enabled, every save uploads the state files and a startup restores any
// file that is missing locally — so friendships, project memberships and
// relayed deltas survive Render's ephemeral filesystem wipes on restart/redeploy.
type BackupConfig struct {
	Enabled   bool
	Endpoint  string // e.g. https://<account>.r2.cloudflarestorage.com
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string // R2 uses "auto"
}

func Load() *Config {
	c := LoadEnv()

	c.JWTSecret = loadOrCreateSecret("ORBIT_JWT_SECRET", secretFilePath(c.DatabasePath, "orbit.jwt-secret"), 32, "JWT")
	c.InviteSalt = loadOrCreateSecret("ORBIT_INVITE_SALT", secretFilePath(c.DatabasePath, "orbit.invite-salt"), 16, "invite")

	return c
}

// LoadEnv reads everything except the JWT/invite secrets. Keeping this separate
// from Load() lets main.go restore the persisted secret files from backup BEFORE
// Load() reads (or regenerates) them.
func LoadEnv() *Config {
	port := os.Getenv("PORT")
	if port == "" {
		port = os.Getenv("ORBIT_PORT")
	}
	if port == "" {
		port = "9090"
	}

	dbPath := os.Getenv("ORBIT_DB_PATH")
	if dbPath == "" {
		dbPath = "orbit.db"
	}

	websiteURL := os.Getenv("WEBSITE_SERVER_URL")
	if websiteURL == "" {
		websiteURL = os.Getenv("ORBIT_WEBSITE_URL")
	}
	if websiteURL == "" {
		websiteURL = "https://orbit-sync.onrender.com"
	}

	serverSecret := os.Getenv("CONTROL_SERVER_SECRET")
	if serverSecret == "" {
		serverSecret = "orbit-control-server-verification-secret-2026"
		log.Printf("[Config] WARNING: CONTROL_SERVER_SECRET env not set. Using default secret.")
	}

	c := &Config{
		Port:         port,
		DatabasePath: dbPath,
		JWTExpiry:    72 * time.Hour,
		WebsiteURL:   websiteURL,
		ServerSecret: serverSecret,
		Backup: BackupConfig{
			Endpoint:  os.Getenv("ORBIT_BACKUP_ENDPOINT"),
			Bucket:    os.Getenv("ORBIT_BACKUP_BUCKET"),
			AccessKey: os.Getenv("ORBIT_BACKUP_ACCESS_KEY"),
			SecretKey: os.Getenv("ORBIT_BACKUP_SECRET_KEY"),
			Region:    firstNonEmpty(os.Getenv("ORBIT_BACKUP_REGION"), "auto"),
		},
	}
	c.Backup.Enabled = c.Backup.Endpoint != "" && c.Backup.Bucket != "" &&
		c.Backup.AccessKey != "" && c.Backup.SecretKey != ""
	if c.Backup.Enabled {
		log.Printf("[Backup] Durable backup ENABLED: bucket=%s endpoint=%s", c.Backup.Bucket, c.Backup.Endpoint)
	}

	return c
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// secretFilePath returns a path for a persisted secret file next to the
// database, so the secret survives process restarts (e.g. Render cold starts).
func secretFilePath(dbPath, fileName string) string {
	dir := filepath.Dir(dbPath)
	if dir == "." {
		return fileName
	}
	return filepath.Join(dir, fileName)
}

// loadOrCreateSecret returns the secret from the environment when set, or from
// a persisted file when present, otherwise generates a fresh secret, writes it
// to the file (0600), and returns it. This keeps JWT/invite secrets stable
// across restarts without requiring the operator to manage another env var.
func loadOrCreateSecret(envKey, filePath string, byteLen int, purpose string) string {
	if fromEnv := os.Getenv(envKey); fromEnv != "" {
		return fromEnv
	}
	if data, err := os.ReadFile(filePath); err == nil {
		if trimmed := strings.TrimSpace(string(data)); trimmed != "" {
			return trimmed
		}
	}
	randomBytes := make([]byte, byteLen)
	if _, err := rand.Read(randomBytes); err != nil {
		log.Fatalf("[Config] Failed to generate %s secret: %v", purpose, err)
	}
	secret := hex.EncodeToString(randomBytes)
	if err := os.WriteFile(filePath, []byte(secret+"\n"), 0600); err != nil {
		log.Printf("[Config] WARNING: %s secret generated but could not persist to %s: %v", purpose, filePath, err)
	} else {
		log.Printf("[Config] %s secret persisted to %s. Sessions stay valid across restarts.", purpose, filePath)
	}
	return secret
}
