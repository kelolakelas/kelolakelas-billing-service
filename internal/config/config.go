package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

type Config struct {
	DatabaseURL                                string `mapstructure:"DATABASE_URL"`
	DBHost                                     string `mapstructure:"DB_HOST"`
	DBPort                                     string `mapstructure:"DB_PORT"`
	DBSSLMode                                  string `mapstructure:"DB_SSLMODE"`
	DBChannelBinding                           string `mapstructure:"DB_CHANNEL_BINDING"`
	DBUser                                     string `mapstructure:"DB_USER"`
	DBPassword                                 string `mapstructure:"DB_PASSWORD"`
	DBName                                     string `mapstructure:"DB_NAME"`
	Port                                       string `mapstructure:"PORT"`
	DuitkuAPIBaseURL                           string `mapstructure:"DUITKU_API_BASE_URL"`
	DuitkuAPIKey                               string `mapstructure:"DUITKU_API_KEY"`
	DuitkuMerchantCode                         string `mapstructure:"DUITKU_MERCHANT_CODE"`
	DuitkuCallbackURL                          string `mapstructure:"DUITKU_CALLBACK_URL"`
	DuitkuReturnURL                            string `mapstructure:"DUITKU_RETURN_URL"`
	AcademicServiceURL                         string `mapstructure:"ACADEMIC_SERVICE_URL"`
	InternalServiceCredential                  string `mapstructure:"INTERNAL_SERVICE_CREDENTIAL"`
	JWTSecret                                  string `mapstructure:"JWT_SECRET"`
	SubscriptionWorkerEnabled                  bool   `mapstructure:"SUBSCRIPTION_WORKER_ENABLED"`
	SubscriptionWorkerIntervalMinutes          int    `mapstructure:"SUBSCRIPTION_WORKER_INTERVAL_MINUTES"`
	SubscriptionPaymentReminderIntervalDays    int    `mapstructure:"SUBSCRIPTION_PAYMENT_REMINDER_INTERVAL_DAYS"`
	SubscriptionPaymentExpiryPeriodDays        int    `mapstructure:"SUBSCRIPTION_PAYMENT_EXPIRY_PERIOD_DAYS"`
	PaymentReconciliationWorkerEnabled         bool   `mapstructure:"PAYMENT_RECONCILIATION_WORKER_ENABLED"`
	PaymentReconciliationWorkerIntervalMinutes int    `mapstructure:"PAYMENT_RECONCILIATION_WORKER_INTERVAL_MINUTES"`
	PaymentReconciliationMaxAttempts           int    `mapstructure:"PAYMENT_RECONCILIATION_MAX_ATTEMPTS"`
	ResendAPIKey                               string `mapstructure:"RESEND_API_KEY"`
	ResendFromEmail                            string `mapstructure:"RESEND_FROM_EMAIL"`
}

func LoadConfig() (Config, error) {
	if err := godotenv.Load(); err != nil {
		slog.Warn("No .env file found by godotenv")
	}

	viper.SetConfigFile(".env")
	if err := viper.ReadInConfig(); err != nil {
		if !os.IsNotExist(err) {
			return Config{}, err
		}
		slog.Warn("No .env file found by Viper, using system environment variables")
	}

	viper.AutomaticEnv()
	for _, key := range []string{
		"DATABASE_URL", "DB_HOST", "DB_PORT", "DB_SSLMODE", "DB_CHANNEL_BINDING", "DB_USER", "DB_PASSWORD", "DB_NAME",
		"PORT", "DUITKU_API_BASE_URL", "DUITKU_API_KEY", "DUITKU_MERCHANT_CODE",
		"DUITKU_CALLBACK_URL", "DUITKU_RETURN_URL", "ACADEMIC_SERVICE_URL", "INTERNAL_SERVICE_CREDENTIAL", "JWT_SECRET",
		"SUBSCRIPTION_WORKER_ENABLED", "SUBSCRIPTION_WORKER_INTERVAL_MINUTES", "SUBSCRIPTION_PAYMENT_REMINDER_INTERVAL_DAYS", "SUBSCRIPTION_PAYMENT_EXPIRY_PERIOD_DAYS", "PAYMENT_RECONCILIATION_WORKER_ENABLED", "PAYMENT_RECONCILIATION_WORKER_INTERVAL_MINUTES", "PAYMENT_RECONCILIATION_MAX_ATTEMPTS", "RESEND_API_KEY", "RESEND_FROM_EMAIL",
	} {
		if err := viper.BindEnv(key); err != nil {
			return Config{}, err
		}
	}

	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		return Config{}, err
	}
	if err := applyDatabaseURL(&config); err != nil {
		return Config{}, err
	}

	if config.DBHost == "" {
		config.DBHost = "localhost"
	}
	if config.DBPort == "" {
		config.DBPort = "5432"
	}
	if config.DBSSLMode == "" {
		config.DBSSLMode = "disable"
	}
	if config.DBChannelBinding == "" {
		config.DBChannelBinding = "disable"
	}
	if err := validateChannelBinding(config.DBChannelBinding); err != nil {
		return Config{}, err
	}
	if config.DBUser == "" {
		config.DBUser = "postgres"
	}
	if config.DBPassword == "" {
		config.DBPassword = "postgres"
	}
	if config.DBName == "" {
		config.DBName = "kelolakelas_billing"
	}
	if config.Port == "" {
		config.Port = "8082"
	}
	if config.SubscriptionWorkerIntervalMinutes == 0 {
		config.SubscriptionWorkerIntervalMinutes = 1440
	}
	if config.SubscriptionPaymentReminderIntervalDays == 0 {
		config.SubscriptionPaymentReminderIntervalDays = 3
	}
	if config.SubscriptionPaymentExpiryPeriodDays == 0 {
		config.SubscriptionPaymentExpiryPeriodDays = 14
	}
	if !viper.IsSet("PAYMENT_RECONCILIATION_WORKER_ENABLED") {
		config.PaymentReconciliationWorkerEnabled = true
	}
	if config.PaymentReconciliationWorkerIntervalMinutes == 0 {
		config.PaymentReconciliationWorkerIntervalMinutes = 1
	}
	if config.PaymentReconciliationMaxAttempts == 0 {
		config.PaymentReconciliationMaxAttempts = 10
	}
	if config.DuitkuAPIBaseURL == "" {
		config.DuitkuAPIBaseURL = "https://sandbox.duitku.com/webapi/api/merchant"
	}
	if config.DuitkuCallbackURL == "" {
		config.DuitkuCallbackURL = "http://localhost:" + config.Port + "/api/v1/billing/webhooks/duitku"
	}
	if config.DuitkuReturnURL == "" {
		config.DuitkuReturnURL = config.DuitkuCallbackURL
	}
	if config.AcademicServiceURL == "" {
		config.AcademicServiceURL = "http://localhost:8081"
	}
	if config.JWTSecret == "" {
		return Config{}, fmt.Errorf("JWT_SECRET is required")
	}
	config.InternalServiceCredential = strings.TrimSpace(config.InternalServiceCredential)
	if config.InternalServiceCredential == "" {
		return Config{}, fmt.Errorf("INTERNAL_SERVICE_CREDENTIAL is required")
	}

	return config, nil
}

func applyDatabaseURL(config *Config) error {
	if config.DatabaseURL == "" {
		return nil
	}
	databaseURL, err := url.Parse(config.DatabaseURL)
	if err != nil || (databaseURL.Scheme != "postgres" && databaseURL.Scheme != "postgresql") || databaseURL.Hostname() == "" || databaseURL.Path == "" {
		return fmt.Errorf("DATABASE_URL must be a valid PostgreSQL URL")
	}
	if config.DBHost == "" {
		config.DBHost = databaseURL.Hostname()
	}
	if config.DBPort == "" {
		config.DBPort = databaseURL.Port()
		if config.DBPort == "" {
			config.DBPort = "5432"
		}
	}
	if config.DBUser == "" && databaseURL.User != nil {
		config.DBUser = databaseURL.User.Username()
	}
	if config.DBPassword == "" && databaseURL.User != nil {
		config.DBPassword, _ = databaseURL.User.Password()
	}
	if config.DBName == "" {
		config.DBName = strings.TrimPrefix(databaseURL.Path, "/")
	}
	if config.DBSSLMode == "" {
		config.DBSSLMode = databaseURL.Query().Get("sslmode")
	}
	if config.DBChannelBinding == "" {
		config.DBChannelBinding = databaseURL.Query().Get("channel_binding")
	}
	return nil
}

func validateChannelBinding(value string) error {
	switch value {
	case "disable", "prefer", "require":
		return nil
	default:
		return fmt.Errorf("DB_CHANNEL_BINDING must be one of disable, prefer, or require")
	}
}
