package repository

import (
	"context"
	"time"

	"github.com/kave08/high-performance-rate-limiter/pkg/config"
	"github.com/kave08/high-performance-rate-limiter/pkg/contract"
	"go.uber.org/zap"
)

const (
	GetUserLimit = `SELECT user_id, limit, tier FROM user_limits WHERE user_id = ? AND (expires_at > NOW() OR expires_at IS NULL);`

	GetAllActiveUserLimits = `
		SELECT user_id, limit, tier
		FROM user_limits
		WHERE expires_at > NOW() OR expires_at IS NULL
	`

	UpdateUserLimit = `
		INSERT INTO user_limits (user_id, limit, updated_at)
		VALUES (?, ?, NOW())
		ON DUPLICATE KEY UPDATE
		limit = VALUES(limit), updated_at = VALUES(updated_at)
	`
)

// UserRepository will hold everything that repo needs
type UserRepository struct {
	db  contract.QueryRunner
	log *zap.SugaredLogger
}

// NewUserRepository makes new instance of UserRepository
func NewUserRepository(db config.SQLDatabase) *UserRepository {
	return &UserRepository{
		db:  db,
		log: zap.S().With("component", "UserRepository"),
	}
}

// GetUserLimitFromDB retrieves user limit from database
func (u *UserRepository) GetUserLimitFromDB(ctx context.Context, userID string) (int, error) {
	var config contract.UserLimitConfig

	err := u.db.QueryRowContext(ctx, GetUserLimit, userID).Scan(
		&config.UserID, &config.Limit, &config.Tier,
	)

	if err != nil {
		return 0, err
	}

	return config.Limit, nil
}

// GetAllActiveUserLimits retrieves all active user limits from database
func (u *UserRepository) GetAllActiveUserLimits(ctx context.Context) ([]contract.UserLimitConfig, error) {
	rows, err := u.db.QueryContext(ctx, GetAllActiveUserLimits)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []contract.UserLimitConfig
	for rows.Next() {
		var config contract.UserLimitConfig
		if err := rows.Scan(&config.UserID, &config.Limit, &config.Tier); err != nil {
			u.log.Errorw("Failed to scan row", "error", err)
			continue
		}
		configs = append(configs, config)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return configs, nil
}

// UpdateDatabase updates the database with new configuration (async)
func (u *UserRepository) UpdateDatabase(userID string, newLimit int) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	_, err := u.db.ExecContext(ctx, UpdateUserLimit, userID, newLimit)
	if err != nil {
		u.log.Errorw("Failed to update database",
			"user_id", userID,
			"limit", newLimit,
			"error", err)
	}
}
