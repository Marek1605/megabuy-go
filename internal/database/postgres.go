package database

import (
	"context"
	"fmt"
	"os"
	"time"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	Pool *pgxpool.Pool
}

func New() (*DB, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return nil, fmt.Errorf("DATABASE_URL required")
	}
	fmt.Println("Connecting to:", dbURL[:50]+"...")
	
	config, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, fmt.Errorf("parse config error: %w", err)
	}
	config.MaxConns = 10
	
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("pool create error: %w", err)
	}
	
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping error: %w", err)
	}
	
	fmt.Println("Connected to PostgreSQL")
	return &DB{Pool: pool}, nil
}

func (db *DB) Close() { 
	if db.Pool != nil {
		db.Pool.Close() 
	}
}

func (db *DB) RunMigrations(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err = db.Pool.Exec(ctx, string(content))
	if err != nil {
		return fmt.Errorf("exec migration: %w", err)
	}
	fmt.Println("Migrations done")
	return nil
}
