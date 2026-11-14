package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/iamonah/cdc/internal/cdc"
	"github.com/iamonah/cdc/internal/config"
	"github.com/iamonah/cdc/internal/logging"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
)

const (
	reconnectDelay = 2 * time.Second
	maxFailures    = 3
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

// run listens for changes, reconnecting when the connection drops. It gives up
// after maxFailures consecutive attempts that cannot establish a session.
func run(ctx context.Context, log zerolog.Logger, cfg config.Config) error {
	failures := 0

	for {
		established, err := listen(ctx, log, cfg)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}

		// A session that connected and subscribed, then dropped later, does
		// not count towards the limit: only consecutive failures to establish
		// a session do.
		if established {
			failures = 0
		}
		failures++

		if failures >= maxFailures {
			return fmt.Errorf("giving up after %d attempts: %w", failures, err)
		}

		log.Warn().Err(err).Int("attempt", failures).Dur("retry_in", reconnectDelay).Msg("listener disconnected, retrying")

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(reconnectDelay):
		}
	}
}

// listen runs one session: connect, subscribe, and handle notifications. It
// reports whether the session was established before returning. A nil error
// means the context was cancelled.
func listen(ctx context.Context, log zerolog.Logger, cfg config.Config) (established bool, err error) {
	// LISTEN is connection-scoped, so the listener needs one dedicated
	// connection. A pgxpool would hand out different connections per query.
	conn, err := pgx.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return false, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(context.Background())

	listen := fmt.Sprintf("LISTEN %s", pgx.Identifier{cfg.Channel}.Sanitize())
	if _, err := conn.Exec(ctx, listen); err != nil {
		return false, fmt.Errorf("listen on %q: %w", cfg.Channel, err)
	}

	log.Info().Str("channel", cfg.Channel).Msg("listening for changes")

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				log.Info().Msg("listener shutting down")
				return true, nil
			}

			return true, fmt.Errorf("wait for notification: %w", err)
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
