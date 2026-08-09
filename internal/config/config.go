package config

import (
	"log"
	"os"
	"time"
)

type Config struct {
	Port           string
	DatabasePath   string
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
		JWTExpiry:      72 * time.Hour,
		WebsiteURL:     websiteURL,
		ServerSecret:   serverSecret,
		EnableMockKeys: false,
	}
}
