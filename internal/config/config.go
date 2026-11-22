// Package config loads runtime configuration from environment variables and
// secret files. Secrets are never read from plain environment variables so
// they cannot leak through process listings or container inspection.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime settings.
type Config struct {
	ListenAddr string

	DBHost string
	DBPort string
	DBName string
	DBUser string

	// Secrets (already read from their files).
	DBPassword string
	APIKey     string
	AdminToken string // empty disables the admin endpoints
	IPSalt     string

	Model string

	AllowedOrigins []string
	ClientIPHeader string // e.g. "CF-Connecting-IP"; empty = use the socket address

	DailyCap        int
	MaxMessageChars int
	HistoryTurns    int
	MaxOutputTokens int
	MaxConcurrent   int
	IPRatePerMin    int
	IPBurst         int
	RequestTimeout  time.Duration
}

// Load reads configuration from the environment.
func Load() (*Config, error) {
	c := &Config{
		ListenAddr:      env("LISTEN_ADDR", ":8080"),
		DBHost:          env("DB_HOST", "postgres"),
		DBPort:          env("DB_PORT", "5432"),
		DBName:          env("DB_NAME", "chatbot"),
		DBUser:          env("DB_USER", "chatbot"),
		Model:           env("MODEL", "claude-haiku-5-5"),
		AllowedOrigins:  splitCSV(env("ALLOWED_ORIGINS", "https://example.com,https://www.example.com")),
		ClientIPHeader:  os.Getenv("CLIENT_IP_HEADER"),
		DailyCap:        envInt("DAILY_CAP", 200),
		MaxMessageChars: envInt("MAX_MESSAGE_CHARS", 500),
		HistoryTurns:    envInt("HISTORY_TURNS", 10),
		MaxOutputTokens: envInt("MAX_OUTPUT_TOKENS", 600),
		MaxConcurrent:   envInt("MAX_CONCURRENT", 8),
		IPRatePerMin:    envInt("IP_RATE_PER_MIN", 10),
		IPBurst:         envInt("IP_BURST", 5),
		RequestTimeout:  time.Duration(envInt("REQUEST_TIMEOUT_SEC", 60)) * time.Second,
	}

	var err error
	var errs []error
	if c.DBPassword, err = secret("DB_PASSWORD_FILE", true); err != nil {
		errs = append(errs, err)
	}
	if c.APIKey, err = secret("ANTHROPIC_API_KEY_FILE", true); err != nil {
		errs = append(errs, err)
	}
	if c.IPSalt, err = secret("IP_SALT_FILE", true); err != nil {
		errs = append(errs, err)
	}
	if c.AdminToken, err = secret("ADMIN_TOKEN_FILE", false); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if c.AdminToken != "" && len(c.AdminToken) < 32 {
		return nil, errors.New("admin token must be at least 32 characters")
	}
	if c.DailyCap < 1 || c.MaxMessageChars < 1 || c.HistoryTurns < 0 || c.MaxOutputTokens < 1 ||
		c.MaxConcurrent < 1 || c.IPRatePerMin < 1 || c.IPBurst < 1 {
		return nil, errors.New("numeric settings must be positive")
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return -1 // rejected by validation in Load
	}
	return n
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// secret reads the file named by the environment variable envKey.
func secret(envKey string, required bool) (string, error) {
	path := os.Getenv(envKey)
	if path == "" {
		if required {
			return "", fmt.Errorf("%s is not set", envKey)
		}
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", envKey, err)
	}
	v := strings.TrimSpace(string(b))
	if v == "" && required {
		return "", fmt.Errorf("%s: file is empty", envKey)
	}
	return v, nil
}
