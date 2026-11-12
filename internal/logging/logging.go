// Package logging builds the application's zerolog logger.
package logging

import (
	"os"
	"time"

	"github.com/rs/zerolog"
)

// New returns a human-readable console logger. Colours are disabled when
// stdout is not a terminal (for example when output is piped to a file).
func New() zerolog.Logger {
	out := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.RFC3339,
	}

	if info, err := os.Stdout.Stat(); err != nil || info.Mode()&os.ModeCharDevice == 0 {
		out.NoColor = true
	}

	return zerolog.New(out).Level(zerolog.InfoLevel).With().Timestamp().Logger()
}
