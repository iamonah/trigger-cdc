package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"github.com/iamonah/cdc/internal/cdc"
	"github.com/iamonah/cdc/internal/config"
	"github.com/iamonah/cdc/internal/logging"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog"
)

const (
	outputPlugin     = "pgoutput"
	standbyInterval  = 10 * time.Second
	publicationProto = "1"
)

var identifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func main() {
	log := logging.New()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, log, config.Load()); err != nil {
		log.Error().Err(err).Msg("replicator stopped")
		os.Exit(1)
	}
}

func run(ctx context.Context, log zerolog.Logger, cfg config.Config) error {
	if err := validateIdentifier("publication", cfg.Publication); err != nil {
		return err
	}
	if err := validateIdentifier("slot", cfg.Slot); err != nil {
		return err
	}

	// A normal connection is used for catalog queries (publication/slot state).
	conn, err := pgx.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(context.Background())

	if err := checkPublication(ctx, conn, cfg.Publication); err != nil {
		return err
	}

	// Logical replication uses a dedicated connection in replication mode.
	pgConfig, err := pgconn.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("parse database url: %w", err)
	}
	if pgConfig.RuntimeParams == nil {
		pgConfig.RuntimeParams = map[string]string{}
	}
	pgConfig.RuntimeParams["replication"] = "database"

	replicationConn, err := pgconn.ConnectConfig(ctx, pgConfig)
	if err != nil {
		return fmt.Errorf("connect replication: %w", err)
	}
	defer replicationConn.Close(context.Background())

	startLSN, err := ensureSlot(ctx, log, conn, replicationConn, cfg.Slot)
	if err != nil {
		return err
	}

	pluginArgs := []string{
		fmt.Sprintf("proto_version '%s'", publicationProto),
		fmt.Sprintf("publication_names '%s'", cfg.Publication),
	}

	err = pglogrepl.StartReplication(ctx, replicationConn, cfg.Slot, startLSN, pglogrepl.StartReplicationOptions{
		PluginArgs: pluginArgs,
	})
	if err != nil {
		return fmt.Errorf("start replication: %w", err)
	}

	log.Info().
		Str("slot", cfg.Slot).
		Str("publication", cfg.Publication).
		Str("lsn", startLSN.String()).
		Msg("replicating changes")

	r := &replicator{
		log:       log,
		typeMap:   pgtype.NewMap(),
		relations: map[uint32]*pglogrepl.RelationMessage{},
	}

	return r.stream(ctx, replicationConn, startLSN)
}

func checkPublication(ctx context.Context, conn *pgx.Conn, publication string) error {
	var exists bool
	err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_publication WHERE pubname = $1)`, publication).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check publication: %w", err)
	}
	if !exists {
		return fmt.Errorf("publication %q does not exist; run `make migrate-up`", publication)
	}

	return nil
}

// ensureSlot resumes from an existing slot or creates a persistent slot.
func ensureSlot(
	ctx context.Context,
	log zerolog.Logger,
	conn *pgx.Conn,
	replicationConn *pgconn.PgConn,
	slot string,
) (pglogrepl.LSN, error) {
	var confirmed pgtype.Text
	err := conn.QueryRow(ctx, `
		SELECT confirmed_flush_lsn::text
		FROM pg_replication_slots
		WHERE slot_name = $1`, slot).Scan(&confirmed)

	switch {
	case err == nil && confirmed.Valid:
		lsn, err := pglogrepl.ParseLSN(confirmed.String)
		if err != nil {
			return 0, fmt.Errorf("parse confirmed_flush_lsn: %w", err)
		}
		log.Info().
			Str("slot", slot).
			Str("lsn", lsn.String()).
			Msg("resuming from existing replication slot")
		return lsn, nil

	case err == nil:
		// The slot exists but has not acknowledged anything yet.
		var current string
		if err := conn.QueryRow(ctx, `SELECT pg_current_wal_lsn()::text`).Scan(&current); err != nil {
			return 0, fmt.Errorf("read current wal lsn: %w", err)
		}
		lsn, err := pglogrepl.ParseLSN(current)
		if err != nil {
			return 0, fmt.Errorf("parse current wal lsn: %w", err)
		}
		log.Info().
			Str("slot", slot).
			Str("lsn", lsn.String()).
			Msg("slot has no confirmed LSN; starting at current WAL position")
		return lsn, nil

	case errors.Is(err, pgx.ErrNoRows):
		result, err := pglogrepl.CreateReplicationSlot(
			ctx,
			replicationConn,
			slot,
			outputPlugin,
			pglogrepl.CreateReplicationSlotOptions{
				SnapshotAction: "NOEXPORT_SNAPSHOT",
			},
		)
		if err != nil {
			return 0, fmt.Errorf("create replication slot %q: %w", slot, err)
		}

		lsn, err := pglogrepl.ParseLSN(result.ConsistentPoint)
		if err != nil {
			return 0, fmt.Errorf("parse consistent point: %w", err)
		}

		log.Info().
			Str("slot", slot).
			Str("plugin", result.OutputPlugin).
			Str("consistent_point", result.ConsistentPoint).
			Msg("created replication slot")
		return lsn, nil

	default:
		return 0, fmt.Errorf("query replication slot: %w", err)
	}
}

type replicator struct {
	log       zerolog.Logger
	typeMap   *pgtype.Map
	relations map[uint32]*pglogrepl.RelationMessage
	xid       uint32
	commitAt  time.Time
}

func (r *replicator) stream(ctx context.Context, conn *pgconn.PgConn, startLSN pglogrepl.LSN) error {
	currentLSN := startLSN
	nextStandby := time.Now().Add(standbyInterval)

	for {
		if time.Now().After(nextStandby) {
			if err := sendStandby(ctx, conn, currentLSN); err != nil {
				return err
			}
			nextStandby = time.Now().Add(standbyInterval)
		}

		recvCtx, cancel := context.WithDeadline(ctx, nextStandby)
		rawMsg, err := conn.ReceiveMessage(recvCtx)
		cancel()

		if err != nil {
			if ctx.Err() != nil {
				// Acknowledge what we processed before shutting down.
				ackCtx, cancelAck := context.WithTimeout(context.Background(), 5*time.Second)
				_ = sendStandby(ackCtx, conn, currentLSN)
				cancelAck()
				r.log.Info().Msg("replicator shutting down")
				return nil
			}
			if pgconn.Timeout(err) {
				continue
			}
			return fmt.Errorf("receive message: %w", err)
		}

		switch msg := rawMsg.(type) {
		case *pgproto3.ErrorResponse:
			return fmt.Errorf("postgres: %s", msg.Message)

		case *pgproto3.CopyData:
			switch msg.Data[0] {
			case pglogrepl.PrimaryKeepaliveMessageByteID:
				keepalive, err := pglogrepl.ParsePrimaryKeepaliveMessage(msg.Data[1:])
				if err != nil {
					return fmt.Errorf("parse keepalive: %w", err)
				}
				if keepalive.ReplyRequested {
					nextStandby = time.Time{}
				}

			case pglogrepl.XLogDataByteID:
				xld, err := pglogrepl.ParseXLogData(msg.Data[1:])
				if err != nil {
					return fmt.Errorf("parse xlog data: %w", err)
				}

				committed, err := r.handle(xld.WALStart, xld.WALData)
				if err != nil {
					return err
				}

				currentLSN = xld.WALStart + pglogrepl.LSN(len(xld.WALData))

				// Acknowledge committed transactions immediately so the slot
				// does not replay them after a restart.
				if committed {
					if err := sendStandby(ctx, conn, currentLSN); err != nil {
						return err
					}
					nextStandby = time.Now().Add(standbyInterval)
				}
			}

		default:
			r.log.Debug().Str("type", fmt.Sprintf("%T", rawMsg)).Msg("ignoring message")
		}
	}
}

func sendStandby(ctx context.Context, conn *pgconn.PgConn, lsn pglogrepl.LSN) error {
	err := pglogrepl.SendStandbyStatusUpdate(ctx, conn, pglogrepl.StandbyStatusUpdate{
		WALWritePosition: lsn,
	})
	if err != nil {
		return fmt.Errorf("send standby status: %w", err)
	}

	return nil
}

func (r *replicator) handle(lsn pglogrepl.LSN, data []byte) (bool, error) {
	if len(data) == 0 {
		return false, nil
	}

	msg, err := pglogrepl.Parse(data)
	if err != nil {
		return false, fmt.Errorf("parse logical message: %w", err)
	}

	switch msg := msg.(type) {
	case *pglogrepl.RelationMessage:
		r.relations[msg.RelationID] = msg
		r.log.Debug().
			Str("table", msg.RelationName).
			Msg("relation metadata received")

	case *pglogrepl.BeginMessage:
		r.xid = msg.Xid
		r.commitAt = msg.CommitTime

	case *pglogrepl.CommitMessage:
		return true, nil

	case *pglogrepl.InsertMessage:
		return false, r.handleInsert(lsn, msg)

	case *pglogrepl.UpdateMessage:
		return false, r.handleUpdate(lsn, msg)

	case *pglogrepl.DeleteMessage:
		return false, r.handleDelete(lsn, msg)

	case *pglogrepl.TruncateMessage:
		r.log.Warn().
			Str("lsn", lsn.String()).
			Msg("truncate received; no per-row event to log")

	default:
		r.log.Debug().
			Str("type", msg.Type().String()).
			Msg("ignoring logical message")
	}

	return false, nil
}

func (r *replicator) handleInsert(lsn pglogrepl.LSN, msg *pglogrepl.InsertMessage) error {
	rel, err := r.relation(msg.RelationID)
	if err != nil {
		return err
	}

	row, err := r.decodeTuple(rel, msg.Tuple)
	if err != nil {
		return err
	}

	r.logChange(lsn, cdc.ActionInsert, rel, row, nil)
	return nil
}

func (r *replicator) handleUpdate(lsn pglogrepl.LSN, msg *pglogrepl.UpdateMessage) error {
	rel, err := r.relation(msg.RelationID)
	if err != nil {
		return err
	}

	row, err := r.decodeTuple(rel, msg.NewTuple)
	if err != nil {
		return err
	}

	var old map[string]any
	if msg.OldTuple != nil {
		old, err = r.decodeTuple(rel, msg.OldTuple)
		if err != nil {
			return err
		}
	}

	r.logChange(lsn, cdc.ActionUpdate, rel, row, old)
	return nil
}

func (r *replicator) handleDelete(lsn pglogrepl.LSN, msg *pglogrepl.DeleteMessage) error {
	rel, err := r.relation(msg.RelationID)
	if err != nil {
		return err
	}

	old, err := r.decodeTuple(rel, msg.OldTuple)
	if err != nil {
		return err
	}

	// Match the trigger payload: data is the deleted row, old is the same row.
	r.logChange(lsn, cdc.ActionDelete, rel, old, old)
	return nil
}

func (r *replicator) relation(id uint32) (*pglogrepl.RelationMessage, error) {
	rel, ok := r.relations[id]
	if !ok {
		return nil, fmt.Errorf("unknown relation id %d", id)
	}

	return rel, nil
}

func (r *replicator) decodeTuple(rel *pglogrepl.RelationMessage, tuple *pglogrepl.TupleData) (map[string]any, error) {
	if tuple == nil {
		return nil, errors.New("nil tuple")
	}

	row := make(map[string]any, len(tuple.Columns))

	for i, column := range tuple.Columns {
		if i >= len(rel.Columns) {
			return nil, fmt.Errorf("tuple has %d columns, relation %q has %d", len(tuple.Columns), rel.RelationName, len(rel.Columns))
		}

		columnInfo := rel.Columns[i]

		switch column.DataType {
		case pglogrepl.TupleDataTypeNull:
			row[columnInfo.Name] = nil

		case pglogrepl.TupleDataTypeToast:
			// Unchanged TOAST value; Postgres does not send the data.

		case pglogrepl.TupleDataTypeText:
			value, err := r.decodeValue(columnInfo.DataType, pgtype.TextFormatCode, column.Data)
			if err != nil {
				return nil, fmt.Errorf("decode column %q: %w", columnInfo.Name, err)
			}
			row[columnInfo.Name] = value

		case pglogrepl.TupleDataTypeBinary:
			value, err := r.decodeValue(columnInfo.DataType, pgtype.BinaryFormatCode, column.Data)
			if err != nil {
				return nil, fmt.Errorf("decode column %q: %w", columnInfo.Name, err)
			}
			row[columnInfo.Name] = value

		default:
			return nil, fmt.Errorf("column %q: unknown tuple data type %q", columnInfo.Name, column.DataType)
		}
	}

	return row, nil
}

func (r *replicator) decodeValue(oid uint32, format int16, data []byte) (any, error) {
	typ, ok := r.typeMap.TypeForOID(oid)
	if !ok {
		return string(data), nil
	}

	return typ.Codec.DecodeValue(r.typeMap, oid, format, data)
}

func (r *replicator) logChange(
	lsn pglogrepl.LSN,
	action cdc.Action,
	rel *pglogrepl.RelationMessage,
	row, old map[string]any,
) {
	data, err := json.Marshal(row)
	if err != nil {
		r.log.Error().Err(err).Msg("could not encode row")
		return
	}

	timestamp := r.commitAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	event := r.log.Info().
		Str("action", string(action)).
		Str("table", rel.RelationName).
		Int64("id", rowID(row)).
		RawJSON("row", data).
		Uint32("xid", r.xid).
		Str("lsn", lsn.String()).
		Time("timestamp", timestamp)

	if old != nil {
		oldData, err := json.Marshal(old)
		if err == nil {
			event = event.RawJSON("old", oldData)
		}
	}

	event.Msg("change received")
}

func rowID(row map[string]any) int64 {
	switch v := row["id"].(type) {
	case int64:
		return v
	case int32:
		return int64(v)
	case int16:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		id, _ := strconv.ParseInt(v, 10, 64)
		return id
	default:
		return 0
	}
}

func validateIdentifier(kind, name string) error {
	if !identifierPattern.MatchString(name) {
		return fmt.Errorf("invalid %s name %q: use lowercase letters, digits, and underscores", kind, name)
	}

	return nil
}
