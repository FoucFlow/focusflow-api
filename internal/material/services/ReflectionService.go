// Package services contains the business-logic layer for the Course Material Service.
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"focusflow-api/internal/material/model"
	"focusflow-api/internal/material/repository"
	"log/slog"
	"strings"
	"time"
)

// asyncReflectionGradingTimeout caps how long the background goroutine is
// permitted to run before we abort and mark the reflection failed. Generous
// because the video-grounded fallback path can take ~120 s on a long lesson.
const asyncReflectionGradingTimeout = 150 * time.Second

// Feynman-technique reflection validation constants.
const (
	ReflectionMinChars = 30
	ReflectionMaxChars = 3000
)

// masteryThreshold is the minimum score at which a lesson is considered mastered.
// The service enforces this rather than trusting the model's mastered field,
// so score is always the single source of truth.
const masteryThreshold = 85

// ReflectionService applies business rules around Feynman-technique reflections
// and orchestrates persistence of reflection rows.
type ReflectionService struct {
	reflectionRepo    model.ReflectionRepository
	materialRepo      model.CourseMaterialRepository
	geminiRepo        repository.GeminiRepository
	transcriptService *TranscriptService
}

// NewReflectionService creates a new service. geminiRepo may be nil when
// GEMINI_API_KEY is not configured; Submit will return a service-unavailable
// error in that case. GetHistory and GetLatest are never gated on Gemini.
// transcriptService may be nil; when nil the service falls back directly to
// the video-grounded analysis path on every submit (Phase 1 behaviour).
func NewReflectionService(
	reflectionRepo model.ReflectionRepository,
	materialRepo model.CourseMaterialRepository,
	geminiRepo repository.GeminiRepository,
	transcriptService *TranscriptService,
) *ReflectionService {
	return &ReflectionService{
		reflectionRepo:    reflectionRepo,
		materialRepo:      materialRepo,
		geminiRepo:        geminiRepo,
		transcriptService: transcriptService,
	}
}

// Submit validates the body, inserts a Reflection row with status="evaluating",
// fires a background goroutine for Gemini analysis, and returns immediately.
// The returned row has the body + word_count populated; score/strengths/gaps
// are zero-valued until the goroutine flips status to "completed" (or "failed").
//
// Multiple calls for the same (user, lesson) always create new rows — history
// is intentionally append-only.
//
// Analysis strategy inside the goroutine (in priority order):
//  1. Transcript cache hit  → AnalyzeReflectionFromTranscript (text-only, fast).
//  2. Transcript cache miss → extract + cache + AnalyzeReflectionFromTranscript.
//  3. Extraction failure   → fallback to video-grounded AnalyzeReflection.
//
// Business rules:
//   - userID and videoID must be positive integers.
//   - Body is trimmed; after trimming it must be ReflectionMinChars..ReflectionMaxChars chars.
//   - The lesson must exist in CourseMaterials; otherwise a "lesson not found" error is returned.
//   - The lesson must have a non-empty YouTubeID; otherwise "lesson has no video" is returned.
//   - Mastery is computed server-side as Score >= 85; the model's own mastered field is ignored.
func (s *ReflectionService) Submit(ctx context.Context, userID, videoID int, body string) (model.Reflection, error) {
	if userID <= 0 {
		return model.Reflection{}, fmt.Errorf("user_id must be a positive integer")
	}
	if videoID <= 0 {
		return model.Reflection{}, fmt.Errorf("video_id must be a positive integer")
	}

	trimmed := strings.TrimSpace(body)
	if len(trimmed) < ReflectionMinChars {
		return model.Reflection{}, fmt.Errorf("reflection body must be 30..3000 chars (got %d)", len(trimmed))
	}
	if len(trimmed) > ReflectionMaxChars {
		return model.Reflection{}, fmt.Errorf("reflection body must be 30..3000 chars (got %d)", len(trimmed))
	}

	cm, err := s.materialRepo.GetCourseMaterialByID(videoID)
	if err != nil {
		return model.Reflection{}, fmt.Errorf("lesson not found: %w", err)
	}

	if cm.YouTubeID == "" {
		return model.Reflection{}, fmt.Errorf("lesson has no video")
	}

	if s.geminiRepo == nil {
		return model.Reflection{}, fmt.Errorf("gemini service unavailable: GEMINI_API_KEY not configured")
	}

	// Initialise empty JSONB arrays so the API returns [] rather than null
	// for the evaluating placeholder row.
	emptyJSON, _ := json.Marshal([]string{})

	ref := &model.Reflection{
		VideoID:   videoID,
		UserID:    userID,
		Body:      trimmed,
		Status:    model.ReflectionStatusEvaluating,
		Strengths: emptyJSON,
		Gaps:      emptyJSON,
		WordCount: countWords(trimmed),
	}

	saved, err := s.reflectionRepo.Create(ref)
	if err != nil {
		return model.Reflection{}, fmt.Errorf("failed to save reflection: %w", err)
	}

	// Spawn the background grader. We MUST NOT pass the request context — it
	// is cancelled the moment this handler returns. Use a fresh background
	// context with its own timeout.
	go s.gradeInBackground(saved.ID, cm, trimmed, videoID)

	return saved, nil
}

// gradeInBackground runs the Gemini analysis off-request. On success it updates
// the row with score/strengths/gaps/etc. and flips status to "completed".
// On failure it sets status="failed" + error_message so the frontend can show
// a meaningful retry message. Errors here never propagate to the user — the
// learner sees only the row, which they can poll.
func (s *ReflectionService) gradeInBackground(
	reflectionID int,
	cm model.CourseMaterial,
	userExplanation string,
	videoID int,
) {
	ctx, cancel := context.WithTimeout(context.Background(), asyncReflectionGradingTimeout)
	defer cancel()

	analysis, err := s.analyze(ctx, cm, userExplanation, videoID)

	// Re-fetch so we don't clobber any columns that may have changed since insert.
	ref, fetchErr := s.reflectionRepo.GetByID(reflectionID)
	if fetchErr != nil {
		slog.Error("background grade: cannot re-fetch row",
			"component", "reflection",
			"reflection_id", reflectionID,
			"error", fetchErr,
		)
		return
	}

	now := time.Now()
	ref.EvaluatedAt = &now

	if err != nil {
		ref.Status = model.ReflectionStatusFailed
		ref.ErrorMessage = truncateMsg(err.Error(), 500)
		if _, updErr := s.reflectionRepo.Update(&ref); updErr != nil {
			slog.Error("background grade: failed to mark row as failed",
				"component", "reflection",
				"reflection_id", reflectionID,
				"error", updErr,
			)
		}
		slog.Error("background grade: row failed",
			"component", "reflection",
			"reflection_id", reflectionID,
			"error", err,
		)
		return
	}

	// json.Marshal never errors on []string.
	strengthsJSON, _ := json.Marshal(analysis.Strengths)
	gapsJSON, _ := json.Marshal(analysis.Gaps)

	ref.Status = model.ReflectionStatusCompleted
	ref.Score = analysis.Score
	ref.Strengths = strengthsJSON
	ref.Gaps = gapsJSON
	ref.SuggestedNextFocus = analysis.SuggestedNextFocus
	// Score is the source of truth for mastery; do not trust the model's field.
	ref.Mastered = analysis.Score >= masteryThreshold
	ref.ErrorMessage = ""

	if _, err := s.reflectionRepo.Update(&ref); err != nil {
		slog.Error("background grade: failed to persist completed row",
			"component", "reflection",
			"reflection_id", reflectionID,
			"error", err,
		)
		return
	}
}

// truncateMsg is a tiny helper for bounding the error_message column so a
// pathological Gemini error doesn't write a 100 KB row.
func truncateMsg(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// analyze chooses the cheapest available analysis path:
//  1. Transcript cache hit  → AnalyzeReflectionFromTranscript (text-only, fast).
//  2. Transcript cache miss → extract + cache + AnalyzeReflectionFromTranscript.
//  3. Extraction failure   → fallback to video-grounded AnalyzeReflection.
//
// The video-grounded fallback is preserved so that a Gemini video-extraction
// outage never silently drops a learner's submission.
func (s *ReflectionService) analyze(
	ctx context.Context,
	cm model.CourseMaterial,
	userExplanation string,
	videoID int,
) (repository.ReflectionAnalysis, error) {
	// Fast path: use the transcript cache when TranscriptService is wired.
	if s.transcriptService != nil {
		transcript, err := s.transcriptService.GetOrExtract(ctx, videoID)
		if err == nil && transcript != "" {
			return s.geminiRepo.AnalyzeReflectionFromTranscript(
				ctx,
				cm.Title,
				cm.Description,
				transcript,
				userExplanation,
			)
		}
		// Extraction failed — log and fall through to the video-grounded path.
		slog.Warn("transcript extraction failed, falling back to video-grounded analysis",
			"component", "reflection",
			"video_id", videoID,
			"error", err,
		)
	}

	// Last-resort fallback: video-grounded analysis (slow, expensive).
	videoURL := "https://www.youtube.com/watch?v=" + cm.YouTubeID
	return s.geminiRepo.AnalyzeReflection(ctx, cm.Title, cm.Description, videoURL, userExplanation)
}

// GetHistory returns all of the user's reflections for a given lesson, ordered
// oldest-first. An empty slice is normal when no reflections have been submitted.
func (s *ReflectionService) GetHistory(userID, videoID int) ([]model.Reflection, error) {
	if userID <= 0 {
		return nil, fmt.Errorf("user_id must be a positive integer")
	}
	if videoID <= 0 {
		return nil, fmt.Errorf("video_id must be a positive integer")
	}
	rows, err := s.reflectionRepo.GetByUserAndVideo(userID, videoID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []model.Reflection{}, nil
	}
	return rows, nil
}

// GetLatest returns the most-recent reflection the user submitted for the
// lesson. A not-found result is a normal case: the caller returns {data: null}
// to the frontend rather than a 404.
func (s *ReflectionService) GetLatest(userID, videoID int) (model.Reflection, error) {
	if userID <= 0 {
		return model.Reflection{}, fmt.Errorf("user_id must be a positive integer")
	}
	if videoID <= 0 {
		return model.Reflection{}, fmt.Errorf("video_id must be a positive integer")
	}
	return s.reflectionRepo.GetLatestByUserAndVideo(userID, videoID)
}
