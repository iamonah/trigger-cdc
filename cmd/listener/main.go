package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/iamonah/cdc/internal/cdc"
	"github.com/iamonah/cdc/internal/config"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, config.Load()); err != nil {
		slog.Error("listener stopped", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config) error {
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

	slog.Info("listening for changes", "channel", cfg.Channel)

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				slog.Info("listener shutting down")
				return nil
			}

			return fmt.Errorf("wait for notification: %w", err)
		}

		handle(notification)
	}
}

func handle(notification *pgconn.Notification) {
	var change cdc.Change
	if err := json.Unmarshal([]byte(notification.Payload), &change); err != nil {
		slog.Warn("could not decode notification",
			"payload", notification.Payload,
			"err", err,
		)
		return
	}

	attrs := []any{
		"action", change.Action,
		"table", change.Table,
		"id", change.ID,
		"row", string(change.Data),
		"timestamp", change.Timestamp,
	}

	if len(change.Old) > 0 && string(change.Old) != "null" {
		attrs = append(attrs, "old", string(change.Old))
	}

	slog.Info("change received", attrs...)
}
