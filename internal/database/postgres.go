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
	config, _ := pgxpool.ParseConfig(dbURL)
	config.MaxConns = 10
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, err
	}
	fmt.Println("Connected to PostgreSQL")
	return &DB{Pool: pool}, nil
}

func (db *DB) Close() { db.Pool.Close() }

func (db *DB) RunMigrations(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err = db.Pool.Exec(ctx, string(content))
	fmt.Println("Migrations done")
	return err
}
