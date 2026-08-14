package migration

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/config"
	"github.com/kelolakelas/kelolakelas-billing-service/pkg/database"
)

func Run(path, direction string, steps int) error {
	if direction != "up" && direction != "down" {
		return errors.New("direction must be up or down")
	}
	if steps < 1 {
		return errors.New("steps must be greater than zero")
	}
	absolute, err := directory(path)
	if err != nil {
		return err
	}
	dsn, err := databaseURL()
	if err != nil {
		return err
	}
	m, err := migrate.New((&url.URL{Scheme: "file", Path: absolute}).String(), dsn)
	if err != nil {
		return fmt.Errorf("migration initialization failed: %w", err)
	}
	defer m.Close()
	if direction == "up" {
		err = m.Up()
	} else {
		err = m.Steps(-steps)
	}
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}
	return nil
}

func Seed(directory, file string) error {
	paths, err := files(directory, file)
	if err != nil {
		return err
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		return errors.New("seed configuration unavailable")
	}
	db, err := database.NewPostgresDB(cfg.DBHost, cfg.DBPort, cfg.DBUser, cfg.DBPassword, cfg.DBName, cfg.DBSSLMode, cfg.DBChannelBinding)
	if err != nil {
		return errors.New("open database failed")
	}
	tx := db.Begin()
	if tx.Error != nil {
		return errors.New("begin seed transaction failed")
	}
	defer tx.Rollback()
	for _, path := range paths {
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return errors.New("read seed file failed")
		}
		if err := tx.Exec(string(contents)).Error; err != nil {
			return errors.New("execute seed failed")
		}
	}
	if err := tx.Commit().Error; err != nil {
		return errors.New("commit seed failed")
	}
	return nil
}
func CreateMigration(directory, name string) (string, error) {
	return createPair(directory, name, "migration")
}
func CreateSeeder(directory, name string) (string, error) {
	name, err := validName(name, "seeder")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", errors.New("create seed directory failed")
	}
	version, err := nextVersion(directory, 6)
	if err != nil {
		return "", err
	}
	path := filepath.Join(directory, fmt.Sprintf("%06d_%s.sql", version, name))
	if err := os.WriteFile(path, []byte("-- Add idempotent SQL statements here.\n"), 0o644); err != nil {
		return "", errors.New("create seed file failed")
	}
	return fmt.Sprintf("created %s", filepath.ToSlash(path)), nil
}
func createPair(directory, name, kind string) (string, error) {
	name, err := validName(name, kind)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", errors.New("create migration directory failed")
	}
	version, err := nextVersion(directory, 14)
	if err != nil {
		return "", err
	}
	prefix := fmt.Sprintf("%014d_%s", version, name)
	up, down := filepath.Join(directory, prefix+".up.sql"), filepath.Join(directory, prefix+".down.sql")
	if err := createEmpty(up); err != nil {
		return "", errors.New("create migration up file failed")
	}
	if err := createEmpty(down); err != nil {
		_ = os.Remove(up)
		return "", errors.New("create migration down file failed")
	}
	return fmt.Sprintf("created %s and %s", filepath.ToSlash(up), filepath.ToSlash(down)), nil
}
func files(directory, file string) ([]string, error) {
	if file != "" {
		if _, err := os.Stat(file); err != nil {
			return nil, errors.New("seed file unavailable")
		}
		return []string{file}, nil
	}
	if directory == "" {
		directory = "seeders"
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, errors.New("read seed directory failed")
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			paths = append(paths, filepath.Join(directory, entry.Name()))
		}
	}
	if len(paths) == 0 {
		return nil, errors.New("no seed SQL files found")
	}
	return paths, nil
}
func directory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("resolve migration path failed")
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return "", errors.New("migration path unavailable")
	}
	return absolute, nil
}
func databaseURL() (string, error) {
	cfg, err := config.LoadConfig()
	if err != nil {
		return "", err
	}
	return buildDatabaseURL(cfg)
}

func buildDatabaseURL(cfg config.Config) (string, error) {
	if cfg.DatabaseURL != "" {
		databaseURL, err := url.Parse(cfg.DatabaseURL)
		if err != nil {
			return "", err
		}
		query := databaseURL.Query()
		addChannelBinding(query, cfg.DBChannelBinding)
		databaseURL.RawQuery = query.Encode()
		return databaseURL.String(), nil
	}
	query := url.Values{"sslmode": []string{cfg.DBSSLMode}}
	addChannelBinding(query, cfg.DBChannelBinding)
	return (&url.URL{Scheme: "postgres", User: url.UserPassword(cfg.DBUser, cfg.DBPassword), Host: cfg.DBHost + ":" + cfg.DBPort, Path: "/" + cfg.DBName, RawQuery: query.Encode()}).String(), nil
}

func addChannelBinding(query url.Values, value string) {
	query.Del("channel_binding")
	if value == "prefer" || value == "require" {
		query.Set("channel_binding", value)
	}
}
func validName(name, kind string) (string, error) {
	name = strings.Join(strings.Fields(strings.TrimSpace(name)), "_")
	if name == "" {
		return "", fmt.Errorf("%s name is required", kind)
	}
	for _, r := range name {
		if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return "", fmt.Errorf("%s name contains invalid characters", kind)
		}
	}
	return name, nil
}
func nextVersion(directory string, width int) (int64, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, errors.New("read migration directory failed")
	}
	version := time.Now().UTC().Unix()
	if width == 6 {
		version = 0
	}
	for _, entry := range entries {
		parsed, err := strconv.ParseInt(strings.SplitN(entry.Name(), "_", 2)[0], 10, 64)
		if err == nil && parsed >= version {
			version = parsed + 1
		}
	}
	return version, nil
}
func createEmpty(path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	return file.Close()
}
