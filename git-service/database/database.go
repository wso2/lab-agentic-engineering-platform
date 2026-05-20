package database

import (
	"fmt"
	"log/slog"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Open connects to the PostgreSQL database and auto-migrates the given models.
func Open(dsn string, models ...any) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:                 gormlogger.Default.LogMode(gormlogger.Silent),
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	if len(models) > 0 {
		if err := db.AutoMigrate(models...); err != nil {
			return nil, fmt.Errorf("auto-migrate: %w", err)
		}
	}

	slog.Info("database connected and migrated")
	return db, nil
}
