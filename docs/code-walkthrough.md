# Code walkthrough

This document explains how the trigger-based CDC demo works, from a row
change in Postgres to a log line in the listener.

For setup and commands, see the [README](../README.md).

## The flow

```
INSERT / UPDATE / DELETE on users
              │
              ▼
   users_notify_changes trigger
              │
              ▼
   pg_notify('users_changes', json)
              │
              ▼
   cmd/listener (LISTEN + WaitForNotification)
              │
              ▼
        structured log line
```

1. The writer (or `psql`) changes a row in `users`.
2. An `AFTER INSERT OR UPDATE OR DELETE` trigger runs in the same transaction.
3. The trigger builds a JSON payload and calls `pg_notify`.
4. Postgres delivers the notification to every session listening on the
   channel, but only after the transaction commits.
5. The listener decodes the JSON and logs it.

## 1. The table

`migrations/001_create_users.sql` creates the table:

```sql
CREATE TABLE users (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       TEXT NOT NULL,
    email      TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

The table is deliberately ordinary. CDC is added entirely through the
trigger in the next migration.

## 2. The trigger

`migrations/002_create_users_notify_trigger.sql` creates a trigger function
and attaches it to the table:

```sql
CREATE TRIGGER users_notify_changes
AFTER INSERT OR UPDATE OR DELETE ON users
FOR EACH ROW
EXECUTE FUNCTION notify_users_changes();
```

A few things are intentional here:

- **`AFTER`** means the row change already succeeded. The trigger does not
  need to validate the row; it only reports what happened.
- **`FOR EACH ROW`** produces one event per changed row, not one per
  statement.
- **`INSERT OR UPDATE OR DELETE`** covers all three change types.
- Because the trigger runs inside the transaction, a rolled-back change never
  produces a notification.

Inside the function, `TG_OP` tells you which operation fired the trigger:

```sql
IF TG_OP = 'DELETE' THEN
    row_data := to_jsonb(OLD);
    old_data := to_jsonb(OLD);
ELSIF TG_OP = 'UPDATE' THEN
    row_data := to_jsonb(NEW);
    old_data := to_jsonb(OLD);
ELSE
    row_data := to_jsonb(NEW);
END IF;
```

For an `INSERT` there is no previous row, so `old` is omitted. For an
`UPDATE`, both the new and previous rows are included. For a `DELETE`, the
deleted row is used as both `data` and `old`.

The actual publish is a single call:

```sql
PERFORM pg_notify('users_changes', payload::text);
```

`pg_notify` is used instead of the `NOTIFY` SQL statement so the channel and
payload can be built dynamically.

## 3. The payload

The trigger publishes JSON shaped like this:

```json
{
  "action": "UPDATE",
  "table": "users",
  "id": 42,
  "data": {
    "id": 42,
    "name": "Ada Lovelace",
    "email": "ada@example.com",
    "created_at": "2026-11-05T12:00:00+00:00",
    "updated_at": "2026-11-05T12:01:00+00:00"
  },
  "old": {
    "id": 42,
    "name": "Ada L.",
    "email": "ada@example.com",
    "created_at": "2026-11-05T12:00:00+00:00",
    "updated_at": "2026-11-05T12:00:00+00:00"
  },
  "timestamp": "2026-11-05T12:01:00.123+00:00"
}
```

`internal/cdc/event.go` mirrors this shape in Go:

```go
type Change struct {
    Action    Action          `json:"action"`
    Table     string          `json:"table"`
    ID        int64           `json:"id"`
    Data      json.RawMessage `json:"data"`
    Old       json.RawMessage `json:"old,omitempty"`
    Timestamp time.Time       `json:"timestamp"`
}
```

`json.RawMessage` keeps the row as raw JSON, so the listener can log it
without defining a struct for every table.

> Keep an eye on the ~8000-byte `NOTIFY` payload limit. For wide rows, publish
> only the `id` and let the consumer fetch the row.

## 4. The listener

`cmd/listener/main.go` has three parts.

### `main`

`main` creates the zerolog logger and a context that is cancelled on
`Ctrl+C` / `SIGTERM`:

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()

if err := run(ctx, log, config.Load()); err != nil {
    log.Error().Err(err).Msg("listener stopped")
    os.Exit(1)
}
```

Passing the context down means the blocking notification wait can be
cancelled cleanly.

### `run`

`run` opens one dedicated connection:

```go
conn, err := pgx.Connect(ctx, cfg.DatabaseURL)
```

A pool is intentionally not used. `LISTEN` applies to a single Postgres
session, so every notification wait must use the same connection that ran
`LISTEN`.

It then subscribes to the channel:

```go
listen := fmt.Sprintf("LISTEN %s", pgx.Identifier{cfg.Channel}.Sanitize())
if _, err := conn.Exec(ctx, listen); err != nil {
    return fmt.Errorf("listen on %q: %w", cfg.Channel, err)
}
```

`pgx.Identifier{...}.Sanitize()` quotes the channel name properly. Postgres
does not allow bind parameters (`$1`) for identifiers, so this is the safe way
to build the statement from configuration. With the default config it
produces:

```sql
LISTEN "users_changes"
```

The channel must match the string in the trigger's `pg_notify` call exactly.

### `handle`

The receive loop blocks until Postgres delivers a notification:

```go
notification, err := conn.WaitForNotification(ctx)
```

Each notification is passed to `handle`, which decodes the payload and logs
it. RawJSON fields are used so the row is emitted as JSON rather than an
escaped string:

```go
event := log.Info().
    Str("action", string(change.Action)).
    Str("table", change.Table).
    Int64("id", change.ID).
    RawJSON("row", change.Data).
    Time("timestamp", change.Timestamp)
```

If the payload cannot be decoded, the listener logs a warning and keeps
running.

## 5. The writer

`cmd/writer/main.go` is the producer. It supports a continuous loop and
one-off commands:

```bash
go run ./cmd/writer                             # insert every 2s
go run ./cmd/writer -interval 500ms             # faster
go run ./cmd/writer create -name "Ada" -email ada@example.com
go run ./cmd/writer update -id 1 -name "Ada L."
go run ./cmd/writer delete -id 1
```

The default `loop` command inserts a random user, waits for the interval, and
repeats until the context is cancelled.

The database work lives in `internal/store/users.go`:

```go
func (u *Users) Create(ctx context.Context, name, email string) (User, error) {
    const query = `
        INSERT INTO users (name, email)
        VALUES ($1, $2)
        RETURNING id, name, email, created_at, updated_at`

    return scanUser(u.pool.QueryRow(ctx, query, name, email))
}
```

`Update` uses `COALESCE(NULLIF($2, ''), name)` so an empty flag means “leave
this column unchanged”. `Delete` reports an error when no row matched.

## 6. Supporting packages

| Package | Responsibility |
| --- | --- |
| `internal/cdc` | The `Change` event shape |
| `internal/config` | Reads `.env`, environment variables, and defaults |
| `internal/db` | Creates and pings the `pgxpool` used by the writer |
| `internal/logging` | Creates the zerolog console logger |
| `internal/store` | SQL queries for the `users` table |

The listener does not use `internal/db` because it needs one dedicated
connection, not a pool.

## 7. Migrations with tern

Migrations are named `NNN_description.sql`. The up statements go above a
separator and the down statements go below it:

```sql
CREATE TABLE widgets (...);

---- create above / drop below ----

DROP TABLE widgets;
```

Applied versions are stored in `public.schema_version`. Use:

```bash
make migrate-up
make migrate-down      # roll back one migration
make migrate-status
make migrate-create name=add_widgets
```

## 8. Experiments

### See at-most-once delivery

1. Stop the listener.
2. Insert or update a user.
3. Start the listener again.

The change made while the listener was stopped is gone. Postgres does not
store notifications for offline sessions.

### See rolled-back changes disappear

Open `psql` and run:

```sql
BEGIN;
INSERT INTO users (name, email) VALUES ('Ghost', 'ghost@example.com');
ROLLBACK;
```

The listener receives nothing because the transaction never committed.

### Publish a notification by hand

With the listener running:

```sql
SELECT pg_notify('users_changes', '{"action":"TEST","table":"users","id":0,"data":{}}');
```

The listener logs it as a change event. Because the payload was not produced
by the trigger, fields such as `timestamp` are zero — a reminder that the
trigger and listener share a JSON contract.

Sending invalid JSON shows the decode error path:

```sql
SELECT pg_notify('users_changes', 'not json');
```

The listener logs a warning and keeps running.

### Inspect the trigger

```sql
\d users
\df notify_users_changes
SELECT tgname, tgrelid::regclass, tgfoid::regproc
FROM pg_trigger
WHERE NOT tgisinternal;
```

### Inspect the notification queue

```sql
SELECT pg_notification_queue_usage();
```

This returns a fraction between 0 and 1. A high value means listeners are not
keeping up or many notifications are queued.

### Change the channel

`CDC_CHANNEL` controls what the listener subscribes to. The trigger currently
hardcodes `users_changes`, so changing only the env var is not enough — also
change the `pg_notify` call in the migration (or make the trigger read a
setting).

## 9. Where to go next

This approach is intentionally small. Common next steps:

- **Audit/outbox table** — persist changes so they survive listener
  disconnects and can be replayed.
- **Polling + notify** — keep the table as the source of truth and use
  notifications only to wake up a poller.
- **WAL-based CDC** — stream changes from Postgres' write-ahead log when you
  need at-least-once delivery without triggers.

Each of these solves a limitation listed in the README's
[LISTEN/NOTIFY caveats](../README.md#things-to-know-about-listennotify).
