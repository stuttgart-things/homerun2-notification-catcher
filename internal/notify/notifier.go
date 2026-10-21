// Package notify dispatches homerun.Message records to external notification
// channels (MS Teams, Slack, email, generic webhook). Each Notifier is a leaf
// sink; routing across multiple sinks belongs in a higher-level dispatcher
// (added in #7 / Phase 3).
package notify

import (
	"context"

	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
)

// Notifier dispatches a single caught message to one external channel. The
// message's ObjectID travels with it into the payload (#64), so a receiver
// (the sink, a log query) can find the post of one pitched message.
//
// Send must be safe for concurrent use. Implementations should treat ctx
// cancellation as a hard deadline (return as soon as the in-flight request
// finishes or is aborted).
type Notifier interface {
	Send(ctx context.Context, msg models.CaughtMessage) error
}
