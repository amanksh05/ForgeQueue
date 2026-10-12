package store

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func NewPool (ctx context.Context) (*pgxpool.Pool, error){
	databaseUrl := os.Getenv("DATABASE_URL")
	if databaseUrl == ""{
		return nil, fmt.Errorf("DATABASE_URL string not set!!")
	}

	config, err := pgxpool.ParseConfig(databaseUrl)

	if err != nil{
		fmt.Errorf("parse database config: %w", err)
	}
	
	config.MaxConns = 10
	config.MinConns = 1

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil{
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return pool, nil
}