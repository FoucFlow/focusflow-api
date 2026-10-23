// Package model defines the domain entities for the FocusFlow Course Material Service.
package model

import (
	"time"

	"gorm.io/datatypes"
)

// QuestionType discriminates between multiple-choice and short-answer
// writing questions. Persisted as a small enum string.
type QuestionType string

const (
	QuestionTypeMCQ     QuestionType = "mcq"
	QuestionTypeWriting QuestionType = "writing"
)

// LessonQuestion is a single quiz question attached to a lesson. Each set
// of 6 rows (3 MCQ + 3 writing) corresponds to one "quiz" — a learner can
// have multiple sets per lesson when they hit "Generate another quiz",
// each covering a different ~25-minute window of the video.
//
// MCQ rows populate Options and CorrectIndex; Rubric is empty.
// Writing rows populate Rubric; Options and CorrectIndex are zero/nil.
type LessonQuestion struct {
	ID      int `json:"id" gorm:"primaryKey;autoIncrement"`
	VideoID int `json:"video_id" gorm:"not null;index"`
	// SetNumber discriminates between regenerated quiz sets for the same
	// lesson. Starts at 1; each call to "Generate another quiz" produces
	// the next integer.
	SetNumber int `json:"set_number" gorm:"not null;default:1;index"`
	// VideoStartSeconds is the offset into the source video where this
	// quiz set's content begins. Combined with VideoEndSeconds it answers
	// "which part of the lesson did these questions cover?".
	VideoStartSeconds int            `json:"video_start_seconds" gorm:"not null;default:0"`
	VideoEndSeconds   int            `json:"video_end_seconds" gorm:"not null;default:1500"` // 25min default
	Position          int            `json:"position"`                                       // 1..6 — order in the stepper
	QuestionType      QuestionType   `json:"question_type" gorm:"type:varchar(16);not null"`
	Prompt            string         `json:"prompt" gorm:"type:text;not null"`
	Options           datatypes.JSON `json:"options,omitempty" gorm:"type:jsonb"` // [{label,text}] for mcq, NULL for writing
	CorrectIndex      *int           `json:"correct_index,omitempty"`             // 0..3 for mcq, NULL for writing
	Rubric            string         `json:"rubric,omitempty" gorm:"type:text"`   // NULL for mcq, set for writing
	CreatedAt         time.Time      `json:"created_at"`
}

// LessonQuestionRepository is the persistence contract for question rows.
type LessonQuestionRepository interface {
	// ListByVideo returns questions for a lesson's LATEST set, in position
	// order. The "latest" set is the one the active quiz dialog draws from.
	ListByVideo(videoID int) ([]LessonQuestion, error)

	// ListByVideoSet returns questions for a specific (video, set) pair
	// — used by the retake-from-history flow when the learner re-attempts
	// an older set rather than the most recent one.
	ListByVideoSet(videoID, setNumber int) ([]LessonQuestion, error)

	// LatestSetNumber returns the highest set_number stored for a lesson,
	// or 0 when no questions exist yet.
	LatestSetNumber(videoID int) (int, error)

	// BulkInsert persists a freshly-generated question set atomically.
	// Used by both /quiz/generate and /quiz/regenerate.
	BulkInsert(qs []LessonQuestion) ([]LessonQuestion, error)

	// CountByVideo returns how many questions exist for a lesson across
	// all sets — used to decide whether INITIAL generation needs to run.
	CountByVideo(videoID int) (int64, error)
}
