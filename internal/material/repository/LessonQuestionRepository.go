// Package repository implements the data access layer.
package repository

import (
	"errors"
	"fmt"
	"focusflow-api/internal/material/model"

	"gorm.io/gorm"
)

// LessonQuestionRepo is the PostgreSQL-backed implementation of
// model.LessonQuestionRepository.
type LessonQuestionRepo struct {
	db *gorm.DB
}

// NewLessonQuestionRepository wires the repo to a DB handle.
func NewLessonQuestionRepository(db *gorm.DB) *LessonQuestionRepo {
	return &LessonQuestionRepo{db: db}
}

// ListByVideo returns the six questions from the LATEST set for a lesson,
// in stepper position order. The "latest set" is the one most recently
// generated — this is the quiz the player's Mark-as-complete will offer.
func (r *LessonQuestionRepo) ListByVideo(videoID int) ([]model.LessonQuestion, error) {
	latest, err := r.LatestSetNumber(videoID)
	if err != nil {
		return nil, err
	}
	if latest == 0 {
		return nil, nil
	}
	return r.ListByVideoSet(videoID, latest)
}

// ListByVideoSet returns questions for a specific (video, set) pair.
func (r *LessonQuestionRepo) ListByVideoSet(videoID, setNumber int) ([]model.LessonQuestion, error) {
	var qs []model.LessonQuestion
	if err := r.db.
		Where("video_id = ? AND set_number = ?", videoID, setNumber).
		Order("position ASC").
		Find(&qs).Error; err != nil {
		return nil, fmt.Errorf("list set %d for video %d: %w", setNumber, videoID, err)
	}
	return qs, nil
}

// LatestSetNumber returns the highest set_number stored for a lesson, or
// 0 when nothing has been generated yet.
func (r *LessonQuestionRepo) LatestSetNumber(videoID int) (int, error) {
	var max struct{ N int }
	if err := r.db.
		Raw("SELECT COALESCE(MAX(set_number), 0) AS n FROM lesson_questions WHERE video_id = ?", videoID).
		Scan(&max).Error; err != nil {
		return 0, fmt.Errorf("max set_number for video %d: %w", videoID, err)
	}
	return max.N, nil
}

// CountByVideo returns total question rows across ALL sets for a lesson.
// Used by Prepare to decide whether the initial set still needs to run.
func (r *LessonQuestionRepo) CountByVideo(videoID int) (int64, error) {
	var count int64
	if err := r.db.Model(&model.LessonQuestion{}).
		Where("video_id = ?", videoID).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count questions failed: %w", err)
	}
	return count, nil
}

// errAlreadyExists guards against duplicate INITIAL generation. Regenerate
// flows bypass this by passing the next set_number explicitly.
var errAlreadyExists = errors.New("questions already exist for this video")

// BulkInsert persists a generated question set atomically. The
// transaction's count-then-insert pattern is the idempotency guard ONLY
// for set 1 — for set > 1 (regenerate) we trust the caller to have picked
// a fresh set_number and skip the guard.
func (r *LessonQuestionRepo) BulkInsert(qs []model.LessonQuestion) ([]model.LessonQuestion, error) {
	if len(qs) == 0 {
		return nil, fmt.Errorf("no questions provided")
	}
	videoID := qs[0].VideoID
	setNumber := qs[0].SetNumber
	if setNumber == 0 {
		setNumber = 1
		for i := range qs {
			qs[i].SetNumber = 1
		}
	}

	var saved []model.LessonQuestion
	txErr := r.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.LessonQuestion{}).
			Where("video_id = ? AND set_number = ?", videoID, setNumber).
			Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errAlreadyExists
		}
		if err := tx.Create(&qs).Error; err != nil {
			return err
		}
		saved = qs
		return nil
	})

	if errors.Is(txErr, errAlreadyExists) {
		// Race lost: another goroutine inserted this set first. Return
		// what's stored.
		return r.ListByVideoSet(videoID, setNumber)
	}
	if txErr != nil {
		return nil, fmt.Errorf("bulk insert failed: %w", txErr)
	}
	return saved, nil
}
