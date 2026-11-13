# Trigger-based CDC with Postgres and Go

A small change data capture (CDC) example that uses Postgres triggers and
`LISTEN/NOTIFY` to publish row changes and a Go service to consume them in
real time.

Inspired by
[Change Data Capture with Go and PostgreSQL (listen/notify)](https://www.youtube.com/watch?v=bnFkUp-jYiA).

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

`migrations/001_create_users.sql` creates the `users` table.
`migrations/002_create_users_notify_trigger.sql` adds an
`AFTER INSERT OR UPDATE OR DELETE ... FOR EACH ROW` trigger that publishes one
JSON payload per changed row:

```sql
PERFORM pg_notify('users_changes', payload::text);
```

Payload:

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

The listener holds a dedicated `pgx.Conn` because `LISTEN` is
connection-scoped, and blocks in `WaitForNotification`, logging each event.

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
make run-writer   # insert a random user every two seconds
```

The listener logs events as they arrive:

```
INF listening for changes channel=users_changes
INF change received action=INSERT id=1 row={"email":"ada@example.com","id":1,...} table=users
```

For one-off changes:

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

Both are read from the environment; a local `.env` file is loaded if present.
Run `make help` for the full list of targets.

## Migrations

Migrations follow tern's `NNN_description.sql` convention and contain both
directions:

```sql
CREATE TABLE widgets (...);

---- create above / drop below ----

DROP TABLE widgets;
```

Applied versions are recorded in `public.schema_version`. Use
`make migrate-create`, `make migrate-up`, `make migrate-down` and
`make migrate-status`.

## Limitations

`LISTEN/NOTIFY` is simple, but delivery is not guaranteed:

- **At-most-once.** Only sessions already listening receive an event. Changes
  published while the listener is offline are lost.
- **No history.** There is no log of past changes to replay.
- **Payload size.** Notifications must fit in roughly 8000 bytes. Wide rows
  should publish only the `id` and let the listener fetch the row.
- **Commit visibility.** Events are published when the transaction commits;
  rolled-back changes are never delivered.

For guaranteed delivery, use an outbox table or WAL-based replication.

## Troubleshooting

**The listener prints nothing.** Confirm migrations were applied
(`make migrate-status`), both processes use the same `DATABASE_URL`, and
`CDC_CHANNEL` matches the channel in the trigger.

**Docker connection errors.** Ensure Docker is running and, if you have
multiple contexts, select one with `docker context use default`.

**Port already in use.** Change `5433:5432` in `docker-compose.yml` and update
`DATABASE_URL`.
