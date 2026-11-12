# CDC with Go, Postgres, and LISTEN/NOTIFY

A small, hands-on change data capture (CDC) experiment based on
[Change Data Capture with Go and PostgreSQL (listen/notify)](https://www.youtube.com/watch?v=bnFkUp-jYiA).

The flow is:

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
   cmd/listener  ──►  logs the change event
```

- `cmd/writer` writes to the `users` table.
- Postgres triggers publish a JSON event for every row change.
- `cmd/listener` holds one connection open and listens for those events.

For a detailed tour of the Go code and SQL, see
[docs/code-walkthrough.md](docs/code-walkthrough.md).

## Requirements

- Go 1.25+
- Docker (or your own Postgres 16+)
- [tern](https://github.com/jackc/tern) v2 (`tern` on your `PATH`):
  `go install github.com/jackc/tern/v2@latest`

## Quick start

```bash
cp .env.example .env
make up            # start Postgres on localhost:5433
make migrate-up    # create the users table, trigger, and function
```

In two separate terminals:

```bash
# terminal 1
make run-listener
```

```bash
# terminal 2
make run-writer
```

The writer inserts a random user every two seconds. The listener prints
something like:

```
2026-11-05T00:48:19+01:00 INF listening for changes channel=users_changes
2026-11-05T00:48:19+01:00 INF change received action=INSERT id=1 row={"created_at":"...","email":"ada...@example.com","id":1,"name":"Ada Lovelace","updated_at":"..."} table=users timestamp=...
```

Try updates and deletes by hand:

```bash
make run-writer ARGS='update -id 1 -name "Ada L."'
make run-writer ARGS='delete -id 1'
```

Or from `psql` (`make psql`):

```sql
INSERT INTO users (name, email) VALUES ('Grace Hopper', 'grace@example.com');
UPDATE users SET name = 'Rear Admiral Grace Hopper' WHERE email = 'grace@example.com';
DELETE FROM users WHERE email = 'grace@example.com';
```

## Useful commands

| Command | What it does |
| --- | --- |
| `make up` / `make down` | Start / stop Postgres |
| `make logs` | Follow Postgres logs |
| `make psql` | Open a psql session |
| `make migrate-up` | Apply migrations |
| `make migrate-down` | Roll back the last migration |
| `make migrate-status` | Show the current migration status |
| `make migrate-create name=add_widgets` | Create a new migration |
| `make run-listener` | Run the listener |
| `make run-writer` | Run the writer loop |
| `make build` | Build both binaries into `./bin` |
| `make reset` | Delete the database volume |

Writer commands:

```bash
go run ./cmd/writer                                   # insert random users forever
go run ./cmd/writer -interval 500ms                   # faster inserts
go run ./cmd/writer create -name "Ada" -email ada@example.com
go run ./cmd/writer update -id 1 -name "Ada L."       # -name and/or -email
go run ./cmd/writer delete -id 1
```

## Migrations (tern)

Migrations live in `migrations/` and follow tern's naming convention:
`NNN_description.sql`. Each file contains the up statements, a separator, and
the down statements:

```sql
CREATE TABLE widgets (...);

---- create above / drop below ----

DROP TABLE widgets;
```

Tern records the applied version in `public.schema_version`. Create a new
migration with `make migrate-create name=add_widgets`, apply it with
`make migrate-up`, and roll it back with `make migrate-down` (which runs
`tern migrate --destination -1`).

The raw tern commands are also available:

```bash
tern migrate --migrations ./migrations --conn-string "$DATABASE_URL"
tern migrate --migrations ./migrations --conn-string "$DATABASE_URL" --destination -1
tern status  --migrations ./migrations --conn-string "$DATABASE_URL"
```

## How it works

### 1. The table

`migrations/001_create_users.sql` creates a normal `users` table.

### 2. The trigger

`migrations/002_create_users_notify_trigger.sql` installs a function
and an `AFTER INSERT OR UPDATE OR DELETE ... FOR EACH ROW` trigger. The
function builds one JSON payload per changed row and calls:

```sql
PERFORM pg_notify('users_changes', payload::text);
```

Payload shape:

```json
{
  "action": "INSERT | UPDATE | DELETE",
  "table": "users",
  "id": 1,
  "data": { "...the new row (or old row for DELETE)..." },
  "old": { "...the previous row, only on UPDATE/DELETE..." },
  "timestamp": "2026-11-05T12:34:56.789+00:00"
}
```

### 3. The listener

The listener uses a **dedicated** `pgx.Conn`, not a pool, because `LISTEN` is
scoped to a single connection. It runs `LISTEN users_changes` once and then
blocks in `WaitForNotification`, decoding and logging each payload.

## Things to know about LISTEN/NOTIFY

This is the simplest way to experiment with CDC, but it has important limits:

- **At-most-once delivery.** Notifications only reach sessions that are
  already listening. Missed messages are gone forever; there is no replay.
- **Payload size.** A notification must fit in roughly 8000 bytes. Wide rows
  need a payload with only the `id`; the listener can then fetch the row.
- **Ephemeral on disconnect.** If the listener's connection drops, events
  published while it is away are lost.
- **No history.** Unlike an audit table or logical replication, there is no
  log of past changes.
- **Transaction visibility.** Notifications are delivered when the
  transaction commits. A rolled-back change never reaches listeners.

For prototyping, cache invalidation, or waking up a poller, this is a great
fit. For guaranteed delivery, use an audit/outbox table or logical
replication (e.g. `wal2json`, `pgoutput`, or a tool such as Sequin).

## Project layout

```
.
├── cmd/
│   ├── listener/        # LISTEN + WaitForNotification loop
│   └── writer/          # inserts/updates/deletes users
├── docs/                # detailed code walkthrough
├── internal/
│   ├── cdc/             # JSON event type
│   ├── config/          # env/.env loading
│   ├── db/              # pgxpool setup
│   ├── logging/         # zerolog console logger
│   └── store/           # users queries
├── migrations/          # tern migrations (up + down in one file)
├── docker-compose.yml
└── Makefile
```

## Logging

Both commands use [zerolog](https://github.com/rs/zerolog) through
`internal/logging`. Logs are written with a `ConsoleWriter` so they stay
readable in a terminal; colours are turned off automatically when stdout is
not a terminal (for example when redirecting to a file).

## Troubleshooting

**The listener prints nothing.** Check that migrations ran
(`make migrate-status`), that the listener is connected to the same database,
and that `CDC_CHANNEL` matches the channel in the trigger
(`users_changes`).

**`make up` cannot connect to Docker.** Make sure Docker is running. If you
have more than one Docker context, `docker context use default` or run
`docker compose` with the right context.

**Port already in use.** Change `5433:5432` in `docker-compose.yml` and update
`DATABASE_URL` in `.env`.
