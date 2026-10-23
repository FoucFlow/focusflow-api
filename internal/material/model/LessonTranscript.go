// Package model defines the domain entities for the FocusFlow Course Material Service.
package model

import "time"

// LessonTranscript caches the plaintext transcript for a lesson video.
// One row per lesson (VideoID is the primary key). The cache is populated by
// TranscriptService on first demand and then reused for every subsequent
// Feynman reflection submission, eliminating the per-reflection video-ingestion
// cost.
//
// Source records which extraction method produced the row; "gemini_extract" is
// the only value in v1 and denotes a native Gemini video-understanding pass.
type LessonTranscript struct {
	VideoID    int    `gorm:"primaryKey"` // one row per lesson
	Transcript string `gorm:"type:text;not null"`
	Source     string `gorm:"type:varchar(32);not null"` // "gemini_extract" for v1
	WordCount  int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// LessonTranscriptRepository is the persistence contract for LessonTranscript
// operations.
type LessonTranscriptRepository interface {
	// Get returns the cached transcript for a lesson. A gorm.ErrRecordNotFound-
	// wrapping error is returned when no row exists yet — this is the expected
	// initial state for every lesson and is not treated as an application error.
	Get(videoID int) (LessonTranscript, error)

	// Upsert creates a new transcript row or updates an existing one for the
	// same VideoID. Returns the persisted entity.
	Upsert(t *LessonTranscript) (LessonTranscript, error)
}
