package main

import (
	"log/slog"
	"os"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	_ "github.com/kelolakelas/kelolakelas-billing-service/docs"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/delivery/http/handler"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/delivery/http/middleware"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/usecase"
	"github.com/kelolakelas/kelolakelas-billing-service/pkg/academic"
	"github.com/kelolakelas/kelolakelas-billing-service/pkg/database"
	"github.com/kelolakelas/kelolakelas-billing-service/pkg/duitku"
)

// @title KelolaKelas Billing Service API
// @version 1.0
// @description Billing and Payment Service for KelolaKelas Platform
// @BasePath /
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
func main() {
	// Initialize JSON logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// Load Configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		slog.Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}

	// Initialize DB Connection
	db, err := database.NewPostgresDB(cfg.DBHost, cfg.DBPort, cfg.DBUser, cfg.DBPassword, cfg.DBName, cfg.DBSSLMode, cfg.DBChannelBinding)
	if err != nil {
		slog.Error("Database connection failed", "error", err)
		os.Exit(1)
	}

	// Auto-migrate schema
	slog.Info("Running auto-migration...")
	if err := db.AutoMigrate(
		&domain.Transaction{},
		&domain.Subscription{},
		&domain.Wallet{},
		&domain.LedgerEntry{},
		&domain.BankAccount{},
		&domain.Withdrawal{},
		&domain.Voucher{},
	); err != nil {
		slog.Error("Auto-migration failed", "error", err)
		os.Exit(1)
	}

	// Initialize Clients
	duitkuClient := duitku.NewClient(cfg.DuitkuAPIBaseURL, cfg.DuitkuAPIKey, cfg.DuitkuMerchantCode, nil)
	academicClient := academic.NewClient(cfg.AcademicServiceURL)

	// Initialize Repositories
	txRepo := repository.NewTransactionRepository(db)
	subscriptionRepo := repository.NewSubscriptionRepository(db)
	walletRepo := repository.NewWalletRepository(db)
	ledgerRepo := repository.NewLedgerEntryRepository(db)

	// Initialize Usecases
	txUsecase := usecase.NewTransactionUsecase(txRepo, walletRepo, ledgerRepo, subscriptionRepo, duitkuClient, academicClient, cfg)

	// Initialize Handlers
	txHandler := handler.NewTransactionHandler(txUsecase, duitkuClient)

	// Initialize Router
	r := gin.New()
	r.Use(gin.Recovery())

	// Health check endpoint
	r.GET("/health", healthHandler("billing-service"))

	// Swagger UI
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	// Routes
	apiV1 := r.Group("/api/v1/billing")
	{
		apiV1.POST("/webhooks/duitku", txHandler.HandleDuitkuWebhook)
		protected := apiV1.Group("")
		protected.Use(middleware.AuthMiddleware(cfg.JWTSecret))
		protected.POST("/transactions", txHandler.GenerateSubscriptionPayment)
		protected.GET("/transactions", txHandler.List)
		protected.GET("/transactions/:id", txHandler.Get)
	}

	slog.Info("Starting billing service", "port", cfg.Port)
	if err := r.Run("0.0.0.0:" + cfg.Port); err != nil {
		slog.Error("Failed to start billing service", "error", err)
		os.Exit(1)
	}
}
