// Package learn is the course builder (FEATURES.md step 1): materials on a
// course canvas, AI digests, AI-organised units, publishing, and the learner
// flow where each unit opens after the previous one is finished.
package learn

import "time"

// Material kinds (F1.2)
const (
	KindPDF    = "pdf"
	KindSlides = "slides" // PowerPoint .pptx
	KindVideo  = "video"  // YouTube link
	KindLink   = "link"   // any web page
)

// Processing states for a material digest or a course organise run
const (
	StatusProcessing = "processing"
	StatusReady      = "ready"
	StatusFailed     = "failed"
	StatusIdle       = "idle"
)

// Material is one item on a course canvas.
type Material struct {
	ID         int    `json:"id" gorm:"primaryKey"`
	CourseID   int    `json:"course_id" gorm:"not null;index"`
	OwnerID    int    `json:"owner_id" gorm:"not null;index"`
	Kind       string `json:"kind" gorm:"size:16;not null"`
	Title      string `json:"title" gorm:"size:255;not null"`
	URL        string `json:"url" gorm:"size:2048"` // public file URL or the external link
	StorageKey string `json:"-" gorm:"size:512"`    // object key in the media bucket (files only)
	YouTubeID  string `json:"youtube_id,omitempty" gorm:"size:32"`
	MimeType   string `json:"mime_type,omitempty" gorm:"size:128"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
	Status     string `json:"status" gorm:"size:16;not null;default:processing"`
	Error      string `json:"error,omitempty" gorm:"type:text"`
	Digest     Digest `json:"digest" gorm:"type:jsonb;serializer:json"`
	// Text extracted from slides (sent to the AI instead of the file). Never in API output.
	Text      string    `json:"-" gorm:"type:text"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (Material) TableName() string { return "course_materials_v2" }

// Digest is the AI's reading of a material (F1.3): a summary plus the
// concepts it teaches and where each one appears.
type Digest struct {
	Summary   string          `json:"summary"`
	Topics    []string        `json:"topics"`
	KeyPoints []string        `json:"key_points"`
	Concepts  []SourceConcept `json:"concepts"`
}

// SourceConcept is one concept found in one material, with its location.
type SourceConcept struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Pages   []int  `json:"pages"`  // PDF pages (1-based)
	Slides  []int  `json:"slides"` // slide numbers
}

// Unit is one section of a course (F1.5). Body is Markdown.
type Unit struct {
	ID        int       `json:"id" gorm:"primaryKey"`
	CourseID  int       `json:"course_id" gorm:"not null;index"`
	Position  int       `json:"position" gorm:"not null"`
	Title     string    `json:"title" gorm:"size:255;not null"`
	Body      string    `json:"body" gorm:"type:text"`
	Source    string    `json:"source" gorm:"size:16;not null;default:manual"` // ai | manual
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Concept is one idea taught in a unit, explained from the materials and
// linked to where learners can check it in the sources.
type Concept struct {
	ID          int             `json:"id" gorm:"primaryKey"`
	UnitID      int             `json:"unit_id" gorm:"not null;index"`
	Position    int             `json:"position" gorm:"not null"`
	Title       string          `json:"title" gorm:"size:255;not null"`
	Explanation string          `json:"explanation" gorm:"type:text"` // Markdown
	Sources     []ConceptSource `json:"sources" gorm:"type:jsonb;serializer:json"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

func (Concept) TableName() string { return "unit_concepts" }

// ConceptSource points at the part of a material that covers a concept.
type ConceptSource struct {
	MaterialID   int   `json:"material_id"`
	Pages        []int `json:"pages,omitempty"`
	Slides       []int `json:"slides,omitempty"`
	StartSeconds *int  `json:"start_seconds,omitempty"` // videos (needs transcripts; later step)
}

// UnitMaterial links a material into a unit, in order.
type UnitMaterial struct {
	UnitID     int `gorm:"primaryKey"`
	MaterialID int `gorm:"primaryKey;index"`
	Position   int `gorm:"not null"`
}

// UnitProgress records that a learner finished a unit (F1.7).
type UnitProgress struct {
	UserID      int       `json:"user_id" gorm:"primaryKey"`
	UnitID      int       `json:"unit_id" gorm:"primaryKey"`
	CourseID    int       `json:"course_id" gorm:"not null;index"`
	CompletedAt time.Time `json:"completed_at"`
}

// AIUsage logs every AI call: task, model, tokens, cost, outcome.
// No learner names or content are stored here.
type AIUsage struct {
	ID           int       `json:"id" gorm:"primaryKey"`
	Task         string    `json:"task" gorm:"size:32;not null;index"`
	Provider     string    `json:"provider" gorm:"size:16;not null"`
	Model        string    `json:"model" gorm:"size:64;not null"`
	CourseID     int       `json:"course_id" gorm:"index"`
	UserID       int       `json:"user_id" gorm:"index"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	CostMicroUSD int64     `json:"cost_micro_usd"`
	DurationMS   int64     `json:"duration_ms"`
	OK           bool      `json:"ok"`
	Error        string    `json:"error,omitempty" gorm:"type:text"`
	CreatedAt    time.Time `json:"created_at" gorm:"index"`
}

func (AIUsage) TableName() string { return "ai_usage" }

// Models lists the tables this package owns, for AutoMigrate.
func Models() []any {
	return []any{&Material{}, &Unit{}, &Concept{}, &UnitMaterial{}, &UnitProgress{}, &AIUsage{}}
}
