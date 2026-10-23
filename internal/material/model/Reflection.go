// Package model defines the domain entities for the FocusFlow Course Material Service.
package model

import (
	"time"

	"gorm.io/datatypes"
)

// ReflectionStatus is the grading state of a Reflection row. The row is
// inserted with status="evaluating" the moment the learner submits, and a
// background goroutine flips it to "completed" (or "failed") once Gemini
// returns. Frontend uses this to render a placeholder card and to know
// when to stop polling.
type ReflectionStatus string

const (
	ReflectionStatusEvaluating ReflectionStatus = "evaluating"
	ReflectionStatusCompleted  ReflectionStatus = "completed"
	ReflectionStatusFailed     ReflectionStatus = "failed"
)

// Reflection records a single Feynman-technique attempt: the learner writes a
// plain-text explanation of a lesson ("explain it like I'm 5") and the system
// evaluates it against the lesson video via Gemini.
//
// Multiple rows per (UserID, VideoID) are intentional — every submission is a
// new row so the learner can see their improvement over time. There is no unique
// constraint on (user, video).
//
// Status flow:
//
//	evaluating  ←  inserted at Submit() time
//	         ↓     background goroutine runs Gemini
//	completed   ←  analysis succeeded; score/strengths/gaps/etc. populated
//	   OR
//	failed      ←  Gemini returned an error; error_message populated
type Reflection struct {
	ID                 int              `json:"id"                   gorm:"primaryKey;autoIncrement"          example:"1"`
	VideoID            int              `json:"video_id"             gorm:"not null;index"                   example:"1"`
	UserID             int              `json:"user_id"              gorm:"not null;index"                   example:"42"`
	Body               string           `json:"body"                 gorm:"type:text;not null"               example:"A goroutine is a lightweight thread managed by the Go runtime…"`
	Status             ReflectionStatus `json:"status"               gorm:"type:varchar(16);not null;default:'evaluating';index" example:"evaluating"`
	Score              int              `json:"score"                                                        example:"72"`
	Strengths          datatypes.JSON   `json:"strengths"            gorm:"type:jsonb"                       swaggertype:"array,string"`
	Gaps               datatypes.JSON   `json:"gaps"                 gorm:"type:jsonb"                       swaggertype:"array,string"`
	SuggestedNextFocus string           `json:"suggested_next_focus" gorm:"type:text"                        example:"Re-watch the section on channel direction — you described channels but skipped the directional send/receive constraint."`
	Mastered           bool             `json:"mastered"                                                     example:"false"`
	WordCount          int              `json:"word_count"                                                   example:"84"`
	ErrorMessage       string           `json:"error_message,omitempty" gorm:"type:text"`
	CreatedAt          time.Time        `json:"created_at"`
	EvaluatedAt        *time.Time       `json:"evaluated_at,omitempty"`
}

// ReflectionRepository is the persistence contract for Reflection operations.
type ReflectionRepository interface {
	// Create inserts a new reflection row and returns the persisted entity.
	Create(r *Reflection) (Reflection, error)

	// GetByID fetches a single reflection by primary key. Used by the
	// background grading goroutine and by polling frontends.
	GetByID(id int) (Reflection, error)

	// Update persists changes to an existing reflection row. The background
	// grading goroutine calls this when Gemini returns to flip status from
	// "evaluating" → "completed" (or "failed") and write the analysis result.
	Update(r *Reflection) (Reflection, error)

	// GetByUserAndVideo returns every reflection the given user has submitted
	// for the given lesson, ordered by created_at ASC (oldest first), so the
	// frontend can render a chronological improvement history.
	GetByUserAndVideo(userID, videoID int) ([]Reflection, error)

	// GetLatestByUserAndVideo returns the most-recent reflection the user
	// submitted for the lesson. Returns an error (wrapping gorm.ErrRecordNotFound)
	// when no reflection exists yet; the service/handler treat that as a
	// normal "not submitted yet" case rather than an application error.
	GetLatestByUserAndVideo(userID, videoID int) (Reflection, error)
}
