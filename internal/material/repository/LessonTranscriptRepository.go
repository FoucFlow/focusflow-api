// Package repository implements the data access layer.
package repository

import (
	"errors"
	"fmt"
	"focusflow-api/internal/material/model"

	"gorm.io/gorm"
)

// LessonTranscriptRepo is the PostgreSQL-backed implementation of
// model.LessonTranscriptRepository.
type LessonTranscriptRepo struct {
	db *gorm.DB
}

// NewLessonTranscriptRepository creates a new LessonTranscriptRepo bound to
// the given DB connection.
func NewLessonTranscriptRepository(db *gorm.DB) *LessonTranscriptRepo {
	return &LessonTranscriptRepo{db: db}
}

// Get returns the cached transcript for a lesson. Returns a wrapped
// gorm.ErrRecordNotFound error when no row exists — callers use errors.Is to
// distinguish a cache-miss from a real DB failure.
func (r *LessonTranscriptRepo) Get(videoID int) (model.LessonTranscript, error) {
	var t model.LessonTranscript
	err := r.db.Where("video_id = ?", videoID).First(&t).Error
	if err != nil {
		return model.LessonTranscript{}, fmt.Errorf("transcript lookup failed: %w", err)
	}
	return t, nil
}

// Upsert inserts a new transcript row or updates the transcript + word_count of
// an existing row for the same VideoID. The VideoID primary key guarantees at
// most one row per lesson.
func (r *LessonTranscriptRepo) Upsert(t *model.LessonTranscript) (model.LessonTranscript, error) {
	var existing model.LessonTranscript
	err := r.db.Where("video_id = ?", t.VideoID).First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.LessonTranscript{}, fmt.Errorf("lookup failed: %w", err)
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := r.db.Create(t).Error; err != nil {
			return model.LessonTranscript{}, fmt.Errorf("create failed: %w", err)
		}
		return *t, nil
	}

	existing.Transcript = t.Transcript
	existing.WordCount = t.WordCount
	existing.Source = t.Source
	if err := r.db.Save(&existing).Error; err != nil {
		return model.LessonTranscript{}, fmt.Errorf("update failed: %w", err)
	}
	return existing, nil
}
