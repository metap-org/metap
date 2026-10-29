package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// RedisCache is the distributed counterpart to MemoryCache, over any RESP-protocol server
// (Redis, DragonflyDB, Valkey, KeyDB are all wire-compatible) — ported from metap-cache's
// RedisCache.
type RedisCache struct {
	client *redis.Client
	ttl    time.Duration
}

// NewRedisCache connects to a standard redis:// URL.
func NewRedisCache(url string, ttl time.Duration) (*RedisCache, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(opts)
	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("connecting to redis at %s: %w", url, err)
	}
	return &RedisCache{client: client, ttl: ttl}, nil
}

func (c *RedisCache) scopedKey(tenantID uuid.UUID, key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	return fmt.Sprintf("metap-cache:%s:%s", tenantID, key), nil
}

func (c *RedisCache) Get(ctx context.Context, tenantID uuid.UUID, key string) ([]byte, bool, error) {
	scoped, err := c.scopedKey(tenantID, key)
	if err != nil {
		return nil, false, err
	}
	v, err := c.client.Get(ctx, scoped).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

func (c *RedisCache) Set(ctx context.Context, tenantID uuid.UUID, key string, value []byte) error {
	scoped, err := c.scopedKey(tenantID, key)
	if err != nil {
		return err
	}
	seconds := c.ttl
	if seconds < time.Second {
		seconds = time.Second
	}
	return c.client.Set(ctx, scoped, value, seconds).Err()
}

func (c *RedisCache) Invalidate(ctx context.Context, tenantID uuid.UUID, key string) error {
	scoped, err := c.scopedKey(tenantID, key)
	if err != nil {
		return err
	}
	return c.client.Del(ctx, scoped).Err()
}
