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
}

func Load() *Config {
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

	jwtSecret := loadOrCreateSecret("ORBIT_JWT_SECRET", secretFilePath(dbPath, "orbit.jwt-secret"), 32, "JWT")
	inviteSalt := loadOrCreateSecret("ORBIT_INVITE_SALT", secretFilePath(dbPath, "orbit.invite-salt"), 16, "invite")

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

	return &Config{
		Port:           port,
		DatabasePath:   dbPath,
		JWTSecret:      jwtSecret,
		JWTExpiry:      72 * time.Hour,
		WebsiteURL:     websiteURL,
		ServerSecret:   serverSecret,
		InviteSalt:     inviteSalt,
	}
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
