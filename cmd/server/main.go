package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	_ "github.com/kelolakelas/kelolakelas-billing-service/docs"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/delivery/http/handler"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/delivery/http/middleware"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/repository"
	"github.com/kelolakelas/kelolakelas-billing-service/internal/usecase"
	"github.com/kelolakelas/kelolakelas-billing-service/pkg/academic"
	"github.com/kelolakelas/kelolakelas-billing-service/pkg/database"
	"github.com/kelolakelas/kelolakelas-billing-service/pkg/duitku"
	"github.com/kelolakelas/kelolakelas-billing-service/pkg/email"
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

	// Initialize Clients
	duitkuClient := duitku.NewClient(cfg.DuitkuAPIBaseURL, cfg.DuitkuAPIKey, cfg.DuitkuMerchantCode, nil)
	academicClient := academic.NewClient(cfg.AcademicServiceURL, cfg.InternalServiceCredential)

	// Initialize Repositories
	txRepo := repository.NewTransactionRepository(db)
	subscriptionRepo := repository.NewSubscriptionRepository(db)
	walletRepo := repository.NewWalletRepository(db)
	ledgerRepo := repository.NewLedgerEntryRepository(db)
	txManager := repository.NewTransactionManager(db)

	// Initialize Usecases
	txUsecase := usecase.NewTransactionUsecase(txRepo, walletRepo, ledgerRepo, subscriptionRepo, duitkuClient, academicClient, cfg, txManager)
	worker := usecase.NewSubscriptionWorker(subscriptionRepo, txRepo, duitkuClient, email.NewResendClient(cfg.ResendAPIKey, cfg.ResendFromEmail), cfg)

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
		protected.GET("/transactions", txHandler.List)
		protected.GET("/transactions/:id", txHandler.Get)
	}
	internal := r.Group("/internal/billing")
	internal.Use(middleware.InternalServiceAuth(cfg.InternalServiceCredential))
	internal.POST("/transactions", txHandler.GenerateInternalSubscriptionPayment)

	server := &http.Server{Addr: "0.0.0.0:" + cfg.Port, Handler: r}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if cfg.SubscriptionWorkerEnabled {
		go worker.Run(ctx)
	}
	slog.Info("Starting billing service", "port", cfg.Port)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("Failed to start billing service", "error", err)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("Failed to shutdown billing service", "error", err)
	}
}
