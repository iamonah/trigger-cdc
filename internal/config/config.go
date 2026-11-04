package config

import (
	"os"

	"github.com/joho/godotenv"
)

// Config holds the settings shared by the listener and the writer.
type Config struct {
	DatabaseURL string
	Channel     string
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

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return fallback
}
