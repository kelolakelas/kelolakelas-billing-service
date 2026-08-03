package config

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

type Config struct {
	DBHost             string `mapstructure:"DB_HOST"`
	DBPort             string `mapstructure:"DB_PORT"`
	DBUser             string `mapstructure:"DB_USER"`
	DBPassword         string `mapstructure:"DB_PASSWORD"`
	DBName             string `mapstructure:"DB_NAME"`
	Port               string `mapstructure:"PORT"`
	DuitkuAPIBaseURL   string `mapstructure:"DUITKU_API_BASE_URL"`
	DuitkuAPIKey       string `mapstructure:"DUITKU_API_KEY"`
	DuitkuMerchantCode string `mapstructure:"DUITKU_MERCHANT_CODE"`
	DuitkuCallbackURL  string `mapstructure:"DUITKU_CALLBACK_URL"`
	DuitkuReturnURL    string `mapstructure:"DUITKU_RETURN_URL"`
	AcademicServiceURL string `mapstructure:"ACADEMIC_SERVICE_URL"`
	JWTSecret          string `mapstructure:"JWT_SECRET"`
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

	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		return Config{}, err
	}

	if config.DBHost == "" {
		config.DBHost = "localhost"
	}
	if config.DBPort == "" {
		config.DBPort = "5432"
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

	return config, nil
}
