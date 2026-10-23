// Package services contains the business logic layer for the Course Material Service.
// Services orchestrate validation, business rules, and delegation to the repository layer.
package services

import (
	"fmt"
	"focusflow-api/internal/material/model"
	"log/slog"
	"sort"
)

// quizPreEagerCount is the maximum number of lessons we eagerly pre-generate
// quizzes for at bulk-insert time. The cap protects the Gemini daily quota
// — the rest of the lessons rely on lazy generation when the learner opens
// them in the player.
const quizPreEagerCount = 2

// CourseMaterialService encapsulates the business logic for course material
// CRUD operations. After persistence it kicks off two best-effort background
// jobs (if their dependencies are wired):
//
//  1. Quiz pre-generation for the first `quizPreEagerCount` lessons in the
//     course — hides the ~15 s Gemini latency from the learner the moment
//     they reach Mark-as-complete on lesson 1 or 2.
//  2. Transcript extraction for EVERY newly-added lesson — every Feynman
//     reflection later submitted for these lessons hits the cheap text-only
//     analysis path instead of incurring a second video-ingestion call.
//
// Both jobs are fire-and-forget; failures are logged but never block the
// CRUD response.
type CourseMaterialService struct {
	repo     model.CourseMaterialRepository
	preparer QuizPreparer     // optional — nil when Gemini is disabled
	warmer   TranscriptWarmer // optional — nil when Gemini is disabled
}

// NewCourseMaterialService creates a new service instance. Passing a nil
// QuizPreparer disables eager quiz pre-generation; passing a nil
// TranscriptWarmer disables transcript pre-extraction. Both fall back to
// lazy generation at request time when nil.
func NewCourseMaterialService(
	repo model.CourseMaterialRepository,
	preparer QuizPreparer,
	warmer TranscriptWarmer,
) *CourseMaterialService {
	return &CourseMaterialService{
		repo:     repo,
		preparer: preparer,
		warmer:   warmer,
	}
}

// CreateCourseMaterials validates and persists a batch of course materials.
// It enforces that every item has a course_id, title, and youtube_id before saving.
// If any single item fails validation or persistence, the entire batch is rejected.
func (s *CourseMaterialService) CreateCourseMaterials(cms []model.CourseMaterial) ([]model.CourseMaterial, error) {
	if len(cms) == 0 {
		return nil, fmt.Errorf("no course materials provided")
	}

	var saved []model.CourseMaterial

	for _, cm := range cms {
		// Validate required fields before delegating to the repository.
		if cm.CourseID == "" {
			return nil, fmt.Errorf("course material must belong to a course (course_id is required)")
		}
		if cm.Title == "" {
			return nil, fmt.Errorf("course material title cannot be empty")
		}
		if cm.YouTubeID == "" {
			return nil, fmt.Errorf("course material must have a YouTube ID")
		}

		// Persist each validated course material to the database.
		result, err := s.repo.CreateCourseMaterial(&cm)
		if err != nil {
			return nil, fmt.Errorf("failed to save course material '%s': %w", cm.Title, err)
		}

		saved = append(saved, result)
	}

	// Fire eager pre-generation for the first quizPreEagerCount lessons of
	// the course (across all batches — not just this insert), so learners
	// can complete lesson 1 and 2 without waiting on Gemini.
	s.preGenerateEarlyLessons(saved)

	// Fire transcript extraction for every newly-added lesson. Each call is
	// internally coalesced (WarmAsync is idempotent), so repeated batches
	// for the same lesson don't multiply Gemini calls.
	s.preWarmTranscripts(saved)

	return saved, nil
}

// preWarmTranscripts fires a background transcript extraction for every
// newly-added lesson. By the time the learner reaches the reflection page,
// the transcript is cached and the analysis runs on the cheap text-only
// Gemini tier instead of re-ingesting the video.
//
// Like preGenerateEarlyLessons, this is best-effort: TranscriptService
// internally logs failures and the reflection service falls back to the
// video-grounded analysis path if extraction is missing at submit time.
func (s *CourseMaterialService) preWarmTranscripts(saved []model.CourseMaterial) {
	if s.warmer == nil || len(saved) == 0 {
		return
	}
	for _, cm := range saved {
		if cm.VideoID <= 0 || cm.YouTubeID == "" {
			continue
		}
		s.warmer.WarmAsync(cm.VideoID)
	}
}

// preGenerateEarlyLessons triggers idempotent background quiz generation
// for the lessons with the lowest video IDs (proxy for "first added") in
// each course touched by the current insert. Calls are non-blocking and
// errors are logged but never surfaced — eager generation is a best-effort
// optimisation, the lazy `prepare` hook on the player is the real
// guarantee.
func (s *CourseMaterialService) preGenerateEarlyLessons(saved []model.CourseMaterial) {
	if s.preparer == nil || len(saved) == 0 {
		return
	}

	// Group by course so a multi-course insert (rare today, but cheap to
	// support) gets its first two lessons in each course pre-generated.
	byCourse := make(map[string][]model.CourseMaterial)
	for _, cm := range saved {
		byCourse[cm.CourseID] = append(byCourse[cm.CourseID], cm)
	}

	for courseID, batch := range byCourse {
		// Use the repo to find ALL materials for this course — we want the
		// first two by ordering across the entire course, not just this batch.
		all, err := s.repo.GetCourseMaterialsByCourseID(courseID)
		if err != nil {
			slog.Warn("pre-gen: list-by-course failed",
				"component", "course-material",
				"course_id", courseID,
				"error", err,
			)
			continue
		}
		if len(all) == 0 {
			all = batch
		}

		sort.Slice(all, func(i, j int) bool { return all[i].VideoID < all[j].VideoID })
		limit := quizPreEagerCount
		if len(all) < limit {
			limit = len(all)
		}

		for i := 0; i < limit; i++ {
			vid := all[i].VideoID
			go func(v int) {
				if _, err := s.preparer.Prepare(v); err != nil {
					slog.Warn("pre-gen: prepare failed",
						"component", "course-material",
						"video_id", v,
						"error", err,
					)
				}
			}(vid)
		}
	}
}

// GetCourseMaterialByID retrieves a single course material by its video ID.
// Returns an error if the video ID is invalid (zero or negative).
func (s *CourseMaterialService) GetCourseMaterialByID(videoID int) (model.CourseMaterial, error) {
	if videoID <= 0 {
		return model.CourseMaterial{}, fmt.Errorf("video_id must be a positive integer")
	}

	result, err := s.repo.GetCourseMaterialByID(videoID)
	if err != nil {
		return model.CourseMaterial{}, fmt.Errorf("failed to retrieve course material: %w", err)
	}

	return result, nil
}

// GetCourseMaterialsByCourseID retrieves all course materials belonging to the given course.
// Returns an error if course ID is empty.
func (s *CourseMaterialService) GetCourseMaterialsByCourseID(courseID string) ([]model.CourseMaterial, error) {
	if courseID == "" {
		return nil, fmt.Errorf("course_id cannot be empty")
	}

	results, err := s.repo.GetCourseMaterialsByCourseID(courseID)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve course materials: %w", err)
	}

	// Return an empty slice (not nil) for consistent JSON output.
	if len(results) == 0 {
		return []model.CourseMaterial{}, nil
	}

	return results, nil
}

// UpdateCourseMaterial validates and updates an existing course material.
// The video ID must be positive, and the essential fields must remain populated.
func (s *CourseMaterialService) UpdateCourseMaterial(cm model.CourseMaterial) (model.CourseMaterial, error) {
	if cm.VideoID <= 0 {
		return model.CourseMaterial{}, fmt.Errorf("video_id must be a positive integer")
	}
	if cm.CourseID == "" {
		return model.CourseMaterial{}, fmt.Errorf("course material must belong to a course (course_id is required)")
	}
	if cm.Title == "" {
		return model.CourseMaterial{}, fmt.Errorf("course material title cannot be empty")
	}
	if cm.YouTubeID == "" {
		return model.CourseMaterial{}, fmt.Errorf("course material must have a YouTube ID")
	}

	// Verify the record exists before attempting the update.
	_, err := s.repo.GetCourseMaterialByID(cm.VideoID)
	if err != nil {
		return model.CourseMaterial{}, fmt.Errorf("course material not found: %w", err)
	}

	// Delegate the update to the repository layer.
	result, err := s.repo.UpdateCourseMaterial(&cm)
	if err != nil {
		return model.CourseMaterial{}, fmt.Errorf("failed to update course material: %w", err)
	}

	return result, nil
}

// DeleteCourseMaterial removes a course material by its video ID.
// Returns an error if the video ID is invalid or the record does not exist.
func (s *CourseMaterialService) DeleteCourseMaterial(videoID int) error {
	if videoID <= 0 {
		return fmt.Errorf("video_id must be a positive integer")
	}

	// Verify the record exists before attempting deletion.
	_, err := s.repo.GetCourseMaterialByID(videoID)
	if err != nil {
		return fmt.Errorf("course material not found: %w", err)
	}

	if err := s.repo.DeleteCourseMaterial(videoID); err != nil {
		return fmt.Errorf("failed to delete course material: %w", err)
	}

	return nil
}
