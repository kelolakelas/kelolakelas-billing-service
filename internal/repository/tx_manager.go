package repository

import (
	"context"

	"gorm.io/gorm"
)

type txKey struct{}

type gormTxManager struct{ db *gorm.DB }

func NewTransactionManager(db *gorm.DB) BillingTransactionManager { return &gormTxManager{db: db} }

func (m *gormTxManager) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, txKey{}, tx))
	})
}

func GetDB(ctx context.Context, fallback *gorm.DB) *gorm.DB {
	if tx, ok := ctx.Value(txKey{}).(*gorm.DB); ok {
		return tx.WithContext(ctx)
	}
	return fallback.WithContext(ctx)
}
