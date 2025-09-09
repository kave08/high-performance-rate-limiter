package config

import (
	"context"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
)

func initializeRedisConnection(config Redis) (*redis.Client, error) {
	if config.Addr == "" {
		config = DefaultConfig()
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:         config.Addr,
		Password:     config.Password,
		DB:           config.DB,
		MaxRetries:   config.MaxRetries,
		DialTimeout:  config.DialTimeout,
		ReadTimeout:  config.ReadTimeout,
		WriteTimeout: config.WriteTimeout,
		PoolSize:     config.PoolSize,
		MinIdleConns: config.MinIdleConns,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return rdb, nil
}

func DefaultConfig() Redis {
	return Redis{
		Addr:         "localhost:6379",
		Password:     "",
		DB:           0,
		MaxRetries:   3,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolSize:     10,
		MinIdleConns: 2,
	}
}
