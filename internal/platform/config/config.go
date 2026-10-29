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
	GeminiAPIKeys    []string // GEMINI_API_KEY then GEMINI_API_KEYS (comma-separated): the course builder takes turns
	GeminiModel      string
	GeminiGradeModel string

	AnthropicAPIKey string
	AIDigestModel   string // reading PDFs and slides (cheap, bulk)
	AIOrganiseModel string // organising courses into units
	AIVideoModel    string // watching videos (Gemini)
	AIEnrichModel   string // finding references with web search
	AIQuestionModel string // writing unit test questions
	AIMarkModel     string // marking written answers
	AIProvider      string

	AIMonthlyBudgetUSD float64
	AIUserDailyUSD     float64 // each creator's AI allowance (0 = none)
	AIUserMonthlyUSD   float64

	FreeCourseLimit    int
	FreeMaterialsLimit int
	FreeOrganiseRuns   int
	FreeQuestionRuns   int // AI question pools per unit
	TestsPerDay        int // test starts per learner per day
	NewCoursesPerDay   int
	PDFPartPages       int // long PDFs are read this many pages at a time
	VideoPartMinutes   int // long videos are watched this many minutes at a time
	PDFFirstPages      int // the first part is shorter, so units come quickly
	VideoFirstMinutes  int
	FakeVideoSeconds   int // development: the length the fake AI gives every video
	MaxMaterialFileMB  int

	AdminEmail    string // seeded admin account (created only if missing)
	AdminPassword string

	ContactEmail string // where home-page contact messages go (empty = AdminEmail)
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
		GeminiAPIKeys:    append([]string{os.Getenv("GEMINI_API_KEY")}, strings.Split(os.Getenv("GEMINI_API_KEYS"), ",")...),
		GeminiModel:      os.Getenv("GEMINI_MODEL"),
		GeminiGradeModel: os.Getenv("GEMINI_GRADE_MODEL"),

		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		// Empty = the chosen provider's default model
		AIDigestModel:   os.Getenv("AI_DIGEST_MODEL"),
		AIOrganiseModel: os.Getenv("AI_ORGANISE_MODEL"),
		AIVideoModel:    get("AI_VIDEO_MODEL", "gemini-2.5-flash"),
		// Picking links needs less judgment than teaching: Sonnet is cheaper
		AIEnrichModel:   os.Getenv("AI_ENRICH_MODEL"), // empty = the provider's default
		AIQuestionModel: os.Getenv("AI_QUESTION_MODEL"),
		AIMarkModel:     os.Getenv("AI_MARK_MODEL"),
		AIProvider:      os.Getenv("AI_PROVIDER"), // claude | gemini | fake (empty: claude if its key is set, else gemini)

		AIMonthlyBudgetUSD: getFloat("AI_MONTHLY_BUDGET_USD", 0),
		AIUserDailyUSD:     getFloat("AI_USER_DAILY_USD", 0.5),
		AIUserMonthlyUSD:   getFloat("AI_USER_MONTHLY_USD", 2),

		FreeCourseLimit:    getInt("FREE_COURSE_LIMIT", 2),
		FreeMaterialsLimit: getInt("FREE_MATERIALS_PER_COURSE", 15),
		FreeOrganiseRuns:   getInt("FREE_ORGANISE_RUNS", 3),
		FreeQuestionRuns:   getInt("FREE_QUESTION_RUNS", 3),
		TestsPerDay:        getInt("TESTS_PER_DAY", 20),
		NewCoursesPerDay:   getInt("NEW_COURSES_PER_DAY", 1),
		PDFPartPages:       getInt("AI_PDF_PART_PAGES", 25),
		VideoPartMinutes:   getInt("AI_VIDEO_PART_MINUTES", 10),
		PDFFirstPages:      getInt("AI_PDF_FIRST_PART_PAGES", 8),
		VideoFirstMinutes:  getInt("AI_VIDEO_FIRST_MINUTES", 3),
		FakeVideoSeconds:   getInt("FAKE_VIDEO_SECONDS", 0),
		MaxMaterialFileMB:  getInt("MAX_MATERIAL_FILE_MB", 20),

		AdminEmail:    os.Getenv("ADMIN_EMAIL"),
		AdminPassword: os.Getenv("ADMIN_PASSWORD"),
		ContactEmail:  os.Getenv("CONTACT_EMAIL"),
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

func getFloat(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil {
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
