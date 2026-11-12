package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/iamonah/cdc/internal/cdc"
	"github.com/iamonah/cdc/internal/config"
	"github.com/iamonah/cdc/internal/logging"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
)

// main configures logging and signal handling, then runs the listener.
func main() {
	log := logging.New()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, log, config.Load()); err != nil {
		log.Error().Err(err).Msg("listener stopped")
		os.Exit(1)
	}
}

// run opens a dedicated connection, subscribes to the configured channel, and
// blocks until the context is cancelled or the connection fails.
func run(ctx context.Context, log zerolog.Logger, cfg config.Config) error {
	// LISTEN is connection-scoped, so the listener needs one dedicated
	// connection. A pgxpool would hand out different connections per query.
	conn, err := pgx.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(context.Background())

	listen := fmt.Sprintf("LISTEN %s", pgx.Identifier{cfg.Channel}.Sanitize())
	if _, err := conn.Exec(ctx, listen); err != nil {
		return fmt.Errorf("listen on %q: %w", cfg.Channel, err)
	}

	log.Info().Str("channel", cfg.Channel).Msg("listening for changes")

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				log.Info().Msg("listener shutting down")
				return nil
			}

			return fmt.Errorf("wait for notification: %w", err)
		}

		handle(log, notification)
	}
}

// handle decodes one notification payload and logs it. Malformed payloads are
// logged and skipped so one bad event does not stop the listener.
func handle(log zerolog.Logger, notification *pgconn.Notification) {
	var change cdc.Change
	if err := json.Unmarshal([]byte(notification.Payload), &change); err != nil {
		log.Warn().
			Str("payload", notification.Payload).
			Err(err).
			Msg("could not decode notification")
		return
	}

	event := log.Info().
		Str("action", string(change.Action)).
		Str("table", change.Table).
		Int64("id", change.ID).
		RawJSON("row", change.Data).
		Time("timestamp", change.Timestamp)

	if len(change.Old) > 0 && string(change.Old) != "null" {
		event = event.RawJSON("old", change.Old)
	}

	event.Msg("change received")
}
