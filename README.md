# CDC with Go and Postgres

Two change data capture (CDC) experiments side by side:

1. **LISTEN/NOTIFY** — trigger-based CDC, based on
   [Change Data Capture with Go and PostgreSQL (listen/notify)](https://www.youtube.com/watch?v=bnFkUp-jYiA).
2. **Logical replication** — WAL-based CDC using
   [pglogrepl](https://github.com/jackc/pglogrepl).

The LISTEN/NOTIFY flow is:

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

The logical replication flow is:

```
INSERT / UPDATE / DELETE on users
              │
              ▼
        Postgres WAL
              │
              ▼
   users_publication (publication)
              │
              ▼
   users_slot (replication slot)
              │
              ▼
   cmd/replicator (pglogrepl)  ──►  logs the change event
```

- `cmd/writer` writes to the `users` table.
- `cmd/listener` consumes trigger notifications from `users_changes`.
- `cmd/replicator` consumes WAL changes through a logical replication slot.

## Requirements

- Go 1.25+
- Docker (or your own Postgres 16+)
- [tern](https://github.com/jackc/tern) v2 (`tern` on your `PATH`):
  `go install github.com/jackc/tern/v2@latest`

## Quick start

```bash
cp .env.example .env
make up            # start Postgres on localhost:5433
make migrate-up    # create the users table, trigger, publication, etc.
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

## Logical replication with pglogrepl

The same `users` table is also published for logical replication. Compared to
LISTEN/NOTIFY, this reads Postgres' WAL instead of relying on a trigger.

`docker-compose.yml` starts Postgres with `wal_level=logical`, and
`migrations/003_create_users_publication.sql` adds:

```sql
CREATE PUBLICATION users_publication FOR TABLE users;
ALTER TABLE users REPLICA IDENTITY FULL;
```

`REPLICA IDENTITY FULL` makes UPDATE/DELETE messages include the complete old
row, not just the primary key.

Run the replicator instead of (or alongside) the listener:

```bash
# terminal 1
make run-replicator
```

```bash
# terminal 2
make run-writer
```

Expected output:

```
2026-11-05T01:08:53+01:00 INF created replication slot consistent_point=0/1BE33D0 plugin=pgoutput slot=users_slot
2026-11-05T01:08:53+01:00 INF replicating changes lsn=0/1BE33D0 publication=users_publication slot=users_slot
2026-11-05T01:08:55+01:00 INF change received action=INSERT id=1 lsn=0/1BE3438 row={"created_at":"...","email":"...","id":1,"name":"Logical Test","updated_at":"..."} table=users timestamp=... xid=758
```

The replicator creates a **persistent slot** (`users_slot`) on first run and
resumes from its confirmed LSN after a restart. Useful commands:

```bash
make replica-status   # slot state + retained WAL
make replica-clean    # drop the slot
```

A slot retains WAL until the consumer acknowledges it. For a long-running
setup, monitor `make replica-status` and set `max_slot_wal_keep_size` so an
abandoned slot cannot fill the disk. `make reset` deletes the volume and the
slot with it.

> If you previously ran an older version of this repo, run `make reset` once
> so the container is recreated with `wal_level=logical` and the replication
> `pg_hba.conf` entry. Init scripts only run on a fresh data volume.

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
| `make run-listener` | Run the LISTEN/NOTIFY listener |
| `make run-replicator` | Run the logical replication consumer |
| `make run-writer` | Run the writer loop |
| `make replica-status` | Show replication slot status and retained WAL |
| `make replica-clean` | Drop the replication slot |
| `make build` | Build all binaries into `./bin` |
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

## How it works: LISTEN/NOTIFY

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

## How it works: logical replication

### 1. Publication and replica identity

`migrations/003_create_users_publication.sql` creates a publication for the
`users` table and sets `REPLICA IDENTITY FULL`. The replicator asks Postgres
to stream that publication with the built-in `pgoutput` output plugin.

### 2. The replicator

`cmd/replicator` opens a normal `pgx` connection for catalog checks and a
second `pgconn` connection with `replication=database` for streaming. It:

1. Checks that `users_publication` exists.
2. Creates the `users_slot` logical replication slot if needed, or resumes
   from its `confirmed_flush_lsn`.
3. Runs `START_REPLICATION` with `proto_version '1'` and
   `publication_names 'users_publication'`.
4. Decodes `pgoutput` messages (`Relation`, `Begin`, `Insert`, `Update`,
   `Delete`, `Commit`) into the same event shape as the trigger payload.
5. Sends `StandbyStatusUpdate` messages to acknowledge processed WAL.

Because the slot persists, events are not lost while the replicator is
offline (until the slot's retained WAL is removed).

## Which approach should I use?

| | LISTEN/NOTIFY | Logical replication |
| --- | --- | --- |
| Capture source | Trigger per row | WAL stream |
| Delivery | At-most-once | At-least-once with LSN tracking |
| Replay after disconnect | No | Yes, via the slot |
| Setup | Trigger + listener | `wal_level=logical`, publication, slot |
| Payload limit | ~8 KB | No NOTIFY limit; streamed from WAL |
| Go dependency | `pgx` | `pglogrepl` + `pgx` |
| Complexity | Low | Medium/high |

Use LISTEN/NOTIFY for prototyping, cache invalidation, or waking up a poller.
Use logical replication when changes must not be missed.

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
│   ├── replicator/      # pglogrepl WAL consumer
│   └── writer/          # inserts/updates/deletes users
├── docker/
│   └── init/            # pg_hba rule for replication connections
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

All commands use [zerolog](https://github.com/rs/zerolog) through
`internal/logging`. Logs are written with a `ConsoleWriter` so they stay
readable in a terminal; colours are turned off automatically when stdout is
not a terminal (for example when redirecting to a file).

## Troubleshooting

**The listener prints nothing.** Check that migrations ran
(`make migrate-status`), that the listener is connected to the same database,
and that `CDC_CHANNEL` matches the channel in the trigger
(`users_changes`).

**The replicator fails with `no pg_hba.conf entry for replication connection`.**
The init script that permits replication connections only runs when the data
volume is first created. Recreate it with `make reset && make up &&
make migrate-up`.

**The replicator says the publication does not exist.** Apply migrations with
`make migrate-up`; `003_create_users_publication.sql` creates it.

**The replication slot is retaining WAL.** Check it with `make replica-status`
and remove it with `make replica-clean` when you are done experimenting.

**`make up` cannot connect to Docker.** Make sure Docker is running. If you
have more than one Docker context, `docker context use default` or run
`docker compose` with the right context.

**Port already in use.** Change `5433:5432` in `docker-compose.yml` and update
`DATABASE_URL` in `.env`.
