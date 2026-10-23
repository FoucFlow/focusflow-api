// Package repository implements the data access layer.
package repository

import (
	"fmt"
	"focusflow-api/internal/material/model"

	"gorm.io/gorm"
)

// QuizRepo is the PostgreSQL-backed implementation of model.QuizRepository.
type QuizRepo struct {
	db *gorm.DB
}

// NewQuizRepository wires the repo to a DB handle.
func NewQuizRepository(db *gorm.DB) *QuizRepo {
	return &QuizRepo{db: db}
}

// CreateAttempt persists the parent QuizAttempt and all its line-item
// QuizAnswer rows inside one transaction. The FK on QuizAnswer.AttemptID
// is populated by GORM's relation handling (Answers slice).
func (r *QuizRepo) CreateAttempt(attempt *model.QuizAttempt) (model.QuizAttempt, error) {
	if attempt == nil {
		return model.QuizAttempt{}, fmt.Errorf("attempt is nil")
	}

	if err := r.db.Transaction(func(tx *gorm.DB) error {
		return tx.Create(attempt).Error
	}); err != nil {
		return model.QuizAttempt{}, fmt.Errorf("create attempt failed: %w", err)
	}
	return *attempt, nil
}

// UpdateAttempt replaces the parent row's mutable fields AND the entire
// answers set. We delete + recreate answers rather than upserting because
// the grading goroutine produces a complete answer set from scratch — it
// doesn't merge with partial existing rows.
func (r *QuizRepo) UpdateAttempt(attempt *model.QuizAttempt) (model.QuizAttempt, error) {
	if attempt == nil || attempt.ID == 0 {
		return model.QuizAttempt{}, fmt.Errorf("attempt id is required")
	}

	if err := r.db.Transaction(func(tx *gorm.DB) error {
		// 1. Refresh the parent row's mutable fields.
		updates := map[string]interface{}{
			"status":        attempt.Status,
			"error_message": attempt.ErrorMessage,
			"total_score":   attempt.TotalScore,
			"mcq_score":     attempt.MCQScore,
			"writing_score": attempt.WritingScore,
			"updated_at":    attempt.UpdatedAt,
		}
		if attempt.TakeawayID != nil {
			updates["takeaway_id"] = *attempt.TakeawayID
		}
		if err := tx.Model(&model.QuizAttempt{}).Where("id = ?", attempt.ID).Updates(updates).Error; err != nil {
			return err
		}

		// 2. Replace answer line items.
		if err := tx.Where("attempt_id = ?", attempt.ID).Delete(&model.QuizAnswer{}).Error; err != nil {
			return err
		}
		if len(attempt.Answers) > 0 {
			for i := range attempt.Answers {
				attempt.Answers[i].AttemptID = attempt.ID
				attempt.Answers[i].ID = 0
			}
			if err := tx.Create(&attempt.Answers).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return model.QuizAttempt{}, fmt.Errorf("update attempt failed: %w", err)
	}
	return *attempt, nil
}

// GetAttemptByID returns one attempt with answer line items eager-loaded.
func (r *QuizRepo) GetAttemptByID(attemptID int) (model.QuizAttempt, error) {
	var attempt model.QuizAttempt
	if err := r.db.Preload("Answers").First(&attempt, attemptID).Error; err != nil {
		return model.QuizAttempt{}, fmt.Errorf("attempt %d not found: %w", attemptID, err)
	}
	return attempt, nil
}

// ListByUser returns a paginated, newest-first list of a learner's
// attempts WITHOUT answer line items (faster — the history view shows
// only summary scores).
func (r *QuizRepo) ListByUser(userID, limit, offset int) ([]model.QuizAttempt, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	if offset < 0 {
		offset = 0
	}
	var attempts []model.QuizAttempt
	if err := r.db.
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&attempts).Error; err != nil {
		return nil, fmt.Errorf("list attempts for user %d: %w", userID, err)
	}
	return attempts, nil
}

// LatestByUserVideo returns the most recent attempt for a learner on a
// lesson, with all answer line items eager-loaded.
func (r *QuizRepo) LatestByUserVideo(userID, videoID int) (model.QuizAttempt, error) {
	var attempt model.QuizAttempt
	if err := r.db.
		Preload("Answers").
		Where("user_id = ? AND video_id = ?", userID, videoID).
		Order("created_at DESC").
		First(&attempt).Error; err != nil {
		return model.QuizAttempt{}, fmt.Errorf("no attempt found: %w", err)
	}
	return attempt, nil
}
