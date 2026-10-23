// Package services contains the business-logic layer for the Course Material Service.
package services

import (
	"fmt"
	"focusflow-api/internal/material/model"
	"strings"
	"time"
)

// Active-recall gate validation constants. See US-001 acceptance criteria.
const (
	TakeawayMinChars = 30
	TakeawayMaxChars = 1000
)

// TakeawayService applies business rules around the active-recall gate and
// orchestrates persistence of takeaways and lesson completion.
type TakeawayService struct {
	takeawayRepo model.TakeawayRepository
	materialRepo model.CourseMaterialRepository
}

// NewTakeawayService creates a new service. It needs both repositories because
// submitting a takeaway atomically marks the related CourseMaterial complete.
func NewTakeawayService(takeawayRepo model.TakeawayRepository, materialRepo model.CourseMaterialRepository) *TakeawayService {
	return &TakeawayService{
		takeawayRepo: takeawayRepo,
		materialRepo: materialRepo,
	}
}

// SubmitTakeaway validates the body, upserts the takeaway, and flips the
// related CourseMaterial to completed. It returns the updated CourseMaterial.
//
// Business rules:
//   - Body must be TakeawayMinChars..TakeawayMaxChars characters (trimmed).
//   - UserID and VideoID must be positive integers.
//   - The lesson must exist; otherwise we return a not-found error.
//   - Re-submitting replaces the body rather than creating duplicates.
func (s *TakeawayService) SubmitTakeaway(userID, videoID int, body string) (model.CourseMaterial, model.Takeaway, error) {
	if userID <= 0 {
		return model.CourseMaterial{}, model.Takeaway{}, fmt.Errorf("user_id must be a positive integer")
	}
	if videoID <= 0 {
		return model.CourseMaterial{}, model.Takeaway{}, fmt.Errorf("video_id must be a positive integer")
	}

	trimmed := strings.TrimSpace(body)
	if len(trimmed) < TakeawayMinChars {
		return model.CourseMaterial{}, model.Takeaway{}, fmt.Errorf("takeaway must be at least %d characters", TakeawayMinChars)
	}
	if len(trimmed) > TakeawayMaxChars {
		return model.CourseMaterial{}, model.Takeaway{}, fmt.Errorf("takeaway must be at most %d characters", TakeawayMaxChars)
	}

	cm, err := s.materialRepo.GetCourseMaterialByID(videoID)
	if err != nil {
		return model.CourseMaterial{}, model.Takeaway{}, fmt.Errorf("lesson not found: %w", err)
	}

	t := &model.Takeaway{
		VideoID:   videoID,
		UserID:    userID,
		Body:      trimmed,
		WordCount: countWords(trimmed),
	}
	saved, err := s.takeawayRepo.Upsert(t)
	if err != nil {
		return model.CourseMaterial{}, model.Takeaway{}, fmt.Errorf("failed to save takeaway: %w", err)
	}

	now := time.Now().UTC()
	cm.Completed = true
	cm.CompletedAt = &now
	updated, err := s.materialRepo.UpdateCourseMaterial(&cm)
	if err != nil {
		return model.CourseMaterial{}, saved, fmt.Errorf("takeaway saved but lesson completion failed: %w", err)
	}

	return updated, saved, nil
}

// GetTakeaway returns the user's prior takeaway for a lesson, if any.
// Returns an empty Takeaway and nil error when none exists, so the caller
// can render an empty-state without treating it as an error.
func (s *TakeawayService) GetTakeaway(userID, videoID int) (model.Takeaway, error) {
	if userID <= 0 || videoID <= 0 {
		return model.Takeaway{}, fmt.Errorf("user_id and video_id must be positive integers")
	}
	t, err := s.takeawayRepo.GetByUserAndVideo(userID, videoID)
	if err != nil {
		// Empty result is normal; the handler will translate to 200 + null body.
		return model.Takeaway{}, nil
	}
	return t, nil
}

// GetTakeawaysForCourse returns every takeaway the user has written for the
// lessons in the given course.
func (s *TakeawayService) GetTakeawaysForCourse(userID int, courseID string) ([]model.Takeaway, error) {
	if userID <= 0 {
		return nil, fmt.Errorf("user_id must be a positive integer")
	}
	if courseID == "" {
		return nil, fmt.Errorf("course_id is required")
	}
	results, err := s.takeawayRepo.GetByUserAndCourse(userID, courseID)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return []model.Takeaway{}, nil
	}
	return results, nil
}

// countWords returns the number of whitespace-delimited tokens in s.
func countWords(s string) int {
	if strings.TrimSpace(s) == "" {
		return 0
	}
	return len(strings.Fields(s))
}
