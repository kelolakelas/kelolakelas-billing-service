package database

import (
	"fmt"
	"io"
	"log/slog"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// UTCNow keeps GORM-generated timestamps independent of the process timezone.
func UTCNow() time.Time { return time.Now().UTC() }

func NewPostgresDB(host, port, user, password, dbname, sslMode, channelBinding string) (*gorm.DB, error) {
	dsn := buildPostgresDSN(host, port, user, password, dbname, sslMode, channelBinding)

	slog.Info("Connecting to PostgreSQL", "dsn", fmt.Sprintf("host=%s user=%s dbname=%s port=%s", host, user, dbname, port))

	// SQL values include payment instructions; never interpolate them into logs.
	redactedLogger := logger.New(slog.NewLogLogger(slog.NewTextHandler(io.Discard, nil), slog.LevelError), logger.Config{
		LogLevel:             logger.Silent,
		ParameterizedQueries: true,
	})
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: redactedLogger, NowFunc: UTCNow})
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
	dsn := fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%s sslmode=%s",
		host, user, password, dbname, port, sslMode)
	if channelBinding != "" && channelBinding != "disable" {
		dsn += fmt.Sprintf(" channel_binding=%s", channelBinding)
	}
	return dsn + " TimeZone=UTC"
}
