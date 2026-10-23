// Package repository implements the data access layer.
package repository

import (
	"fmt"
	"focusflow-api/internal/material/model"

	"gorm.io/gorm"
)

// ReflectionRepo is the PostgreSQL-backed implementation of model.ReflectionRepository.
type ReflectionRepo struct {
	db *gorm.DB
}

// NewReflectionRepository creates a new ReflectionRepo bound to the given DB.
func NewReflectionRepository(db *gorm.DB) *ReflectionRepo {
	return &ReflectionRepo{db: db}
}

// Create inserts a new reflection row. Each call always produces a new row —
// reflections are append-only so the learner retains a full improvement history.
func (r *ReflectionRepo) Create(ref *model.Reflection) (model.Reflection, error) {
	if err := r.db.Create(ref).Error; err != nil {
		return model.Reflection{}, fmt.Errorf("create reflection failed: %w", err)
	}
	return *ref, nil
}

// GetByID fetches a single reflection row by primary key.
func (r *ReflectionRepo) GetByID(id int) (model.Reflection, error) {
	var ref model.Reflection
	if err := r.db.First(&ref, id).Error; err != nil {
		return model.Reflection{}, fmt.Errorf("reflection not found: %w", err)
	}
	return ref, nil
}

// Update persists changes to an existing reflection. GORM's Save uses the
// primary key to issue an UPDATE; if the row doesn't exist it would insert,
// but Update is only ever called by the background grader on a row Create
// returned moments earlier, so that case doesn't arise in practice.
func (r *ReflectionRepo) Update(ref *model.Reflection) (model.Reflection, error) {
	if err := r.db.Save(ref).Error; err != nil {
		return model.Reflection{}, fmt.Errorf("update reflection failed: %w", err)
	}
	return *ref, nil
}

// GetByUserAndVideo returns every reflection the user has submitted for the
// given lesson, sorted oldest-first so the frontend can display a chronological
// improvement timeline.
func (r *ReflectionRepo) GetByUserAndVideo(userID, videoID int) ([]model.Reflection, error) {
	var reflections []model.Reflection
	err := r.db.
		Where("user_id = ? AND video_id = ?", userID, videoID).
		Order("created_at ASC").
		Find(&reflections).Error
	if err != nil {
		return nil, fmt.Errorf("fetch reflections failed: %w", err)
	}
	return reflections, nil
}

// GetLatestByUserAndVideo returns the single most-recent reflection for the
// (user, lesson) pair. The caller is expected to check for gorm.ErrRecordNotFound
// (wrapped inside the error) to distinguish "none yet" from a real DB error.
func (r *ReflectionRepo) GetLatestByUserAndVideo(userID, videoID int) (model.Reflection, error) {
	var ref model.Reflection
	err := r.db.
		Where("user_id = ? AND video_id = ?", userID, videoID).
		Order("created_at DESC").
		First(&ref).Error
	if err != nil {
		return model.Reflection{}, fmt.Errorf("latest reflection not found: %w", err)
	}
	return ref, nil
}
