package models

import (
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"
)

// CaughtMessage wraps a homerun.Message with stream metadata. Carrying ObjectID
// and StreamID lets handlers (logger, notifier) trace each dispatched message
// back to its origin in Redis.
type CaughtMessage struct {
	homerun.Message
	ObjectID string    `json:"objectId"`
	StreamID string    `json:"streamId"`
	CaughtAt time.Time `json:"caughtAt"`
}
