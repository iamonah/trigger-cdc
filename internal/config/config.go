// Package config loads database and channel settings from the environment.
package config

import (
	"os"

	"github.com/joho/godotenv"
)

// Config holds the settings shared by the listener and the writer.
type Config struct {
	DatabaseURL string // DATABASE_URL
	Channel     string // CDC_CHANNEL
}

// Load reads .env (if present) and then falls back to environment variables
// and sensible local defaults.
func Load() Config {
	_ = godotenv.Load()

	return Config{
		DatabaseURL: getenv("DATABASE_URL", "postgres://cdc:cdc@localhost:5433/cdc?sslmode=disable"),
		Channel:     getenv("CDC_CHANNEL", "users_changes"),
	}
}

// getenv returns the environment variable or fallback when it is unset/empty.
func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return fallback
}
