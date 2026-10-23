// Package repository implements the data access layer for the Course Material Service.
// It contains concrete implementations of the repository interfaces defined in the model package.
package repository

import (
	"fmt"
	"focusflow-api/internal/material/model"

	"gorm.io/gorm"
)

// CourseMaterialRepo is the PostgreSQL-backed implementation of model.CourseMaterialRepository.
type CourseMaterialRepo struct {
	db *gorm.DB
}

// NewCourseMaterialRepository creates a new CourseMaterialRepo with the given database connection.
func NewCourseMaterialRepository(db *gorm.DB) *CourseMaterialRepo {
	return &CourseMaterialRepo{db: db}
}

// CreateCourseMaterial inserts a single course material record into the database.
// On success it returns the persisted entity with its auto-generated VideoID populated.
func (r *CourseMaterialRepo) CreateCourseMaterial(cm *model.CourseMaterial) (model.CourseMaterial, error) {
	result := r.db.Create(cm)
	if result.Error != nil {
		return model.CourseMaterial{}, result.Error
	}
	return *cm, nil
}

// GetCourseMaterialByID fetches a single course material by its primary key (VideoID).
// Returns an error if the record is not found.
func (r *CourseMaterialRepo) GetCourseMaterialByID(videoID int) (model.CourseMaterial, error) {
	var cm model.CourseMaterial
	result := r.db.First(&cm, videoID)
	if result.Error != nil {
		return model.CourseMaterial{}, fmt.Errorf("course material with video_id %d not found: %w", videoID, result.Error)
	}
	return cm, nil
}

// GetCourseMaterialsByCourseID retrieves all course materials that belong to a given course.
// Returns an empty slice (not nil) when no records match.
func (r *CourseMaterialRepo) GetCourseMaterialsByCourseID(courseID string) ([]model.CourseMaterial, error) {
	var courseMaterials []model.CourseMaterial
	result := r.db.Where("course_id = ?", courseID).Find(&courseMaterials)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to fetch course materials for course %s: %w", courseID, result.Error)
	}
	return courseMaterials, nil
}

// UpdateCourseMaterial updates an existing course material record in the database.
// It uses Save which updates all fields, ensuring the full entity is persisted.
func (r *CourseMaterialRepo) UpdateCourseMaterial(cm *model.CourseMaterial) (model.CourseMaterial, error) {
	result := r.db.Save(cm)
	if result.Error != nil {
		return model.CourseMaterial{}, fmt.Errorf("failed to update course material with video_id %d: %w", cm.VideoID, result.Error)
	}
	return *cm, nil
}

// DeleteCourseMaterial removes a course material by its primary key (VideoID).
// Returns an error if the record does not exist or the deletion fails.
func (r *CourseMaterialRepo) DeleteCourseMaterial(videoID int) error {
	result := r.db.Delete(&model.CourseMaterial{}, videoID)
	if result.Error != nil {
		return fmt.Errorf("failed to delete course material with video_id %d: %w", videoID, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("course material with video_id %d not found", videoID)
	}
	return nil
}
