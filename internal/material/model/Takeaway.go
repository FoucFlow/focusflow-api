// Package model defines the domain entities for the FocusFlow Course Material Service.
package model

import "time"

// Takeaway is a learner's short, written summary of a lesson, submitted at the
// moment of marking the lesson complete. It operationalises the active-recall
// gate described in US-001 — the Testing Effect (Roediger & Karpicke, 2006).
//
// One row per (UserID, VideoID). Resubmission updates the body rather than
// creating duplicates.
type Takeaway struct {
	ID        int       `json:"id" gorm:"primaryKey;autoIncrement" example:"1"`
	VideoID   int       `json:"video_id" gorm:"not null;uniqueIndex:idx_user_video" example:"1"`
	UserID    int       `json:"user_id" gorm:"not null;uniqueIndex:idx_user_video" example:"42"`
	Body      string    `json:"body" gorm:"type:text;not null" example:"Goroutines are lightweight threads managed by the Go runtime."`
	WordCount int       `json:"word_count" example:"12"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TakeawayRepository is the persistence contract for Takeaway operations.
type TakeawayRepository interface {
	// Upsert creates a new takeaway or replaces the body of an existing one
	// for the same (UserID, VideoID). Returns the persisted entity.
	Upsert(t *Takeaway) (Takeaway, error)

	// GetByUserAndVideo returns the user's prior takeaway for a lesson, or
	// an error if none exists.
	GetByUserAndVideo(userID, videoID int) (Takeaway, error)

	// GetByUserAndCourse returns every takeaway the user has written for
	// lessons in the given course, ordered by lesson_id then created_at.
	GetByUserAndCourse(userID int, courseID string) ([]Takeaway, error)
}
