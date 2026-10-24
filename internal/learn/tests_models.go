package learn

import "time"

// Unit tests (FEATURES.md step 3: F2.1 to F2.4). Each unit can have a test:
// a pool of questions written by the AI from the unit's lessons and reviewed by
// the creator; every learner gets a random draw with shuffled options. Passing
// finishes the unit and opens the next. With teacher review on, the AI's marks
// are proposals the creator confirms or changes.

// Question kinds (F2.1)
const (
	QuestionMCQ     = "mcq"     // multiple choice, marked in code
	QuestionShort   = "short"   // a sentence or two, marked by the AI
	QuestionExplain = "explain" // a short paragraph, marked by the AI
)

// Attempt states
const (
	AttemptInProgress = "in_progress"
	AttemptMarking    = "marking" // written answers are with the AI
	AttemptReview     = "review"  // waiting for the teacher
	AttemptDone       = "done"
)

// Question is one item of a unit's pool.
type Question struct {
	ID       int      `json:"id" gorm:"primaryKey"`
	UnitID   int      `json:"unit_id" gorm:"not null;index"`
	CourseID int      `json:"course_id" gorm:"not null;index"`
	Kind     string   `json:"kind" gorm:"size:16;not null"`
	Prompt   string   `json:"prompt" gorm:"type:text;not null"`
	Options  []string `json:"options" gorm:"type:jsonb;serializer:json"` // mcq only
	Correct  int      `json:"correct"`                                   // mcq: index into Options
	// AnswerGuide is what a good answer contains (written questions) or why the
	// right option is right (mcq). Used for marking and teacher review.
	AnswerGuide string    `json:"answer_guide" gorm:"type:text"`
	Concept     string    `json:"concept" gorm:"size:255"`                   // the concept it tests
	Source      string    `json:"source" gorm:"size:16;not null;default:ai"` // ai | edited | manual
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (Question) TableName() string { return "unit_questions" }

// TestSettings is a unit's test configuration (F2.3) and its pool status.
type TestSettings struct {
	UnitID           int       `json:"unit_id" gorm:"primaryKey"`
	CourseID         int       `json:"course_id" gorm:"not null;index"`
	Enabled          bool      `json:"enabled"`
	QuestionsPerTest int       `json:"questions_per_test" gorm:"not null;default:8"`
	PassMark         int       `json:"pass_mark" gorm:"not null;default:60"`          // percent
	TimeLimitMinutes int       `json:"time_limit_minutes" gorm:"not null;default:15"` // 0 = no limit
	MaxAttempts      int       `json:"max_attempts" gorm:"not null;default:3"`        // 0 = unlimited
	TeacherReview    bool      `json:"teacher_review"`
	TrackTab         bool      `json:"track_tab"`   // leaving the tab or fullscreen
	TrackPaste       bool      `json:"track_paste"` // copy/paste blocked and logged
	PoolStatus       string    `json:"pool_status" gorm:"size:16;not null;default:idle"`
	PoolError        string    `json:"pool_error,omitempty" gorm:"type:text"`
	PoolRuns         int       `json:"pool_runs" gorm:"not null;default:0"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (TestSettings) TableName() string { return "unit_tests" }

func defaultTestSettings(unitID, courseID int) TestSettings {
	return TestSettings{UnitID: unitID, CourseID: courseID, QuestionsPerTest: 8, PassMark: 60,
		TimeLimitMinutes: 15, MaxAttempts: 3, TrackTab: true, TrackPaste: true, PoolStatus: StatusIdle}
}

// Attempt is one learner's sitting of a unit test. The drawn questions are
// copied in (Items), so later pool edits never change a past attempt.
type Attempt struct {
	ID            int           `json:"id" gorm:"primaryKey"`
	UnitID        int           `json:"unit_id" gorm:"not null;index"`
	CourseID      int           `json:"course_id" gorm:"not null;index"`
	UserID        int           `json:"user_id" gorm:"not null;index"`
	Number        int           `json:"number"` // 1st, 2nd... attempt of this learner
	Status        string        `json:"status" gorm:"size:16;not null;index"`
	Items         []AttemptItem `json:"items" gorm:"type:jsonb;serializer:json"`
	Events        []TestEvent   `json:"events" gorm:"type:jsonb;serializer:json"`   // integrity evidence
	Settings      TestSettings  `json:"settings" gorm:"type:jsonb;serializer:json"` // as shown when it started
	StartedAt     time.Time     `json:"started_at"`
	Deadline      *time.Time    `json:"deadline"`
	SubmittedAt   *time.Time    `json:"submitted_at"`
	AutoSubmitted bool          `json:"auto_submitted"`
	ScorePercent  float64       `json:"score_percent"` // final score (after review when on)
	Passed        bool          `json:"passed"`
	ReviewedBy    *int          `json:"reviewed_by"`
	ReviewedAt    *time.Time    `json:"reviewed_at"`
	MarkError     string        `json:"mark_error,omitempty" gorm:"type:text"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

func (Attempt) TableName() string { return "test_attempts" }

// AttemptItem is one drawn question with the learner's answer and its mark.
type AttemptItem struct {
	QuestionID int      `json:"question_id"`
	Kind       string   `json:"kind"`
	Prompt     string   `json:"prompt"`
	Options    []string `json:"options,omitempty"` // shuffled for this learner
	Correct    int      `json:"correct"`           // index into the shuffled Options
	Guide      string   `json:"answer_guide,omitempty"`
	Concept    string   `json:"concept,omitempty"`
	Choice     *int     `json:"choice"`  // mcq answer
	Answer     string   `json:"answer"`  // written answer
	AIMark     *float64 `json:"ai_mark"` // 0, 0.5 or 1: proposed by code (mcq) or the AI
	AIFeedback string   `json:"ai_feedback,omitempty"`
	Mark       *float64 `json:"mark"` // final mark (= AIMark unless the teacher changed it)
	Note       string   `json:"teacher_note,omitempty"`
}

// TestEvent is a browser signal during a test (layer 3 of the anti-cheating
// plan): it is evidence for the teacher, never a penalty on its own.
type TestEvent struct {
	Kind string    `json:"kind"` // tab_hidden | fullscreen_exit | paste | copy | timeout
	At   time.Time `json:"at"`
}

// MarkChange logs every change a teacher makes to a mark (F2.4).
type MarkChange struct {
	ID         int       `json:"id" gorm:"primaryKey"`
	AttemptID  int       `json:"attempt_id" gorm:"not null;index"`
	ItemIndex  int       `json:"item_index"`
	QuestionID int       `json:"question_id"`
	OldMark    *float64  `json:"old_mark"`
	NewMark    float64   `json:"new_mark"`
	Note       string    `json:"note" gorm:"type:text"`
	ChangedBy  int       `json:"changed_by" gorm:"not null"`
	CreatedAt  time.Time `json:"created_at"`
}

func (MarkChange) TableName() string { return "test_mark_changes" }
