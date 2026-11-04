package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/iamonah/cdc/internal/config"
	"github.com/iamonah/cdc/internal/db"
	"github.com/iamonah/cdc/internal/logging"
	"github.com/iamonah/cdc/internal/store"

	"github.com/rs/zerolog"
)

func main() {
	log := logging.New()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := config.Load()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error().Err(err).Msg("could not connect to postgres")
		os.Exit(1)
	}
	defer pool.Close()

	users := store.NewUsers(pool)

	args := os.Args[1:]
	command := "loop"
	if len(args) > 0 {
		command, args = args[0], args[1:]
	}

	var runErr error

	switch command {
	case "loop":
		runErr = runLoop(ctx, log, users, args)
	case "create":
		runErr = runCreate(ctx, log, users, args)
	case "update":
		runErr = runUpdate(ctx, log, users, args)
	case "delete":
		runErr = runDelete(ctx, log, users, args)
	case "help", "-h", "--help":
		usage()
		return
	default:
		usage()
		runErr = fmt.Errorf("unknown command %q", command)
	}

	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		log.Error().Err(runErr).Msg("writer stopped")
		os.Exit(1)
	}
}

// runLoop is the default command: insert a random user on every tick.
func runLoop(ctx context.Context, log zerolog.Logger, users *store.Users, args []string) error {
	fs := flag.NewFlagSet("loop", flag.ExitOnError)
	interval := fs.Duration("interval", 2*time.Second, "delay between inserts")
	fs.Parse(args)

	if *interval <= 0 {
		return errors.New("loop: -interval must be positive")
	}

	log.Info().Dur("interval", *interval).Msg("writing users")

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	for {
		user, err := users.Create(ctx, randomName(), randomEmail())
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Error().Err(err).Msg("could not create user")
		} else {
			log.Info().
				Int64("id", user.ID).
				Str("name", user.Name).
				Str("email", user.Email).
				Msg("user created")
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func runCreate(ctx context.Context, log zerolog.Logger, users *store.Users, args []string) error {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	name := fs.String("name", randomName(), "name for the new user")
	email := fs.String("email", "", "email for the new user (default: generated)")
	fs.Parse(args)

	if *email == "" {
		*email = randomEmail()
	}

	user, err := users.Create(ctx, *name, *email)
	if err != nil {
		return err
	}

	log.Info().
		Int64("id", user.ID).
		Str("name", user.Name).
		Str("email", user.Email).
		Msg("user created")

	return nil
}

func runUpdate(ctx context.Context, log zerolog.Logger, users *store.Users, args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	id := fs.Int64("id", 0, "id of the user to update (required)")
	name := fs.String("name", "", "new name")
	email := fs.String("email", "", "new email")
	fs.Parse(args)

	if *id <= 0 {
		return errors.New("update: -id is required")
	}
	if *name == "" && *email == "" {
		return errors.New("update: pass -name and/or -email")
	}

	user, err := users.Update(ctx, *id, *name, *email)
	if err != nil {
		return err
	}

	log.Info().
		Int64("id", user.ID).
		Str("name", user.Name).
		Str("email", user.Email).
		Msg("user updated")

	return nil
}

func runDelete(ctx context.Context, log zerolog.Logger, users *store.Users, args []string) error {
	fs := flag.NewFlagSet("delete", flag.ExitOnError)
	id := fs.Int64("id", 0, "id of the user to delete (required)")
	fs.Parse(args)

	if *id <= 0 {
		return errors.New("delete: -id is required")
	}

	if err := users.Delete(ctx, *id); err != nil {
		return err
	}

	log.Info().Int64("id", *id).Msg("user deleted")
	return nil
}

var names = []string{
	"Ada Lovelace",
	"Alan Turing",
	"Barbara Liskov",
	"Edsger Dijkstra",
	"Grace Hopper",
	"Margaret Hamilton",
}

func randomName() string {
	return names[rand.IntN(len(names))]
}

func randomEmail() string {
	local := strings.ToLower(strings.ReplaceAll(randomName(), " ", "."))
	return fmt.Sprintf("%s.%d@example.com", local, time.Now().UnixNano())
}

func usage() {
	fmt.Fprint(os.Stderr, `Usage: writer [command] [flags]

Commands:
  loop      Insert random users on an interval (default)
  create    Insert one user
  update    Update a user
  delete    Delete a user

Examples:
  go run ./cmd/writer
  go run ./cmd/writer create -name "Ada Lovelace" -email ada@example.com
  go run ./cmd/writer update -id 1 -name "Ada L."
  go run ./cmd/writer delete -id 1
`)
}
