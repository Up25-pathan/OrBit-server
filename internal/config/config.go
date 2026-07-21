package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"time"
)

type Config struct {
	Port         string
	DatabasePath string
	JWTSecret    string
	JWTExpiry    time.Duration
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
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			log.Fatalf("failed to generate random JWT secret: %v", err)
		}
		secret = hex.EncodeToString(b)
		log.Println("WARNING: ORBIT_JWT_SECRET not set. Auto-generated a random secret for this session. Logins will not persist across server restarts.")
	}

	return &Config{
		Port:         port,
		DatabasePath: dbPath,
		JWTSecret:    secret,
		JWTExpiry:    72 * time.Hour,
	}
}
