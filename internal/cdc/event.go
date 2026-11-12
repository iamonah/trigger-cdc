// Package cdc defines the change event published by the users trigger.
package cdc

import (
	"encoding/json"
	"time"
)

// Action is the kind of row change captured by the trigger.
type Action string

const (
	// ActionInsert is emitted for a new row.
	ActionInsert Action = "INSERT"
	// ActionUpdate is emitted when a row changes.
	ActionUpdate Action = "UPDATE"
	// ActionDelete is emitted when a row is removed.
	ActionDelete Action = "DELETE"
)

// Change mirrors the JSON payload published by notify_users_changes().
type Change struct {
	Action    Action          `json:"action"`
	Table     string          `json:"table"`
	ID        int64           `json:"id"`
	Data      json.RawMessage `json:"data"`
	Old       json.RawMessage `json:"old,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
}
