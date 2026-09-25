package catcher

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"time"

	"github.com/redis/go-redis/v9"
	homerun "github.com/stuttgart-things/homerun-library/v4"
	"github.com/stuttgart-things/homerun2-notification-catcher/internal/models"
	"github.com/stuttgart-things/redisqueue"
)

// MessageHandler processes a caught message.
type MessageHandler func(msg models.CaughtMessage)

// Catcher defines the interface for message consumption backends.
type Catcher interface {
	Run()
	Shutdown()
	Errors() <-chan error
}

// RedisCatcher consumes messages from one or more Redis Streams and resolves
// full payloads from Redis JSON.
type RedisCatcher struct {
	consumer    *redisqueue.Consumer
	redisClient redis.UniversalClient
	streams     []string
	handlers    []MessageHandler
}

// DefaultStartID is where a consumer group created by the catcher starts
// reading: "$" delivers only messages pitched after the group exists. A
// notification is about now: replaying a stream's backlog would post every
// old alert or result to a real channel again, so it is never the default.
const DefaultStartID = "$"

// blockingTimeout bounds each XREADGROUP call, and so how long Shutdown waits
// for the poller; tests shorten it.
var blockingTimeout = 5 * time.Second

var startIDPattern = regexp.MustCompile(`^(\$|\d+(-\d+)?)$`)

// NewRedisCatcher creates a consumer connected to the given Redis streams.
// If streams is empty, it falls back to rc.Stream (legacy single-stream mode).
//
// startID only applies to consumer groups that do not exist yet: "$" (the
// default when empty) starts after the current end of the stream, "0" replays
// the whole stream, and any other stream ID starts after that entry. An
// existing group keeps its position, so messages pitched while the catcher
// was down are still delivered.
func NewRedisCatcher(
	rc homerun.RedisConfig,
	streams []string,
	groupName, consumerName, startID string,
	handlers ...MessageHandler,
) (*RedisCatcher, error) {
	addr := fmt.Sprintf("%s:%s", rc.Addr, rc.Port)
	options := &redis.Options{Addr: addr, Password: rc.Password}

	return newRedisCatcher(redis.NewClient(options), redis.NewClient(options),
		rc.Stream, streams, groupName, consumerName, startID, handlers...)
}

// newRedisCatcher wires a catcher to the given clients: consumerClient drives
// the stream consumer, payloadClient resolves message payloads.
func newRedisCatcher(
	consumerClient, payloadClient redis.UniversalClient,
	legacyStream string,
	streams []string,
	groupName, consumerName, startID string,
	handlers ...MessageHandler,
) (*RedisCatcher, error) {
	closeClients := func() {
		_ = consumerClient.Close()
		if payloadClient != consumerClient {
			_ = payloadClient.Close()
		}
	}

	if len(streams) == 0 {
		if legacyStream == "" {
			closeClients()
			return nil, fmt.Errorf("no streams configured: pass streams or set rc.Stream")
		}
		streams = []string{legacyStream}
	}

	if startID == "" {
		startID = DefaultStartID
	}
	if !startIDPattern.MatchString(startID) {
		closeClients()
		return nil, fmt.Errorf("invalid consumer start ID %q: use \"$\", \"0\" or a stream ID", startID)
	}

	if consumerName == "" {
		hostname, _ := os.Hostname()
		consumerName = hostname
	}

	consumer, err := redisqueue.NewConsumerWithOptions(&redisqueue.ConsumerOptions{
		Name:            consumerName,
		GroupName:       groupName,
		BlockingTimeout: blockingTimeout,
		BufferSize:      100,
		// One worker handles a stream's messages in stream order. A burst
		// (two alerts, or a point and the match-winning point, pitched
		// milliseconds apart) must reach the channel in the order it
		// happened; ten workers could post them the other way round.
		Concurrency: 1,
		RedisClient: consumerClient,
	})
	if err != nil {
		closeClients()
		return nil, fmt.Errorf("failed to create redis consumer: %w", err)
	}

	c := &RedisCatcher{
		consumer:    consumer,
		redisClient: payloadClient,
		streams:     streams,
		handlers:    handlers,
	}

	for _, stream := range streams {
		consumer.RegisterWithLastID(stream, startID, c.handleMessage)
	}

	return c, nil
}

// Streams returns the list of streams this catcher is subscribed to.
func (c *RedisCatcher) Streams() []string {
	return c.streams
}

// Run starts the consumer. Blocks until Shutdown is called.
func (c *RedisCatcher) Run() {
	c.consumer.Run()
}

// Shutdown gracefully stops the consumer and closes the Redis client.
func (c *RedisCatcher) Shutdown() {
	c.consumer.Shutdown()
	if c.redisClient != nil {
		_ = c.redisClient.Close()
	}
}

// Errors returns the consumer's error channel.
func (c *RedisCatcher) Errors() <-chan error {
	return c.consumer.Errors
}

func (c *RedisCatcher) handleMessage(msg *redisqueue.Message) error {
	messageID, ok := msg.Values["messageID"]
	if !ok {
		slog.Warn("stream entry missing messageID field", "id", msg.ID)
		return nil
	}

	messageIDStr, ok := messageID.(string)
	if !ok {
		slog.Warn("messageID is not a string", "id", msg.ID, "messageID", messageID)
		return nil
	}

	payload, err := c.resolveMessage(messageIDStr)
	if err != nil {
		slog.Warn("failed to resolve message from Redis JSON",
			"messageID", messageIDStr,
			"error", err,
		)
		return nil
	}

	caught := models.CaughtMessage{
		Message:  *payload,
		ObjectID: messageIDStr,
		StreamID: msg.ID,
		CaughtAt: time.Now(),
	}

	for _, h := range c.handlers {
		h(caught)
	}

	return nil
}

func (c *RedisCatcher) resolveMessage(messageID string) (*homerun.Message, error) {
	ctx := context.Background()

	result, err := c.redisClient.Do(ctx, "JSON.GET", messageID, ".").Text()
	if err != nil {
		return nil, fmt.Errorf("JSON.GET %s: %w", messageID, err)
	}

	var msg homerun.Message
	if err := json.Unmarshal([]byte(result), &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}

	return &msg, nil
}
