// Package config reads all settings from the environment (.env in dev,
// /opt/focusflow/.env mounted into the container in production).
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Env         string // production | development
	Port        string
	DatabaseURL string
	LogLevel    string

	// AppKey signs email-verification links. 32+ random bytes, never shared.
	AppKey string

	// PublicURL is where the API itself is reachable (verification links).
	PublicURL string
	// FrontendURL is the Nuxt app on Vercel (reset links, redirects, invitations).
	FrontendURL string

	SessionTTL    time.Duration
	SecureCookies bool

	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string

	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string
	MailFrom     string
	MailFromName string

	S3Endpoint       string // RustFS on the VPS: http://127.0.0.1:9000
	S3PublicEndpoint string // https://api.focusflow.sswoco.org/media
	S3Region         string
	S3Bucket         string
	S3AccessKey      string
	S3SecretKey      string

	YouTubeAPIKey    string
	GeminiAPIKey     string
	GeminiModel      string
	GeminiGradeModel string

	AdminEmail    string // seeded admin account (created only if missing)
	AdminPassword string
}

func Load() (*Config, error) {
	c := &Config{
		Env:         get("APP_ENV", "development"),
		Port:        get("PORT", "8091"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		LogLevel:    get("LOG_LEVEL", "info"),
		AppKey:      os.Getenv("APP_KEY"),
		PublicURL:   strings.TrimRight(get("PUBLIC_URL", "http://localhost:8091"), "/"),
		FrontendURL: strings.TrimRight(get("FRONTEND_URL", "http://localhost:3000"), "/"),

		SessionTTL: time.Duration(getInt("SESSION_TTL_DAYS", 30)) * 24 * time.Hour,

		GoogleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		GoogleRedirectURL:  os.Getenv("GOOGLE_REDIRECT_URI"),

		SMTPHost:     os.Getenv("SMTP_HOST"),
		SMTPPort:     getInt("SMTP_PORT", 465),
		SMTPUser:     os.Getenv("SMTP_USERNAME"),
		SMTPPassword: os.Getenv("SMTP_PASSWORD"),
		MailFrom:     os.Getenv("MAIL_FROM_ADDRESS"),
		MailFromName: get("MAIL_FROM_NAME", "FocusFlow"),

		S3Endpoint:       os.Getenv("S3_ENDPOINT"),
		S3PublicEndpoint: strings.TrimRight(os.Getenv("S3_PUBLIC_ENDPOINT"), "/"),
		S3Region:         get("AWS_REGION", "us-east-1"),
		S3Bucket:         get("AWS_BUCKET_NAME", "focusflow-media"),
		S3AccessKey:      os.Getenv("AWS_ACCESS_KEY_ID"),
		S3SecretKey:      os.Getenv("AWS_SECRET_ACCESS_KEY"),

		YouTubeAPIKey:    os.Getenv("YOUTUBE_API_KEY"),
		GeminiAPIKey:     os.Getenv("GEMINI_API_KEY"),
		GeminiModel:      os.Getenv("GEMINI_MODEL"),
		GeminiGradeModel: os.Getenv("GEMINI_GRADE_MODEL"),

		AdminEmail:    os.Getenv("ADMIN_EMAIL"),
		AdminPassword: os.Getenv("ADMIN_PASSWORD"),
	}
	c.SecureCookies = c.Env == "production"

	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if len(c.AppKey) < 32 {
		return nil, fmt.Errorf("APP_KEY must be at least 32 characters")
	}
	return c, nil
}

func (c *Config) IsProduction() bool { return c.Env == "production" }

func get(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}
