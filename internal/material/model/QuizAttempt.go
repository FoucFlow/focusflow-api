// Package model defines the domain entities for the FocusFlow Course Material Service.
package model

import "time"

// AttemptStatus discriminates between in-flight and completed grading.
type AttemptStatus string

const (
	// AttemptStatusEvaluating means the row was created and the grading
	// goroutine is either pending or running. MCQ scores may be populated
	// already (they're deterministic), but writing scores are not yet final.
	AttemptStatusEvaluating AttemptStatus = "evaluating"

	// AttemptStatusCompleted means grading finished successfully. All
	// answers carry final scores and feedback.
	AttemptStatusCompleted AttemptStatus = "completed"

	// AttemptStatusFailed means grading hit a non-recoverable error
	// (e.g. Gemini quota). MCQ portion of the score is still trustworthy;
	// writing answers are marked with score=0 and the failure reason.
	AttemptStatusFailed AttemptStatus = "failed"
)

// QuizAttempt is one learner's run through the six questions for a lesson.
// The attempt row is created synchronously when the learner submits and
// grading then runs asynchronously in a goroutine; the row's `status`
// transitions from `evaluating` to `completed` (or `failed`) when grading
// finishes. The frontend polls `GET /quiz-attempts/:id` to render the
// results page progressively.
type QuizAttempt struct {
	ID         int  `json:"id" gorm:"primaryKey;autoIncrement"`
	VideoID    int  `json:"video_id" gorm:"not null;index"`
	UserID     int  `json:"user_id" gorm:"not null;index"`
	TakeawayID *int `json:"takeaway_id,omitempty"` // FK to takeaways.id; reuses the US-001 row
	// SetNumber records which lesson_questions set this attempt was on —
	// learners can have multiple attempts per lesson if they retake, and
	// multiple sets per lesson if they hit "Generate another quiz".
	SetNumber    int           `json:"set_number" gorm:"not null;default:1"`
	Status       AttemptStatus `json:"status" gorm:"type:varchar(16);not null;default:'evaluating'"`
	ErrorMessage string        `json:"error_message,omitempty" gorm:"type:text"` // populated when status='failed'
	TotalScore   float64       `json:"total_score"`                              // 0..100, weighted avg across 6 answers
	MCQScore     float64       `json:"mcq_score"`                                // 0..100, MCQ subset only
	WritingScore float64       `json:"writing_score"`                            // 0..100, writing subset only
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`

	// Eager-loaded line items for the results screen.
	Answers []QuizAnswer `json:"answers,omitempty" gorm:"foreignKey:AttemptID;references:ID"`

	// Read-only enrichment populated by handlers for the results-page
	// response. Not a column. Stored here so the JSON shape is one
	// object rather than a parallel array the frontend has to join.
	LessonTitle string `json:"lesson_title,omitempty" gorm:"-"`
	CourseID    string `json:"course_id,omitempty" gorm:"-"`
}

// QuizAnswer is the per-question line item of a QuizAttempt. One row per
// question (so six rows per attempt for a complete quiz).
//
// The fields prefixed with `Question*` are NOT columns — they are
// read-only enrichment that the QuizService populates from a JOIN with
// `lesson_questions` before returning the row to the frontend. This way
// the results page can show the question prompt and (for MCQ) the
// correct option without making a second round-trip.
type QuizAnswer struct {
	ID         int     `json:"id" gorm:"primaryKey;autoIncrement"`
	AttemptID  int     `json:"attempt_id" gorm:"not null;index"`
	QuestionID int     `json:"question_id" gorm:"not null;index"`
	Answer     string  `json:"answer" gorm:"type:text"`             // MCQ: the chosen option label; writing: free text
	IsCorrect  *bool   `json:"is_correct,omitempty"`                // MCQ only; NULL for writing
	Score      float64 `json:"score"`                               // 0..1
	Feedback   string  `json:"feedback,omitempty" gorm:"type:text"` // Gemini rationale for writing answers

	// Enriched fields (not persisted):
	QuestionPrompt  string   `json:"question_prompt,omitempty" gorm:"-"`
	QuestionType    string   `json:"question_type,omitempty" gorm:"-"`
	QuestionOptions []string `json:"question_options,omitempty" gorm:"-"`
	// CorrectOption is the text of the correct answer for MCQ items, empty
	// for writing items. Only safe to return AFTER the attempt has been
	// submitted (which is the only path that reads it).
	CorrectOption string `json:"correct_option,omitempty" gorm:"-"`
}

// QuizRepository is the persistence contract for attempts + answers.
type QuizRepository interface {
	// CreateAttempt persists the parent attempt and all its line-item
	// answers in a single transaction.
	CreateAttempt(attempt *QuizAttempt) (QuizAttempt, error)

	// UpdateAttempt overwrites the parent row's status/scores/error_message
	// and replaces the answer line items. Used by the async grading
	// goroutine when it transitions an attempt from 'evaluating' to
	// 'completed' or 'failed'.
	UpdateAttempt(attempt *QuizAttempt) (QuizAttempt, error)

	// GetAttemptByID returns one attempt with answers eager-loaded. Used
	// by the results page poll.
	GetAttemptByID(attemptID int) (QuizAttempt, error)

	// LatestByUserVideo returns the most recent attempt for a learner on a
	// lesson, with answers eager-loaded. Used by the lesson sidebar / dash.
	LatestByUserVideo(userID, videoID int) (QuizAttempt, error)

	// ListByUser returns the learner's attempts, newest first, paginated.
	// Used by the /quizzes history page. Answers are NOT eager-loaded —
	// the list view shows scores only; details fetch follows on click.
	ListByUser(userID, limit, offset int) ([]QuizAttempt, error)
}
