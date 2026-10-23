// Package handler implements the HTTP transport layer for the Course Material Service.
package handler

import (
	"errors"
	"focusflow-api/internal/auth"
	"focusflow-api/internal/material/services"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// classifyGenerateError inspects a generation error and returns a (reason,
// human-readable message) pair the frontend can show to the learner.
//
// Note: as of v2 we no longer surface a `too_long` classification — long
// videos are auto-clipped to ~25 minutes by GeminiRepository (see
// maxClippedVideoDuration). Token-count errors therefore no longer occur
// in practice; if they ever do they fall through to `generation_failed`.
func classifyGenerateError(err error) (reason, message string) {
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "experiencing high demand") ||
		strings.Contains(lower, "currently unavailable") ||
		strings.Contains(lower, "service is unavailable"):
		return "service_busy",
			"Gemini is busy right now. This usually clears in under a minute — try again."
	case strings.Contains(lower, "quota") || strings.Contains(lower, "rate limit"):
		return "quota_exceeded",
			"Today's quiz-generation quota is exhausted. Try again after the daily reset."
	case strings.Contains(lower, "video") && strings.Contains(lower, "not"):
		return "video_unavailable",
			"Gemini couldn't access this YouTube video. It may be private, age-restricted, or deleted."
	default:
		return "generation_failed",
			"We couldn't generate a quiz for this lesson. You can continue with the short takeaway."
	}
}

// QuizHandler exposes the US-026 endpoints: idempotent generate, list,
// submit attempt, fetch latest attempt.
//
// When service is nil (Gemini disabled at boot), every endpoint replies
// HTTP 503. The frontend interprets that as "fall back to US-001
// takeaway-only dialog".
type QuizHandler struct {
	service           *services.QuizService
	transcriptService *services.TranscriptService
}

// NewQuizHandler binds a handler to its service. Passing a nil service
// puts the handler into fallback mode (see type doc).
//
// transcriptService may be nil — when nil the handler skips transcript
// pre-warming (preserving behaviour when Gemini is not configured).
func NewQuizHandler(service *services.QuizService, transcriptService *services.TranscriptService) *QuizHandler {
	return &QuizHandler{service: service, transcriptService: transcriptService}
}

// disabled returns true when this handler was constructed without a quiz
// service (Gemini wasn't configured). Every endpoint short-circuits with
// a 503 in that case.
func (h *QuizHandler) disabled() bool { return h.service == nil }

func (h *QuizHandler) reply503(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"error": "quiz feature is disabled on this deployment (GEMINI_API_KEY not configured)",
	})
}

// GenerateQuiz godoc
// @Summary      Idempotent: create or fetch the 6 quiz questions for a lesson
// @Description  First call on a lesson generates 3 MCQ + 3 writing questions via Gemini and persists them. Subsequent calls return the cached set with zero LLM cost.
// @Tags         Quiz
// @Produce      json
// @Param        id   path      int  true  "Video ID (lesson)"
// @Success      201  {object}  map[string]interface{}
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  map[string]interface{}
// @Failure      503  {object}  map[string]interface{}
// @Router       /course-materials/{id}/quiz/generate [post]
func (h *QuizHandler) GenerateQuiz(c *gin.Context) {
	if h.disabled() {
		h.reply503(c)
		return
	}
	videoID, err := strconv.Atoi(c.Param("id"))
	if err != nil || videoID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid video_id"})
		return
	}

	if _, err := h.service.GenerateOrFetch(c.Request.Context(), videoID); err != nil {
		// Gemini failures land here. Classify the cause so the frontend
		// can show a meaningful message and the right next-step buttons.
		reason, message := classifyGenerateError(err)
		slog.Error("quiz generation failed",
			"video_id", videoID,
			"reason", reason,
			"error", err,
			"request_id", c.GetString("request_id"),
		)
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "quiz unavailable for this lesson",
			"reason":  reason,  // machine-readable: too_long | service_busy | generation_failed
			"message": message, // human-readable, safe to show in the UI
			"details": err.Error(),
		})
		return
	}

	// Pre-warm the transcript cache in the background. By the time the learner
	// finishes the quiz and submits a Feynman reflection, the transcript will
	// usually be ready — eliminating the per-reflection video-ingestion wait.
	// Skip when transcriptService is nil (Gemini not configured).
	if h.transcriptService != nil {
		h.transcriptService.WarmAsync(videoID)
	}

	// After generation we always serve the redacted version (correct_index
	// and rubric stripped), so the frontend has one consistent shape for
	// both first-run and cache-hit paths.
	qs, err := h.service.ListForTaking(videoID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "quiz ready",
		"count":   len(qs),
		"data":    qs,
	})
}

// ListQuiz godoc
// @Summary      List the questions a learner will see (no answers leaked)
// @Description  Returns the 6 questions in stepper order with correct_index and rubric stripped, so the frontend cannot show the answer key.
// @Tags         Quiz
// @Produce      json
// @Param        id   path      int  true  "Video ID (lesson)"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  map[string]interface{}
// @Router       /course-materials/{id}/quiz [get]
func (h *QuizHandler) ListQuiz(c *gin.Context) {
	if h.disabled() {
		h.reply503(c)
		return
	}
	videoID, err := strconv.Atoi(c.Param("id"))
	if err != nil || videoID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid video_id"})
		return
	}

	qs, err := h.service.ListForTaking(videoID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"count": len(qs), "data": qs})
}

// SubmitAttempt godoc
// @Summary      Submit a full quiz attempt (6 answers + takeaway), grade it, persist it
// @Description  Atomic: grades MCQ deterministically, grades writing via Gemini, upserts the US-001 takeaway, persists the QuizAttempt + 6 QuizAnswers, flips the lesson to completed. Returns the full graded attempt for the step-9 results screen.
// @Tags         Quiz
// @Accept       json
// @Produce      json
// @Param        id    path      int                            true  "Video ID (lesson)"
// @Param        body  body      services.SubmitAttemptInput    true  "Attempt payload"
// @Success      201   {object}  map[string]interface{}
// @Failure      400   {object}  map[string]interface{}
// @Failure      422   {object}  map[string]interface{}
// @Router       /course-materials/{id}/quiz/attempts [post]
func (h *QuizHandler) SubmitAttempt(c *gin.Context) {
	if h.disabled() {
		h.reply503(c)
		return
	}
	videoID, err := strconv.Atoi(c.Param("id"))
	if err != nil || videoID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid video_id"})
		return
	}

	var in services.SubmitAttemptInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body", "details": err.Error()})
		return
	}
	in.UserID = c.GetInt(auth.CtxUserID)

	// CreateAttempt is synchronous for the easy parts (MCQ scoring, takeaway,
	// row insert) but spawns a goroutine for the Gemini writing grader. The
	// response carries an attempt_id with status='evaluating'. The frontend
	// navigates to /courses/quiz/<attemptId> and polls GetAttempt until the
	// status flips to 'completed'.
	attempt, err := h.service.CreateAttempt(videoID, in)
	if err != nil {
		// Sequential-progression lock — return a structured 409 so the
		// frontend can deep-link the learner to the blocking lesson.
		var lockErr *services.SequentialLockError
		if errors.As(err, &lockErr) {
			slog.Warn("sequential lock blocked attempt",
				"video_id", videoID,
				"user_id", in.UserID,
				"blocking_video_id", lockErr.BlockingVideoID,
				"request_id", c.GetString("request_id"),
			)
			c.JSON(http.StatusConflict, gin.H{
				"error":             "sequential_lock",
				"message":           lockErr.Error(),
				"blocking_title":    lockErr.BlockingTitle,
				"blocking_video_id": lockErr.BlockingVideoID,
			})
			return
		}
		slog.Error("quiz attempt submission failed",
			"video_id", videoID,
			"user_id", in.UserID,
			"error", err,
			"request_id", c.GetString("request_id"),
		)
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{
		"message": "attempt accepted; grading in progress",
		"data":    attempt,
	})
}

// PrepareQuiz godoc
// @Summary      Pre-generate the quiz for a lesson in the background
// @Description  Fire-and-forget endpoint. Returns 202 immediately. If questions don't exist yet, spawns a goroutine that calls Gemini. The frontend calls this when the learner selects a lesson so by the time they click Mark-as-complete the quiz is already cached.
// @Tags         Quiz
// @Produce      json
// @Param        id   path      int  true  "Video ID (lesson)"
// @Success      202  {object}  map[string]interface{}
// @Failure      400  {object}  map[string]interface{}
// @Router       /course-materials/{id}/quiz/prepare [post]
func (h *QuizHandler) PrepareQuiz(c *gin.Context) {
	if h.disabled() {
		h.reply503(c)
		return
	}
	videoID, err := strconv.Atoi(c.Param("id"))
	if err != nil || videoID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid video_id"})
		return
	}
	status, err := h.service.Prepare(videoID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"data": status})
}

// QuizStatus godoc
// @Summary      Cheap status poll: are the questions ready yet?
// @Description  Returns `{ready, generating, error}`. Never calls Gemini. Used by the frontend to gate the Mark-as-complete button while background generation is in flight.
// @Tags         Quiz
// @Produce      json
// @Param        id   path      int  true  "Video ID (lesson)"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  map[string]interface{}
// @Router       /course-materials/{id}/quiz/status [get]
func (h *QuizHandler) QuizStatus(c *gin.Context) {
	if h.disabled() {
		h.reply503(c)
		return
	}
	videoID, err := strconv.Atoi(c.Param("id"))
	if err != nil || videoID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid video_id"})
		return
	}
	status, err := h.service.Status(videoID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": status})
}

// GetAttempt godoc
// @Summary      Poll a quiz attempt by id (results-page feed)
// @Description  Returns the attempt with all answer line items eager-loaded. While `status="evaluating"` the writing answers carry placeholder feedback; the frontend polls until status flips to `completed` or `failed`.
// @Tags         Quiz
// @Produce      json
// @Param        id  path  int  true  "Attempt ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  map[string]interface{}
// @Router       /quiz-attempts/{id} [get]
func (h *QuizHandler) GetAttempt(c *gin.Context) {
	if h.disabled() {
		h.reply503(c)
		return
	}
	attemptID, err := strconv.Atoi(c.Param("id"))
	if err != nil || attemptID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid attempt_id"})
		return
	}
	attempt, err := h.service.GetAttempt(attemptID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "attempt not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": attempt})
}

// RegenerateQuiz godoc
// @Summary      Generate another quiz set for a lesson (covers next 25-min window)
// @Description  Creates a new lesson_questions set with set_number = N+1 and start_offset = (N-1)*25min. Useful when the first quiz only covered the opening segment of a long video, or when the learner wants fresh questions on the same content for varied practice.
// @Tags         Quiz
// @Produce      json
// @Param        id   path      int  true  "Video ID (lesson)"
// @Success      201  {object}  map[string]interface{}
// @Failure      400  {object}  map[string]interface{}
// @Failure      503  {object}  map[string]interface{}
// @Router       /course-materials/{id}/quiz/regenerate [post]
func (h *QuizHandler) RegenerateQuiz(c *gin.Context) {
	if h.disabled() {
		h.reply503(c)
		return
	}
	videoID, err := strconv.Atoi(c.Param("id"))
	if err != nil || videoID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid video_id"})
		return
	}
	qs, err := h.service.Regenerate(c.Request.Context(), videoID)
	if err != nil {
		reason, msg := classifyGenerateError(err)
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "quiz unavailable",
			"reason":  reason,
			"message": msg,
			"details": err.Error(),
		})
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"message": "new quiz set generated",
		"count":   len(qs),
		"data":    qs,
	})
}

// ListAttempts godoc
// @Summary      List the learner's quiz attempts (history page feed)
// @Description  Returns attempts newest-first, paginated, with lesson title + course id eager-resolved. Each attempt's answer line items are NOT included; fetch them via GetAttempt on row click.
// @Tags         Quiz
// @Produce      json
// @Param        user_id  query  int  true   "User ID"
// @Param        limit    query  int  false  "Page size (default 25, max 100)"
// @Param        offset   query  int  false  "Page offset (default 0)"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  map[string]interface{}
// @Router       /quiz-attempts [get]
func (h *QuizHandler) ListAttempts(c *gin.Context) {
	if h.disabled() {
		h.reply503(c)
		return
	}
	userID, err := sessionUserID(c)
	if err != nil || userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_id query param required"})
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))

	attempts, err := h.service.ListAttemptsForUser(userID, limit, offset)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"count": len(attempts),
		"data":  attempts,
	})
}

// LatestAttempt godoc
// @Summary      Get the learner's most recent attempt for a lesson
// @Description  Used by the "view results" screen on completed lessons.
// @Tags         Quiz
// @Produce      json
// @Param        id       path   int  true   "Video ID (lesson)"
// @Param        user_id  query  int  true   "User ID"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  map[string]interface{}
// @Failure      404      {object}  map[string]interface{}
// @Router       /course-materials/{id}/quiz/latest [get]
func (h *QuizHandler) LatestAttempt(c *gin.Context) {
	if h.disabled() {
		h.reply503(c)
		return
	}
	videoID, err := strconv.Atoi(c.Param("id"))
	if err != nil || videoID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid video_id"})
		return
	}
	userID, err := sessionUserID(c)
	if err != nil || userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_id query param required"})
		return
	}

	attempt, err := h.service.LatestForUser(userID, videoID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no attempt found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": attempt})
}
