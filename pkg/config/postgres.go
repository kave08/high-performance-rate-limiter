package config

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

func initializePostgreSql(config Postgres) (*SQLDatabase, error) {
	if config.DBName == "" {
		config = DefaultPostgersConfig()
	}

	db, err := sql.Open("postgres", fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		config.Host,
		config.Port,
		config.User,
		config.Password,
		config.DBName,
		config.SSLMode,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to open database connection: %w", err)
	}

	db.SetMaxOpenConns(config.MaxOpenConns)
	db.SetMaxIdleConns(config.MaxIdleConns)
	db.SetConnMaxLifetime(config.ConnMaxLifetime)
	db.SetConnMaxIdleTime(config.ConnMaxIdleTime)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &SQLDatabase{
		DB:             db,
		ConnectionName: fmt.Sprintf("%s:%d", config.Host, config.Port),
		DatabaseName:   config.DBName,
	}, nil
}

func DefaultPostgersConfig() Postgres {
	return Postgres{
		Host: "localhost",
		Port: 5432,
		User: "postgres",
	}
}
