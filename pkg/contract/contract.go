package contract

import (
	"context"
	"database/sql"
	"time"
)

type RateLimiter interface{}

type QueryRunner interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type CacheInterface interface {
	Get(key string) (int, bool)
	Set(key string, value int, ttl time.Duration)
	Delete(key string)
	Size() int
	Clear()
	Stop()
	GetStats() CacheStats
}

type CacheStats struct {
	Size    int `json:"size"`
	Active  int `json:"active"`
	Expired int `json:"expired"`
	MaxSize int `json:"max_size"`
}

type UserLimitConfig struct {
	UserID    string    `json:"user_id"`
	Limit     int       `json:"limit"`
	Tier      string    `json:"tier"`
	ExpiresAt time.Time `json:"expires_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type UserRepository interface {
	GetUserLimitFromDB(ctx context.Context, userID string) (int, error)
	GetAllActiveUserLimits(ctx context.Context) ([]UserLimitConfig, error)
	UpdateDatabase(userID string, newLimit int)
}

type ConfigUpdate struct {
	UserID string `json:"user_id"`
	Limit  int    `json:"limit"`
	Action string `json:"action"`
}
