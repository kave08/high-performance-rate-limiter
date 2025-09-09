package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/kave08/high-performance-rate-limiter/pkg/contract"
	"github.com/kave08/high-performance-rate-limiter/pkg/repository"
	"go.uber.org/zap"
)

const (
	// CacheKeyPrefix is the prefix for all cache keys
	CacheKeyPrefix = "user_limit:"
)

// Manager handles dynamic configuration for rate limits
type Manager struct {
	redis         *redis.Client
	localCache    contract.CacheInterface
	logger        *zap.Logger
	defaultLimits map[string]int
	refreshPeriod time.Duration
	pubsub        *redis.PubSub
	subscribers   []chan contract.ConfigUpdate
	mu            sync.RWMutex
	stopCh        chan struct{}
	wg            sync.WaitGroup
	userRepo      contract.UserRepository
}

// NewManager creates a new configuration manager
func NewManager(redisClient *redis.Client, logger *zap.Logger, userRepo contract.UserRepository) *Manager {
	return &Manager{
		redis:         redisClient,
		localCache:    repository.NewCache(1000, time.Minute*5),
		logger:        logger,
		defaultLimits: make(map[string]int),
		refreshPeriod: time.Minute * 5,
		stopCh:        make(chan struct{}),
		userRepo:      userRepo,
	}
}

// Start begins the configuration manager background processes
func (m *Manager) Start(ctx context.Context) error {
	m.pubsub = m.redis.Subscribe(ctx, "rate_limit_config_updates")

	m.wg.Add(1)
	go m.configUpdateListener(ctx)

	m.wg.Add(1)
	go m.periodicRefresh(ctx)

	m.logger.Info("Config manager started")
	return nil
}

// Stop gracefully shuts down the configuration manager
func (m *Manager) Stop() {
	close(m.stopCh)

	if m.pubsub != nil {
		m.pubsub.Close()
	}

	m.wg.Wait()
	m.logger.Info("Config manager stopped")
}

// GetUserLimit retrieves the rate limit for a specific user
func (m *Manager) GetUserLimit(ctx context.Context, userID string) (int, error) {
	if limit, found := m.localCache.Get(userID); found {
		return limit, nil
	}

	key := CacheKeyPrefix + userID
	limitStr, err := m.redis.Get(ctx, key).Result()
	if err == nil {
		limit, parseErr := strconv.Atoi(limitStr)
		if parseErr == nil {
			m.localCache.Set(userID, limit, time.Minute*5)
			return limit, nil
		}
	}

	limit, err := m.userRepo.GetUserLimitFromDB(ctx, userID)
	if err == nil {
		m.localCache.Set(userID, limit, time.Minute*10)
		return limit, nil
	}

	return m.getDefaultLimit(userID), nil
}

// UpdateUserLimit updates a user's rate limit
func (m *Manager) UpdateUserLimit(ctx context.Context, userID string, newLimit int, ttl time.Duration) error {
	key := CacheKeyPrefix + userID
	err := m.redis.Set(ctx, key, newLimit, ttl).Err()
	if err != nil {
		return fmt.Errorf("failed to update Redis: %w", err)
	}

	m.localCache.Delete(userID)

	update := contract.ConfigUpdate{
		UserID: userID,
		Limit:  newLimit,
		Action: "update",
	}
	if err := m.publishUpdate(ctx, update); err != nil {
		m.logger.Error("Failed to publish config update", zap.Error(err))
	}

	go m.userRepo.UpdateDatabase(userID, newLimit)

	return nil
}

// DeleteUserLimit removes a user's custom rate limit
func (m *Manager) DeleteUserLimit(ctx context.Context, userID string) error {
	key := CacheKeyPrefix + userID
	err := m.redis.Del(ctx, key).Err()
	if err != nil {
		return fmt.Errorf("failed to delete from Redis: %w", err)
	}

	m.localCache.Delete(userID)

	update := contract.ConfigUpdate{
		UserID: userID,
		Action: "delete",
	}
	if err := m.publishUpdate(ctx, update); err != nil {
		m.logger.Error("Failed to publish config delete", zap.Error(err))
	}

	return nil
}

// SetDefaultLimit sets a default limit for a user tier
func (m *Manager) SetDefaultLimit(tier string, limit int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultLimits[tier] = limit
}

// Subscribe allows components to listen for configuration updates
func (m *Manager) Subscribe() <-chan contract.ConfigUpdate {
	m.mu.Lock()
	defer m.mu.Unlock()

	ch := make(chan contract.ConfigUpdate, 10)
	m.subscribers = append(m.subscribers, ch)
	return ch
}

// RefreshAllConfigs loads all configurations from database to Redis
func (m *Manager) RefreshAllConfigs(ctx context.Context) error {
	m.logger.Info("Refreshing all configurations from database")

	configs, err := m.userRepo.GetAllActiveUserLimits(ctx)
	if err != nil {
		return fmt.Errorf("failed to get active user limits: %w", err)
	}

	pipe := m.redis.Pipeline()
	count := 0

	for _, config := range configs {
		key := CacheKeyPrefix + config.UserID
		pipe.Set(ctx, key, config.Limit, time.Hour*24)
		count++

		// Execute in batches of 1000
		if count%1000 == 0 {
			if _, err := pipe.Exec(ctx); err != nil {
				m.logger.Error("Failed to execute pipeline", zap.Error(err))
			}
			pipe = m.redis.Pipeline()
		}
	}

	// Execute final batch
	if count%1000 != 0 {
		if _, err := pipe.Exec(ctx); err != nil {
			m.logger.Error("Failed to execute final pipeline", zap.Error(err))
		}
	}

	m.logger.Info("Configuration refresh completed", zap.Int("count", count))
	return nil
}

// getDefaultLimit returns the default limit for a user
func (m *Manager) getDefaultLimit(userID string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// In a real implementation, you might determine user tier from userID
	// For now, return global default
	if limit, exists := m.defaultLimits["default"]; exists {
		return limit
	}
	return 100 // Hardcoded fallback
}

// configUpdateListener listens for configuration update notifications
func (m *Manager) configUpdateListener(ctx context.Context) {
	defer m.wg.Done()

	ch := m.pubsub.Channel()

	for {
		select {
		case msg := <-ch:
			if msg == nil {
				continue
			}

			var update contract.ConfigUpdate
			if err := json.Unmarshal([]byte(msg.Payload), &update); err != nil {
				m.logger.Error("Failed to unmarshal config update", zap.Error(err))
				continue
			}

			m.handleConfigUpdate(update)

		case <-m.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// handleConfigUpdate processes a configuration update
func (m *Manager) handleConfigUpdate(update contract.ConfigUpdate) {
	// Invalidate local cache
	m.localCache.Delete(update.UserID)

	// Optionally update local cache with new value
	if update.Action == "update" && update.Limit > 0 {
		m.localCache.Set(update.UserID, update.Limit, time.Minute*5)
	}

	// Notify subscribers
	m.mu.RLock()
	for _, subscriber := range m.subscribers {
		select {
		case subscriber <- update:
		default:
			// Non-blocking send to prevent slow consumers from blocking
		}
	}
	m.mu.RUnlock()

	m.logger.Debug("Processed config update",
		zap.String("user_id", update.UserID),
		zap.Int("limit", update.Limit),
		zap.String("action", update.Action))
}

// periodicRefresh periodically refreshes configurations
func (m *Manager) periodicRefresh(ctx context.Context) {
	defer m.wg.Done()

	ticker := time.NewTicker(m.refreshPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := m.RefreshAllConfigs(ctx); err != nil {
				m.logger.Error("Periodic config refresh failed", zap.Error(err))
			}
		case <-m.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// publishUpdate publishes a configuration update to all instances
func (m *Manager) publishUpdate(ctx context.Context, update contract.ConfigUpdate) error {
	payload, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("failed to marshal update: %w", err)
	}

	return m.redis.Publish(ctx, "rate_limit_config_updates", payload).Err()
}
