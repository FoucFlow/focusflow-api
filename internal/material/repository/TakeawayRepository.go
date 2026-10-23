// Package repository implements the data access layer.
package repository

import (
	"errors"
	"fmt"
	"focusflow-api/internal/material/model"

	"gorm.io/gorm"
)

// TakeawayRepo is the PostgreSQL-backed implementation of model.TakeawayRepository.
type TakeawayRepo struct {
	db *gorm.DB
}

// NewTakeawayRepository creates a new TakeawayRepo bound to the given DB.
func NewTakeawayRepository(db *gorm.DB) *TakeawayRepo {
	return &TakeawayRepo{db: db}
}

// Upsert inserts a new takeaway or updates the body+word_count of an existing
// row matching (UserID, VideoID). The unique index on the table guarantees
// at most one row per (user, lesson).
func (r *TakeawayRepo) Upsert(t *model.Takeaway) (model.Takeaway, error) {
	var existing model.Takeaway
	err := r.db.Where("user_id = ? AND video_id = ?", t.UserID, t.VideoID).First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Takeaway{}, fmt.Errorf("lookup failed: %w", err)
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := r.db.Create(t).Error; err != nil {
			return model.Takeaway{}, fmt.Errorf("create failed: %w", err)
		}
		return *t, nil
	}

	existing.Body = t.Body
	existing.WordCount = t.WordCount
	if err := r.db.Save(&existing).Error; err != nil {
		return model.Takeaway{}, fmt.Errorf("update failed: %w", err)
	}
	return existing, nil
}

// GetByUserAndVideo returns the takeaway for a (user, lesson) pair.
func (r *TakeawayRepo) GetByUserAndVideo(userID, videoID int) (model.Takeaway, error) {
	var t model.Takeaway
	err := r.db.Where("user_id = ? AND video_id = ?", userID, videoID).First(&t).Error
	if err != nil {
		return model.Takeaway{}, fmt.Errorf("takeaway not found: %w", err)
	}
	return t, nil
}

// GetByUserAndCourse returns every takeaway the user has written for lessons
// in the given course. The join is application-level because CourseMaterial
// and Takeaway live in the same database (same service), but the relation is
// expressed via a Where + IN sub-query against course_materials.video_id.
func (r *TakeawayRepo) GetByUserAndCourse(userID int, courseID string) ([]model.Takeaway, error) {
	var takeaways []model.Takeaway
	err := r.db.
		Joins("JOIN course_materials cm ON cm.video_id = takeaways.video_id").
		Where("takeaways.user_id = ? AND cm.course_id = ?", userID, courseID).
		Order("cm.lesson_id ASC, takeaways.created_at ASC").
		Find(&takeaways).Error
	if err != nil {
		return nil, fmt.Errorf("fetch by course failed: %w", err)
	}
	return takeaways, nil
}
