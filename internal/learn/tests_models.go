package learn

import "time"

// Unit tests (FEATURES.md step 3: F2.1 to F2.4). Each unit can have a test:
// a pool of questions written by the AI from the unit's lessons and reviewed by
// the creator; every learner gets a random draw with shuffled options. Passing
// finishes the unit and opens the next. With teacher review on, the AI's marks
// are proposals the creator confirms or changes.

// Question kinds (F2.1)
const (
	QuestionMCQ       = "mcq"       // multiple choice, one answer, marked in code
	QuestionMulti     = "multi"     // multiple select, marked in code (F2.8)
	QuestionTrueFalse = "truefalse" // options True, False; marked in code
	QuestionShort     = "short"     // a sentence or two, marked by the AI
	QuestionExplain   = "explain"   // a short paragraph, marked by the AI
)

// choiceKind: answered by picking options, marked in code.
func choiceKind(k string) bool {
	return k == QuestionMCQ || k == QuestionMulti || k == QuestionTrueFalse
}

// Attempt states
const (
	AttemptInProgress = "in_progress"
	AttemptMarking    = "marking" // written answers are with the AI
	AttemptExplain    = "explain" // the learner answers follow-up questions (F2.5)
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
	Options  []string `json:"options" gorm:"type:jsonb;serializer:json"`            // mcq only
	Correct  int      `json:"correct"`                                              // mcq, truefalse: index into Options
	Corrects []int    `json:"corrects,omitempty" gorm:"type:jsonb;serializer:json"` // multi: indexes into Options
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
// Numbers have no column default on purpose: 0 means "none" (no time limit,
// unlimited attempts) and GORM would replace a zero with the default.
type TestSettings struct {
	UnitID           int    `json:"unit_id" gorm:"primaryKey"`
	CourseID         int    `json:"course_id" gorm:"not null;index"`
	Enabled          bool   `json:"enabled"`
	QuestionsPerTest int    `json:"questions_per_test" gorm:"not null"`
	PassMark         int    `json:"pass_mark" gorm:"not null"`          // percent
	TimeLimitMinutes int    `json:"time_limit_minutes" gorm:"not null"` // 0 = no limit
	MaxAttempts      int    `json:"max_attempts" gorm:"not null"`       // 0 = unlimited
	TeacherReview    bool   `json:"teacher_review"`
	TrackTab         bool   `json:"track_tab"`   // leaving the tab or fullscreen
	TrackPaste       bool   `json:"track_paste"` // copy/paste blocked and logged
	PoolStatus       string `json:"pool_status" gorm:"size:16;not null;default:idle"`
	PoolError        string `json:"pool_error,omitempty" gorm:"type:text"`
	PoolRuns         int    `json:"pool_runs" gorm:"not null;default:0"`
	// Restriction level (F2.12): practice | standard | strict | supervised | custom
	Level          string     `json:"level" gorm:"size:16;not null;default:standard"`
	Practice       bool       `json:"practice"`                                                   // does not block the next unit
	ShowAnswers    string     `json:"show_answers" gorm:"size:16;not null;default:when_finished"` // after_question | when_finished | after_submit | after_close | never
	AllOnOnePage   bool       `json:"all_on_one_page"`                                            // else one question per screen
	NoBack         bool       `json:"no_back"`                                                    // cannot go back to earlier questions
	OpensAt        *time.Time `json:"opens_at"`
	ClosesAt       *time.Time `json:"closes_at"`
	AccessCode     string     `json:"access_code,omitempty" gorm:"size:32"` // supervised: given out in the room
	ExplainAnswers bool       `json:"explain_answers"`                      // F2.5 follow-up questions
	Camera         bool       `json:"camera"`                               // F2.7 periodic snapshots
	UpdatedAt      time.Time  `json:"updated_at"`
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
	// Explain your answer (F2.5): follow-up questions on the learner's own
	// answers and how consistent the replies were. Evidence, never a penalty.
	Followups       []Followup `json:"followups,omitempty" gorm:"type:jsonb;serializer:json"`
	Consistency     string     `json:"consistency,omitempty" gorm:"size:16"` // consistent | partly | inconsistent
	ConsistencyNote string     `json:"consistency_note,omitempty" gorm:"type:text"`
	Snapshots       []Snapshot `json:"-" gorm:"type:jsonb;serializer:json"` // camera (F2.7)
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (Attempt) TableName() string { return "test_attempts" }

// AttemptItem is one drawn question with the learner's answer and its mark.
type AttemptItem struct {
	QuestionID int      `json:"question_id"`
	Kind       string   `json:"kind"`
	Prompt     string   `json:"prompt"`
	Options    []string `json:"options,omitempty"`  // shuffled for this learner
	Correct    int      `json:"correct"`            // index into the shuffled Options
	Corrects   []int    `json:"corrects,omitempty"` // multi: indexes into the shuffled Options
	Choices    []int    `json:"choices,omitempty"`  // multi answer
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

// Followup is one "explain your answer" question and the learner's reply.
type Followup struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// Snapshot is one camera picture taken during a test (F2.7). Key is a random
// object key, served only through the API to the course creator.
type Snapshot struct {
	Key string    `json:"key"`
	At  time.Time `json:"at"`
}
