// Package notify dispatches homerun.Message records to external notification
// channels (MS Teams, Slack, email, generic webhook). Each Notifier is a leaf
// sink; routing across multiple sinks belongs in a higher-level dispatcher
// (added in #7 / Phase 3).
package notify

import (
	"context"

	homerun "github.com/stuttgart-things/homerun-library/v3"
)

// Notifier dispatches a single homerun.Message to one external channel.
//
// Send must be safe for concurrent use. Implementations should treat ctx
// cancellation as a hard deadline (return as soon as the in-flight request
// finishes or is aborted).
type Notifier interface {
	Send(ctx context.Context, msg homerun.Message) error
}
