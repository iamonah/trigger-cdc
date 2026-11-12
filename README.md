# CDC with Go, Postgres, and LISTEN/NOTIFY

A small change data capture (CDC) experiment based on
[Change Data Capture with Go and PostgreSQL (listen/notify)](https://www.youtube.com/watch?v=bnFkUp-jYiA).

A Postgres trigger publishes every row change as JSON, and a small Go service
consumes those events in real time.

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
        cmd/listener
```

## How it works

**Table.** `migrations/001_create_users.sql` creates a normal `users` table.

**Trigger.** `migrations/002_create_users_notify_trigger.sql` installs an
`AFTER INSERT OR UPDATE OR DELETE ... FOR EACH ROW` trigger. For each changed
row it calls:

```sql
PERFORM pg_notify('users_changes', payload::text);
```

The payload looks like:

```json
{
  "action": "INSERT | UPDATE | DELETE",
  "table": "users",
  "id": 1,
  "data": { "...new row, or the deleted row..." },
  "old": { "...previous row, on UPDATE and DELETE..." },
  "timestamp": "2026-11-05T12:34:56.789+00:00"
}
```

**Listener.** `cmd/listener` uses a dedicated `pgx.Conn` (not a pool, since
`LISTEN` is connection-scoped), subscribes to the channel, and blocks in
`WaitForNotification`, logging each decoded event.

## Requirements

- Go 1.25+
- Docker, or Postgres 16+
- [tern](https://github.com/jackc/tern) v2 on your `PATH`:
  `go install github.com/jackc/tern/v2@latest`

## Quick start

```bash
make up          # start Postgres on localhost:5433
make migrate-up  # create the users table and trigger
```

Then, in two terminals:

```bash
make run-listener
make run-writer     # inserts a random user every two seconds
```

When a row changes, the listener logs an event such as:

```
INF listening for changes channel=users_changes
INF change received action=INSERT id=1 row={"email":"ada@example.com","id":1,...} table=users
```

Use the writer for one-off changes:

```bash
make run-writer ARGS='create -name "Ada" -email ada@example.com'
make run-writer ARGS='update -id 1 -name "Ada L."'
make run-writer ARGS='delete -id 1'
```

## Configuration

| Variable | Default |
| --- | --- |
| `DATABASE_URL` | `postgres://cdc:cdc@localhost:5433/cdc?sslmode=disable` |
| `CDC_CHANNEL` | `users_changes` |

Both are read from the environment. A local `.env` file is also loaded if
present.

## Commands

Run `make` or `make help` to list all targets, including migrations, Docker
and reset commands.

## Migrations

Migrations follow tern's `NNN_description.sql` convention. Each file contains
the up statements, a separator, and the down statements:

```sql
CREATE TABLE widgets (...);

---- create above / drop below ----

DROP TABLE widgets;
```

Applied versions are stored in `public.schema_version`. Use `make
migrate-create`, `make migrate-up`, `make migrate-down` and `make
migrate-status`.

## Limitations of LISTEN/NOTIFY

Trigger-based CDC keeps the setup simple, but it has significant limits:

- **At-most-once delivery.** Notifications reach only sessions that are
  already listening. Events published while the listener is offline are lost.
- **No history.** There is no record of past changes to replay.
- **Payload size.** Notifications must fit in roughly 8000 bytes. For wide
  rows, publish only the `id` and fetch the row in the listener.
- **Commit visibility.** Notifications are delivered when the transaction
  commits, so rolled-back changes are never published.

For guaranteed delivery, use an outbox/audit table or WAL-based replication.

## Project layout

```
.
├── cmd/
│   ├── listener/        # LISTEN + WaitForNotification loop
│   └── writer/          # inserts, updates and deletes users
├── internal/
│   ├── cdc/             # JSON event type
│   ├── config/          # environment configuration
│   ├── db/              # pgxpool setup
│   ├── logging/         # zerolog logger
│   └── store/           # users queries
├── migrations/          # tern migrations
├── docker-compose.yml
└── Makefile
```

## Troubleshooting

**The listener prints nothing.** Confirm migrations were applied
(`make migrate-status`), that both processes use the same `DATABASE_URL`, and
that `CDC_CHANNEL` matches the channel used by the trigger.

**Docker connection errors.** Make sure Docker is running. If you have more
than one context, select the right one with `docker context use default`.

**Port already in use.** Change `5433:5432` in `docker-compose.yml` and update
`DATABASE_URL`.
