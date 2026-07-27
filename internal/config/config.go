package config

import (
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
		secret = "orbit-secret-key-signature-token-safe-random-2026"
		log.Printf("[Config] Warning: ORBIT_JWT_SECRET env not set, using default signing secret.")
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
	}

	enableMockKeys := os.Getenv("ENABLE_MOCK_KEYS") != "false"

	return &Config{
		Port:           port,
		DatabasePath:   dbPath,
		JWTSecret:      secret,
		JWTExpiry:      72 * time.Hour,
		WebsiteURL:     websiteURL,
		ServerSecret:   serverSecret,
		EnableMockKeys: enableMockKeys,
	}
}
