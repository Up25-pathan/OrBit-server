package config

import (
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
		secret = "orbit-dev-secret-change-in-production"
	}

	return &Config{
		Port:         port,
		DatabasePath: dbPath,
		JWTSecret:    secret,
		JWTExpiry:    72 * time.Hour,
	}
}
