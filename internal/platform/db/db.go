// Package db opens the single Postgres connection shared by every module.
package db

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/uptrace/opentelemetry-go-extra/otelgorm"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func Open(dsn string) (*gorm.DB, error) {
	pgxConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	// Simple protocol avoids "prepared statement already exists" (42P05)
	// with GORM Preload + FindInBatches on shared connections.
	pgxConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	sqlDB := stdlib.OpenDB(*pgxConfig)
	// Small pool: one VPS, one small Postgres container
	sqlDB.SetMaxOpenConns(15)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)

	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		Logger: gormlogger.New(log.New(os.Stderr, "", log.LstdFlags), gormlogger.Config{
			SlowThreshold:             500 * time.Millisecond,
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: true, // "not found" is a normal answer, not a warning
			Colorful:                  false,
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	if err := gdb.Use(otelgorm.NewPlugin()); err != nil {
		return nil, fmt.Errorf("otelgorm: %w", err)
	}
	return gdb, nil
}
