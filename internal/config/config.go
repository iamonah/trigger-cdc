package config

import (
	"os"

	"github.com/joho/godotenv"
)

// Config holds the settings shared by the listener, writer, and replicator.
type Config struct {
	DatabaseURL string
	Channel     string
	Publication string
	Slot        string
}

// Load reads .env (if present) and then falls back to environment variables
// and sensible local defaults.
func Load() Config {
	_ = godotenv.Load()

	return Config{
		DatabaseURL: getenv("DATABASE_URL", "postgres://cdc:cdc@localhost:5433/cdc?sslmode=disable"),
		Channel:     getenv("CDC_CHANNEL", "users_changes"),
		Publication: getenv("CDC_PUBLICATION", "users_publication"),
		Slot:        getenv("CDC_SLOT", "users_slot"),
	}
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return fallback
}
