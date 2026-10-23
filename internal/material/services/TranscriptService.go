// Package services contains the business-logic layer for the Course Material Service.
package services

import (
	"context"
	"errors"
	"fmt"
	"focusflow-api/internal/material/model"
	"focusflow-api/internal/material/repository"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

// transcriptWarmTimeout is the maximum time a WarmAsync goroutine is permitted
// to run. Five minutes is generous enough for slow Gemini video-ingestion on
// long videos while still guaranteeing the goroutine does not live forever if
// something hangs.
const transcriptWarmTimeout = 5 * time.Minute

// TranscriptService manages extraction and caching of lesson transcripts. Its
// primary purpose is to pre-warm the transcript cache when the learner starts a
// quiz, so that the subsequent Feynman reflection submission can use the cheap
// text-only analysis path instead of the expensive video-grounded one.
//
// In-process deduplication via warming is handled by a sync.Map — two
// concurrent quiz-generate requests for the same lesson will trigger at most
// one Gemini extraction call.
type TranscriptService struct {
	repo   model.LessonTranscriptRepository
	cmRepo model.CourseMaterialRepository
	gemini repository.GeminiRepository

	// warming tracks lessons for which a WarmAsync goroutine is currently
	// in-flight. The value is always bool(true); presence of the key is what
	// matters. Entries are deleted when the goroutine exits.
	warming sync.Map // map[int]bool
}

// NewTranscriptService wires the service to its dependencies.
func NewTranscriptService(
	repo model.LessonTranscriptRepository,
	cmRepo model.CourseMaterialRepository,
	gemini repository.GeminiRepository,
) *TranscriptService {
	return &TranscriptService{
		repo:   repo,
		cmRepo: cmRepo,
		gemini: gemini,
	}
}

// GetOrExtract returns the cached transcript for the lesson, extracting and
// persisting it via Gemini if no cache entry exists yet.
//
// This method is synchronous and may block for up to 120 s on a cache-miss
// while Gemini processes the video. Callers that want a non-blocking path
// should call WarmAsync ahead of time.
//
// Errors:
//   - lesson not found: the VideoID does not exist in course_materials.
//   - lesson has no video: the lesson's YouTubeID is empty.
//   - any Gemini or DB error propagated transparently.
func (s *TranscriptService) GetOrExtract(ctx context.Context, videoID int) (string, error) {
	// --- cache hit ---
	cached, err := s.repo.Get(videoID)
	if err == nil {
		return cached.Transcript, nil
	}
	// Any error other than record-not-found is a real DB failure.
	if !errors.Is(err, gorm.ErrRecordNotFound) && !isMissError(err) {
		return "", fmt.Errorf("transcript cache lookup failed: %w", err)
	}

	// --- cache miss: resolve the video URL ---
	cm, err := s.cmRepo.GetCourseMaterialByID(videoID)
	if err != nil {
		return "", fmt.Errorf("lesson not found: %w", err)
	}
	if cm.YouTubeID == "" {
		return "", fmt.Errorf("lesson has no video")
	}

	videoURL := "https://www.youtube.com/watch?v=" + cm.YouTubeID

	// --- extract via Gemini ---
	transcript, err := s.gemini.ExtractTranscript(ctx, videoURL)
	if err != nil {
		return "", fmt.Errorf("transcript extraction failed: %w", err)
	}

	// --- persist ---
	row := &model.LessonTranscript{
		VideoID:    videoID,
		Transcript: transcript,
		Source:     "gemini_extract",
		WordCount:  countWords(transcript),
	}
	if _, err := s.repo.Upsert(row); err != nil {
		// Non-fatal: the transcript was extracted successfully; the next call
		// will just have to re-extract. Log and return the transcript anyway.
		slog.Warn("cache persist failed",
			"component", "transcript",
			"video_id", videoID,
			"error", err,
		)
	}

	return transcript, nil
}

// WarmAsync fires a background goroutine that calls GetOrExtract for the
// lesson. It is safe to call repeatedly — if the cache is already warm or a
// goroutine for the same lesson is already in-flight, the call is a no-op.
//
// The goroutine uses context.Background() with a 5-minute timeout so it is not
// cancelled when the quiz-generate request context is closed (which happens as
// soon as the HTTP response is sent to the frontend).
//
// The goroutine never panics; all errors are logged.
func (s *TranscriptService) WarmAsync(videoID int) {
	// Coalesce: if a warm goroutine is already running for this lesson, skip.
	if _, alreadyRunning := s.warming.LoadOrStore(videoID, true); alreadyRunning {
		return
	}

	go func() {
		defer s.warming.Delete(videoID)

		ctx, cancel := context.WithTimeout(context.Background(), transcriptWarmTimeout)
		defer cancel()

		transcript, err := s.GetOrExtract(ctx, videoID)
		if err != nil {
			slog.Error("async warm failed",
				"component", "transcript",
				"video_id", videoID,
				"error", err,
			)
			return
		}
		slog.Info("async warm succeeded",
			"component", "transcript",
			"video_id", videoID,
			"words", len(strings.Fields(transcript)),
		)
	}()
}

// isMissError returns true if err's message indicates a record-not-found
// condition from a repository that wraps gorm.ErrRecordNotFound. We use this
// alongside errors.Is to handle the two flavours our repositories return.
func isMissError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "record not found") ||
		strings.Contains(msg, "not found")
}
