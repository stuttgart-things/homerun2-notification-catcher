package catcher

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	homerun "github.com/stuttgart-things/homerun-library/v4"
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

// NewRedisDeadLetter opens the dead-letter stream on its own client, for
// reading it outside the catcher (`notification-catcher replay`, #70).
// Close it when done.
func NewRedisDeadLetter(rc homerun.RedisConfig, stream string) *RedisDeadLetter {
	client := redis.NewClient(&redis.Options{Addr: fmt.Sprintf("%s:%s", rc.Addr, rc.Port), Password: rc.Password})
	return &RedisDeadLetter{client: client, stream: stream, maxLen: DefaultDeadLetterMaxLen}
}

// Close closes the client. Only for a dead letter from NewRedisDeadLetter:
// the catcher's own closes with the catcher.
func (d *RedisDeadLetter) Close() error { return d.client.Close() }

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

// Range returns the parked entries from since on (all when zero), oldest
// first: the order to replay them in.
func (d *RedisDeadLetter) Range(ctx context.Context, since time.Time) ([]notify.ParkedEntry, error) {
	start := "-"
	if !since.IsZero() {
		start = strconv.FormatInt(since.UnixMilli(), 10)
	}
	msgs, err := d.client.XRange(ctx, d.stream, start, "+").Result()
	if err != nil {
		return nil, fmt.Errorf("dead-letter: XRANGE %s: %w", d.stream, err)
	}
	return parseParked(msgs), nil
}

// Recent returns the last n parked entries, newest first.
func (d *RedisDeadLetter) Recent(ctx context.Context, n int64) ([]notify.ParkedEntry, error) {
	msgs, err := d.client.XRevRangeN(ctx, d.stream, "+", "-", n).Result()
	if err != nil {
		return nil, fmt.Errorf("dead-letter: XREVRANGE %s: %w", d.stream, err)
	}
	return parseParked(msgs), nil
}

// Len is the number of parked entries.
func (d *RedisDeadLetter) Len(ctx context.Context) (int64, error) {
	n, err := d.client.XLen(ctx, d.stream).Result()
	if err != nil {
		return 0, fmt.Errorf("dead-letter: XLEN %s: %w", d.stream, err)
	}
	return n, nil
}

// Delete removes the entry id, once its replay went through.
func (d *RedisDeadLetter) Delete(ctx context.Context, id string) error {
	if err := d.client.XDel(ctx, d.stream, id).Err(); err != nil {
		return fmt.Errorf("dead-letter: XDEL %s %s: %w", d.stream, id, err)
	}
	return nil
}

// parseParked reads entries as Park writes them. A field that does not
// parse is left empty rather than dropping the entry: what is there is
// still worth showing.
func parseParked(msgs []redis.XMessage) []notify.ParkedEntry {
	out := make([]notify.ParkedEntry, 0, len(msgs))
	for _, m := range msgs {
		str := func(k string) string { s, _ := m.Values[k].(string); return s }
		e := notify.ParkedEntry{
			ID:       m.ID,
			Output:   str("output"),
			ObjectID: str("objectId"),
			StreamID: str("streamId"),
			Error:    str("error"),
		}
		e.Attempts, _ = strconv.Atoi(str("attempts"))
		e.FailedAt, _ = time.Parse(time.RFC3339Nano, str("failedAt"))
		var p notify.WebhookPayload
		if json.Unmarshal([]byte(str("message")), &p) == nil {
			e.Message = p.Message
			if e.ObjectID == "" {
				e.ObjectID = p.ObjectID
			}
		}
		out = append(out, e)
	}
	return out
}
