-- Database initialization script for rate limiter configuration

-- Create database if it doesn't exist
-- (This script assumes the database already exists from docker-compose)

-- User limits table for persistent configuration
CREATE TABLE IF NOT EXISTS user_limits (
    id SERIAL PRIMARY KEY,
    user_id VARCHAR(255) NOT NULL UNIQUE,
    limit_value INTEGER NOT NULL DEFAULT 100,
    tier VARCHAR(50) DEFAULT 'basic',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    expires_at TIMESTAMP WITH TIME ZONE NULL,
    
    CONSTRAINT positive_limit CHECK (limit_value > 0)
);

-- Index for fast user lookup
CREATE INDEX IF NOT EXISTS idx_user_limits_user_id ON user_limits(user_id);
CREATE INDEX IF NOT EXISTS idx_user_limits_tier ON user_limits(tier);
CREATE INDEX IF NOT EXISTS idx_user_limits_expires_at ON user_limits(expires_at);

-- Rate limit tiers configuration
CREATE TABLE IF NOT EXISTS rate_limit_tiers (
    id SERIAL PRIMARY KEY,
    tier_name VARCHAR(50) NOT NULL UNIQUE,
    default_limit INTEGER NOT NULL,
    description TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    
    CONSTRAINT positive_tier_limit CHECK (default_limit > 0)
);

-- Insert default tiers
INSERT INTO rate_limit_tiers (tier_name, default_limit, description) VALUES
    ('basic', 100, 'Basic tier with 100 requests per second'),
    ('premium', 500, 'Premium tier with 500 requests per second'),
    ('enterprise', 2000, 'Enterprise tier with 2000 requests per second'),
    ('unlimited', 10000, 'Unlimited tier with very high limits')
ON CONFLICT (tier_name) DO NOTHING;

-- Rate limit history for analytics (optional)
CREATE TABLE IF NOT EXISTS rate_limit_history (
    id SERIAL PRIMARY KEY,
    user_id VARCHAR(255) NOT NULL,
    requested_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    limit_value INTEGER NOT NULL,
    allowed BOOLEAN NOT NULL,
    algorithm VARCHAR(50) DEFAULT 'sliding_window',
    response_time_ms INTEGER,
    
    -- Partition by date for better performance
    CONSTRAINT valid_algorithm CHECK (algorithm IN ('sliding_window', 'leaky_bucket', 'token_bucket'))
);

-- Indexes for analytics queries
CREATE INDEX IF NOT EXISTS idx_rate_limit_history_user_id ON rate_limit_history(user_id);
CREATE INDEX IF NOT EXISTS idx_rate_limit_history_requested_at ON rate_limit_history(requested_at);
CREATE INDEX IF NOT EXISTS idx_rate_limit_history_allowed ON rate_limit_history(allowed);

-- Function to update the updated_at timestamp
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ language 'plpgsql';

-- Trigger to automatically update updated_at
CREATE TRIGGER update_user_limits_updated_at 
    BEFORE UPDATE ON user_limits 
    FOR EACH ROW 
    EXECUTE FUNCTION update_updated_at_column();

-- Insert some example user configurations
INSERT INTO user_limits (user_id, limit_value, tier) VALUES
    ('demo_user', 50, 'basic'),
    ('premium_user', 500, 'premium'),
    ('enterprise_user', 2000, 'enterprise'),
    ('test_user', 10, 'basic')
ON CONFLICT (user_id) DO NOTHING;

-- View for current active limits (excluding expired ones)
CREATE OR REPLACE VIEW active_user_limits AS
SELECT 
    ul.user_id,
    ul.limit_value,
    ul.tier,
    ul.created_at,
    ul.updated_at,
    ul.expires_at,
    rt.description as tier_description
FROM user_limits ul
LEFT JOIN rate_limit_tiers rt ON ul.tier = rt.tier_name
WHERE ul.expires_at IS NULL OR ul.expires_at > NOW();

-- Function to get user limit with fallback to tier default
CREATE OR REPLACE FUNCTION get_user_limit(p_user_id VARCHAR(255), p_default_tier VARCHAR(50) DEFAULT 'basic')
RETURNS INTEGER AS $$
DECLARE
    user_limit INTEGER;
    tier_limit INTEGER;
BEGIN
    -- Try to get user-specific limit first
    SELECT limit_value INTO user_limit
    FROM user_limits 
    WHERE user_id = p_user_id 
    AND (expires_at IS NULL OR expires_at > NOW());
    
    IF user_limit IS NOT NULL THEN
        RETURN user_limit;
    END IF;
    
    -- Fallback to tier default
    SELECT default_limit INTO tier_limit
    FROM rate_limit_tiers 
    WHERE tier_name = p_default_tier;
    
    RETURN COALESCE(tier_limit, 100); -- Final fallback
END;
$$ LANGUAGE plpgsql;

-- Function to record rate limit events (for analytics)
CREATE OR REPLACE FUNCTION record_rate_limit_event(
    p_user_id VARCHAR(255),
    p_limit_value INTEGER,
    p_allowed BOOLEAN,
    p_algorithm VARCHAR(50) DEFAULT 'sliding_window',
    p_response_time_ms INTEGER DEFAULT NULL
) RETURNS VOID AS $$
BEGIN
    INSERT INTO rate_limit_history (user_id, limit_value, allowed, algorithm, response_time_ms)
    VALUES (p_user_id, p_limit_value, p_allowed, p_algorithm, p_response_time_ms);
END;
$$ LANGUAGE plpgsql;

-- Cleanup function for old history records (run periodically)
CREATE OR REPLACE FUNCTION cleanup_old_history(days_to_keep INTEGER DEFAULT 30)
RETURNS INTEGER AS $$
DECLARE
    deleted_count INTEGER;
BEGIN
    DELETE FROM rate_limit_history 
    WHERE requested_at < NOW() - INTERVAL '1 day' * days_to_keep;
    
    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    RETURN deleted_count;
END;
$$ LANGUAGE plpgsql;

-- Grant permissions (adjust as needed for your security requirements)
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO rate_limiter;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO rate_limiter;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO rate_limiter;

-- Example queries for monitoring and analytics:

-- Most active users in the last hour
-- SELECT user_id, COUNT(*) as requests, 
--        COUNT(*) FILTER (WHERE allowed = true) as allowed_requests,
--        COUNT(*) FILTER (WHERE allowed = false) as denied_requests
-- FROM rate_limit_history 
-- WHERE requested_at > NOW() - INTERVAL '1 hour'
-- GROUP BY user_id 
-- ORDER BY requests DESC 
-- LIMIT 10;

-- Rate limit effectiveness by tier
-- SELECT rt.tier_name, 
--        COUNT(*) as total_requests,
--        COUNT(*) FILTER (WHERE allowed = true) as allowed,
--        COUNT(*) FILTER (WHERE allowed = false) as denied,
--        ROUND(100.0 * COUNT(*) FILTER (WHERE allowed = false) / COUNT(*), 2) as denial_rate_percent
-- FROM rate_limit_history rlh
-- JOIN user_limits ul ON rlh.user_id = ul.user_id
-- JOIN rate_limit_tiers rt ON ul.tier = rt.tier_name
-- WHERE rlh.requested_at > NOW() - INTERVAL '1 day'
-- GROUP BY rt.tier_name
-- ORDER BY total_requests DESC;