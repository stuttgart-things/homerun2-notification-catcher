package catcher

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/notify"
)

// DefaultDeadLetterStream and DefaultDeadLetterMaxLen are the defaults of
// DEAD_LETTER_STREAM and DEAD_LETTER_MAXLEN (#68).
const (
	DefaultDeadLetterStream = "notification-catcher-dead-letter"
	DefaultDeadLetterMaxLen = 10000
)

// RedisDeadLetter parks sends that failed after their retries in a Redis
// stream, one entry per output and message, trimmed to about MaxLen entries.
// Each entry is self-contained: the message JSON travels with it, so it can
// be inspected or replayed after the pitcher's JSON document has expired.
type RedisDeadLetter struct {
	client redis.UniversalClient
	stream string
	maxLen int64
}

// DeadLetter returns a dead letter on the catcher's payload client.
func (c *RedisCatcher) DeadLetter(stream string, maxLen int64) *RedisDeadLetter {
	return &RedisDeadLetter{client: c.redisClient, stream: stream, maxLen: maxLen}
}

// Stream is the dead-letter stream's name.
func (d *RedisDeadLetter) Stream() string { return d.stream }

// Park appends e to the stream.
func (d *RedisDeadLetter) Park(ctx context.Context, e notify.DeadLetterEntry) error {
	body, err := json.Marshal(notify.WebhookPayload{Message: e.Message.Message, ObjectID: e.Message.ObjectID})
	if err != nil {
		return fmt.Errorf("dead-letter: marshal message: %w", err)
	}
	errText := ""
	if e.Err != nil {
		errText = e.Err.Error()
	}
	err = d.client.XAdd(ctx, &redis.XAddArgs{
		Stream: d.stream,
		MaxLen: d.maxLen,
		Approx: true,
		Values: map[string]any{
			"output":   e.Output,
			"objectId": e.Message.ObjectID,
			"streamId": e.Message.StreamID,
			"error":    errText,
			"attempts": strconv.Itoa(e.Attempts),
			"failedAt": e.At.Format(time.RFC3339Nano),
			"message":  string(body),
		},
	}).Err()
	if err != nil {
		return fmt.Errorf("dead-letter: XADD %s: %w", d.stream, err)
	}
	return nil
}
