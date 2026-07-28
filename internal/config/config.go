package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
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

	secret := os.Getenv("ORBIT_JWT_SECRET")
	if secret == "" {
		// Audit Fix #7: Generate a random JWT secret instead of using a hardcoded default.
		// This secret won't persist across restarts unless the env var is set.
		randomBytes := make([]byte, 32)
		_, _ = rand.Read(randomBytes)
		secret = hex.EncodeToString(randomBytes)
		log.Printf("[Config] WARNING: ORBIT_JWT_SECRET env not set. Generated ephemeral secret. Set ORBIT_JWT_SECRET for persistent sessions.")
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
		// Audit Fix #7: Generate an ephemeral secret instead of hardcoded default
		randomBytes := make([]byte, 16)
		_, _ = rand.Read(randomBytes)
		serverSecret = "ephemeral-" + hex.EncodeToString(randomBytes)
		log.Printf("[Config] WARNING: CONTROL_SERVER_SECRET env not set. Generated ephemeral secret.")
	}

	return &Config{
		Port:           port,
		DatabasePath:   dbPath,
		JWTSecret:      secret,
		JWTExpiry:      72 * time.Hour,
		WebsiteURL:     websiteURL,
		ServerSecret:   serverSecret,
	}
}
