package cdc

import (
	"encoding/json"
	"time"
)

// Action is the kind of row change captured by the trigger.
type Action string

const (
	ActionInsert Action = "INSERT"
	ActionUpdate Action = "UPDATE"
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
