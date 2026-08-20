package catcher

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
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
	redisClient *redis.Client
	streams     []string
	handlers    []MessageHandler
}

// NewRedisCatcher creates a consumer connected to the given Redis streams.
func NewRedisCatcher(rc homerun.RedisConfig, streams []string, groupName, consumerName string, handlers ...MessageHandler) (*RedisCatcher, error) {
	if len(streams) == 0 {
		if rc.Stream == "" {
			return nil, fmt.Errorf("no streams configured: pass streams or set rc.Stream")
		}
		streams = []string{rc.Stream}
	}

	if consumerName == "" {
		hostname, _ := os.Hostname()
		consumerName = hostname
	}

	addr := fmt.Sprintf("%s:%s", rc.Addr, rc.Port)

	consumer, err := redisqueue.NewConsumerWithOptions(&redisqueue.ConsumerOptions{
		Name:        consumerName,
		GroupName:   groupName,
		BufferSize:  100,
		Concurrency: 10,
		RedisOptions: &redisqueue.RedisOptions{
			Addr:     addr,
			Password: rc.Password,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create redis consumer: %w", err)
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: rc.Password,
	})

	c := &RedisCatcher{
		consumer:    consumer,
		redisClient: redisClient,
		streams:     streams,
		handlers:    handlers,
	}

	for _, stream := range streams {
		consumer.Register(stream, c.handleMessage)
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
