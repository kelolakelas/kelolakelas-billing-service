package database

import (
	"fmt"
	"log/slog"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func NewPostgresDB(host, port, user, password, dbname, sslMode, channelBinding string) (*gorm.DB, error) {
	dsn := buildPostgresDSN(host, port, user, password, dbname, sslMode, channelBinding)

	slog.Info("Connecting to PostgreSQL", "dsn", fmt.Sprintf("host=%s user=%s dbname=%s port=%s", host, user, dbname, port))

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get sql database from gorm: %w", err)
	}

	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)

	return db, nil
}

func buildPostgresDSN(host, port, user, password, dbname, sslMode, channelBinding string) string {
	return fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%s sslmode=%s channel_binding=%s TimeZone=UTC",
		host, user, password, dbname, port, sslMode, channelBinding)
}
