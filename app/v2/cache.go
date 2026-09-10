package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	redisConnectTimeout = 2 * time.Second
	redisTimeout        = 2 * time.Second
)

type Cache struct {
	client *redis.Client
}

func NewCache(url string) (*Cache, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	opts.DialTimeout = redisConnectTimeout
	opts.ReadTimeout = redisTimeout
	opts.WriteTimeout = redisTimeout
	return &Cache{client: redis.NewClient(opts)}, nil
}

func (c *Cache) Get(ctx context.Context, key string) (string, bool) {
	val, err := c.client.Get(ctx, key).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return "", false
	}
	return val, err == nil
}

func (c *Cache) SetEx(ctx context.Context, key string, ttlSeconds int, value string) {
	c.client.SetEx(ctx, key, value, time.Duration(ttlSeconds)*time.Second)
}

func (c *Cache) Delete(ctx context.Context, key string) {
	c.client.Del(ctx, key)
}

func (c *Cache) Ping(ctx context.Context) bool {
	return c.client.Ping(ctx).Err() == nil
}

func (c *Cache) Close() error {
	return c.client.Close()
}
