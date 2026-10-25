// FocusFlow API: one Go service for accounts, courses and course materials
// (replaces the Laravel auth service and the two Go services).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"focusflow-api/internal/auth"
	"focusflow-api/internal/course/controllers"
	coursemodels "focusflow-api/internal/course/models"
	courseroutes "focusflow-api/internal/course/routes"
	"focusflow-api/internal/learn"
	"focusflow-api/internal/material/handler"
	"focusflow-api/internal/material/model"
	"focusflow-api/internal/material/repository"
	materialroutes "focusflow-api/internal/material/routes"
	"focusflow-api/internal/material/services"
	"focusflow-api/internal/platform/config"
	"focusflow-api/internal/platform/db"
	"focusflow-api/internal/platform/httpx"
	"focusflow-api/internal/platform/logger"
	"focusflow-api/internal/platform/mailer"
	"focusflow-api/internal/platform/metrics"
	"focusflow-api/internal/platform/storage"
	"focusflow-api/internal/platform/tracing"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"gorm.io/gorm"
)

var version = "dev" // set at build time: -ldflags "-X main.version=<sha>"

func main() {
	// "focusflow-api healthcheck" is the container HEALTHCHECK (no curl in distroless)
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	_ = godotenv.Load() // .env in development; production passes real env vars

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}
	logger.Init(cfg.LogLevel)
	slog.Info("starting focusflow-api", "version", version, "env", cfg.Env)

	shutdownTracer := tracing.InitTracer("focusflow-api")

	gdb, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		slog.Error("database", "error", err)
		os.Exit(1)
	}
	if err := migrate(gdb); err != nil {
		slog.Error("migrate", "error", err)
		os.Exit(1)
	}

	storage.Init()
	mail := mailer.New(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUser, cfg.SMTPPassword, cfg.MailFrom, cfg.MailFromName)
	controllers.Mail = mail

	authSvc := auth.NewService(gdb, cfg.AppKey, cfg.SessionTTL)
	auth.Init(authSvc)
	if created, err := authSvc.EnsureAdmin(cfg.AdminEmail, cfg.AdminPassword); err != nil {
		slog.Error("seed admin", "error", err)
	} else if created {
		slog.Info("admin account created", "email", cfg.AdminEmail)
	}
	go purgeLoop(authSvc)

	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	// Only Caddy on the same host may set X-Forwarded-For
	_ = r.SetTrustedProxies([]string{"127.0.0.1", "::1"})
	r.Use(gin.Recovery(), otelgin.Middleware("focusflow-api"), httpx.RequestLogger(), metrics.PrometheusMiddleware())

	r.GET("/health", health(gdb))
	r.GET("/metrics", metrics.MetricsHandler())

	auth.NewHandler(authSvc, cfg, mail).Routes(r)
	courseroutes.CourseRoutes(r, gdb)
	wireLearn(r, gdb, cfg)
	wireMaterials(r, gdb, cfg)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second, // quiz generation calls Gemini
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		slog.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server", "error", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	slog.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
	_ = shutdownTracer(shutCtx)
}

// migrate creates or updates every table. GORM AutoMigrate only adds tables
// and columns, never drops them.
func migrate(gdb *gorm.DB) error {
	all := append(auth.Models(), learn.Models()...)
	all = append(all,
		&coursemodels.Category{}, &coursemodels.Course{}, &coursemodels.Enrollment{}, &coursemodels.CourseInvitation{},
		&model.CourseMaterial{}, &model.Takeaway{}, &model.LessonQuestion{}, &model.QuizAttempt{},
		&model.QuizAnswer{}, &model.Reflection{}, &model.LessonTranscript{},
	)
	// One at a time, in dependency order, so a failure names its table
	for _, m := range all {
		if err := gdb.AutoMigrate(m); err != nil {
			return fmt.Errorf("%T: %w", m, err)
		}
	}
	coursemodels.CategorySeeder(gdb)
	return nil
}

func wireMaterials(r *gin.Engine, gdb *gorm.DB, cfg *config.Config) {
	searchHandler := handler.NewSearchCourseMaterialHandler(
		services.NewSearchCourseMaterialService(repository.NewSearchCourseMaterialRepository(cfg.YouTubeAPIKey)))
	if cfg.YouTubeAPIKey == "" {
		slog.Warn("YOUTUBE_API_KEY not set: YouTube search will fail")
	}

	courseMaterialRepo := repository.NewCourseMaterialRepository(gdb)
	takeawayRepo := repository.NewTakeawayRepository(gdb)
	questionRepo := repository.NewLessonQuestionRepository(gdb)
	quizRepo := repository.NewQuizRepository(gdb)
	reflectionRepo := repository.NewReflectionRepository(gdb)
	transcriptRepo := repository.NewLessonTranscriptRepository(gdb)

	takeawayService := services.NewTakeawayService(takeawayRepo, courseMaterialRepo)
	takeawayHandler := handler.NewTakeawayHandler(takeawayService)

	var (
		quizHandler       *handler.QuizHandler
		reflectionHandler *handler.ReflectionHandler
		preparer          services.QuizPreparer     // nil when Gemini is disabled
		transcriptWarmer  services.TranscriptWarmer // nil when Gemini is disabled
	)
	if cfg.GeminiAPIKey == "" {
		slog.Warn("GEMINI_API_KEY not set: quiz and reflection endpoints return 503")
		quizHandler = handler.NewQuizHandler(nil, nil)
		reflectionHandler = handler.NewReflectionHandler(nil)
	} else {
		geminiRepo, err := repository.NewGeminiRepository(context.Background(), cfg.GeminiAPIKey, cfg.GeminiModel, cfg.GeminiGradeModel)
		if err != nil {
			slog.Error("gemini", "error", err)
			os.Exit(1)
		}
		transcriptService := services.NewTranscriptService(transcriptRepo, courseMaterialRepo, geminiRepo)
		transcriptWarmer = transcriptService
		quizService := services.NewQuizService(geminiRepo, questionRepo, quizRepo, courseMaterialRepo, takeawayService)
		quizHandler = handler.NewQuizHandler(quizService, transcriptService)
		preparer = quizService
		reflectionHandler = handler.NewReflectionHandler(
			services.NewReflectionService(reflectionRepo, courseMaterialRepo, geminiRepo, transcriptService))
	}

	courseMaterialHandler := handler.NewCourseMaterialHandler(
		services.NewCourseMaterialService(courseMaterialRepo, preparer, transcriptWarmer))
	dashboardHandler := handler.NewDashboardHandler(services.NewDashboardService(gdb, courseMaterialRepo, quizRepo))
	analyticsHandler := handler.NewLearningAnalyticsHandler(
		services.NewLearningAnalyticsService(repository.NewLearningAnalyticsRepository(gdb)))

	// Lessons may only be changed by the course owner or an admin
	handler.CanEditCourse = func(c *gin.Context, courseID string) bool {
		if c.GetString(auth.CtxUserRole) == auth.RoleAdmin {
			return true
		}
		id, err := strconv.Atoi(courseID)
		if err != nil {
			return false
		}
		var n int64
		gdb.Model(&coursemodels.Course{}).Where("course_id = ? AND user_id = ?", id, c.GetInt(auth.CtxUserID)).Count(&n)
		return n > 0
	}

	materialroutes.SetupRoutes(r, searchHandler, courseMaterialHandler, takeawayHandler, quizHandler,
		dashboardHandler, reflectionHandler, analyticsHandler)
}

// wireLearn sets up the course builder (FEATURES.md step 1).
func wireLearn(r *gin.Engine, gdb *gorm.DB, cfg *config.Config) {
	ai, provider := chooseAI(cfg)
	slog.Info("AI provider", "provider", provider)
	limits := learn.Limits{
		CoursesPerUser:        cfg.FreeCourseLimit,
		MaterialsPerCourse:    cfg.FreeMaterialsLimit,
		MaxFileBytes:          int64(cfg.MaxMaterialFileMB) << 20,
		OrganiseRunsPerCourse: cfg.FreeOrganiseRuns,
		QuestionRunsPerUnit:   cfg.FreeQuestionRuns,
		TestsPerDay:           cfg.TestsPerDay,
		PartPages:             cfg.PDFPartPages,
		VideoPartSeconds:      cfg.VideoPartMinutes * 60,
		MonthlyBudgetUSD:      cfg.AIMonthlyBudgetUSD,
		UserDailyUSD:          cfg.AIUserDailyUSD,
		UserMonthlyUSD:        cfg.AIUserMonthlyUSD,
	}
	if cfg.AIMonthlyBudgetUSD > 0 {
		slog.Info("AI monthly budget", "usd", cfg.AIMonthlyBudgetUSD)
	}
	svc := learn.NewService(gdb, ai, limits, cfg.AppKey, 2)
	// References by web search: whatever provider implements it
	if e, ok := ai.(learn.Enricher); ok && provider != "none" {
		svc.SetEnricher(e)
	}
	// Unit tests: question writing and marking, same provider
	if e, ok := ai.(learn.Examiner); ok && provider != "none" {
		if m, ok := ai.(interface{ SetExamModels(q, m string) }); ok {
			m.SetExamModels(cfg.AIQuestionModel, cfg.AIMarkModel)
		}
		svc.SetExaminer(e)
	}
	// Videos: Gemini watches them (concepts with timestamps)
	if fake, ok := ai.(learn.FakeAI); ok {
		svc.SetVideoAI(fake)
		if n := cfg.FakeVideoSeconds; n > 0 {
			svc.SetVideoDuration(func(context.Context, string) (int, error) { return n, nil })
		}
	} else if video, err := learn.NewGeminiVideoAI(context.Background(), cfg.GeminiAPIKey, cfg.AIVideoModel); err != nil {
		slog.Error("gemini video reader", "error", err)
	} else if video != nil {
		svc.SetVideoAI(video)
		if cfg.YouTubeAPIKey != "" {
			svc.SetVideoDuration(learn.YouTubeDuration(cfg.YouTubeAPIKey))
		} else {
			slog.Warn("YOUTUBE_API_KEY not set: long videos are watched in one piece")
		}
	} else {
		slog.Warn("GEMINI_API_KEY not set: videos are linked by title only (no concepts or timestamps)")
	}
	// Every AI request is logged for quota tracking (admin AI usage page)
	for _, p := range []any{ai, svc.VideoAI()} {
		if r, ok := p.(interface{ SetRecorder(learn.CallRecorder) }); ok {
			r.SetRecorder(svc.Recorder())
		}
	}
	controllers.CanCreateCourse = svc.CanCreateCourse
	// Sites allowed to upload files straight to the API: the frontend URL,
	// its www/non-www twin, and any extra in UPLOAD_ORIGINS (comma-separated)
	origins := append(strings.Split(os.Getenv("UPLOAD_ORIGINS"), ","), cfg.FrontendURL, wwwTwin(cfg.FrontendURL))
	learn.NewHandler(svc, cfg.PublicURL, origins).Routes(r)
	svc.ResumePending()
}

// chooseAI picks the course-builder AI: AI_PROVIDER=claude|gemini|fake, or
// by default Claude when its key is set, otherwise Gemini.
func chooseAI(cfg *config.Config) (learn.AI, string) {
	provider := cfg.AIProvider
	if provider == "" {
		switch {
		case cfg.AnthropicAPIKey != "":
			provider = "claude"
		case cfg.GeminiAPIKey != "":
			provider = "gemini"
		}
	}
	switch provider {
	case "fake":
		if cfg.IsProduction() {
			slog.Error("AI_PROVIDER=fake is not allowed in production")
			os.Exit(1)
		}
		slog.Warn("using the fake AI provider (development only)")
		return learn.FakeAI{}, "fake"
	case "gemini":
		g, err := learn.NewGeminiAI(context.Background(), cfg.GeminiAPIKey, cfg.AIDigestModel, cfg.AIOrganiseModel, cfg.AIEnrichModel)
		if err != nil || g == nil {
			slog.Error("AI_PROVIDER=gemini but Gemini is not usable (GEMINI_API_KEY?)", "error", err)
			return learn.NewClaudeAI("", "", "", ""), "none"
		}
		return g, "gemini"
	case "claude":
		if cfg.AnthropicAPIKey == "" {
			slog.Warn("AI_PROVIDER=claude but ANTHROPIC_API_KEY is not set: material reading and AI organising are off")
			return learn.NewClaudeAI("", "", "", ""), "none"
		}
		claude := learn.NewClaudeAI(cfg.AnthropicAPIKey, cfg.AIDigestModel, cfg.AIOrganiseModel, cfg.AIEnrichModel)
		// When Claude's credit runs out, Gemini (default models) takes over
		if g, err := learn.NewGeminiAI(context.Background(), cfg.GeminiAPIKey, "", "", ""); err == nil && g != nil {
			if full, ok := claude.(interface {
				learn.AI
				learn.Enricher
				learn.Examiner
			}); ok {
				slog.Info("Gemini is the backup AI when Claude is out of credit")
				return learn.FallbackAI{Primary: full, Backup: g}, "claude"
			}
		}
		return claude, "claude"
	}
	slog.Warn("no AI key set: material reading and AI organising are off")
	return learn.NewClaudeAI("", "", "", ""), "none"
}

// wwwTwin turns https://www.x.com into https://x.com and back.
func wwwTwin(u string) string {
	if strings.Contains(u, "://www.") {
		return strings.Replace(u, "://www.", "://", 1)
	}
	return strings.Replace(u, "://", "://www.", 1)
}

func health(gdb *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		sqlDB, err := gdb.DB()
		if err == nil {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
			defer cancel()
			err = sqlDB.PingContext(ctx)
		}
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy", "database": "down"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "healthy", "service": "focusflow-api", "version": version, "database": "up"})
	}
}

func purgeLoop(s *auth.Service) {
	for {
		s.PurgeExpired()
		time.Sleep(time.Hour)
	}
}

func healthcheck() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8091"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/health")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
