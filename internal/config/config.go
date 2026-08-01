package config

import (
	"log/slog"
	"os"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

type Config struct {
	DBHost              string `mapstructure:"DB_HOST"`
	DBPort              string `mapstructure:"DB_PORT"`
	DBUser              string `mapstructure:"DB_USER"`
	DBPassword          string `mapstructure:"DB_PASSWORD"`
	DBName              string `mapstructure:"DB_NAME"`
	Port                string `mapstructure:"PORT"`
	FlipBaseURL         string `mapstructure:"FLIP_BASE_URL"`
	FlipAPISecretKey    string `mapstructure:"FLIP_API_SECRET_KEY"`
	FlipValidationToken string `mapstructure:"FLIP_VALIDATION_TOKEN"`
	AcademicServiceURL  string `mapstructure:"ACADEMIC_SERVICE_URL"`
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
	if config.FlipBaseURL == "" {
		config.FlipBaseURL = "https://bigflip.id/big_sandbox_api"
	}
	if config.AcademicServiceURL == "" {
		config.AcademicServiceURL = "http://localhost:8081"
	}

	return config, nil
}
