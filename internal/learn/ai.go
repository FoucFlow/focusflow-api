package learn

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrAIUnavailable means no AI provider is configured (no API key).
var ErrAIUnavailable = errors.New("AI is not configured on this server")

// ErrAICredit means the provider refused the call because the account's
// credit is used up (a billing problem, not something a retry fixes).
var ErrAICredit = errors.New("the AI account's credit is used up")

// fullAI is a provider that does every course-builder task.
type fullAI interface {
	AI
	Enricher
	Examiner
}

// FallbackAI uses Primary, and Backup for any call Primary refuses because
// its credit is used up (e.g. Claude out of credit → Gemini).
type FallbackAI struct {
	Primary, Backup fullAI
}

// SetExamModels applies to the primary (the backup keeps its own defaults).
func (f FallbackAI) SetExamModels(question, mark string) {
	if m, ok := f.Primary.(interface{ SetExamModels(q, m string) }); ok {
		m.SetExamModels(question, mark)
	}
}

// SetRecorder logs the calls of both providers.
func (f FallbackAI) SetRecorder(r CallRecorder) {
	for _, p := range []any{f.Primary, f.Backup} {
		if s, ok := p.(interface{ SetRecorder(CallRecorder) }); ok {
			s.SetRecorder(r)
		}
	}
}

func (f FallbackAI) Digest(ctx context.Context, in DigestInput) (Digest, Usage, error) {
	d, u, err := f.Primary.Digest(ctx, in)
	if errors.Is(err, ErrAICredit) {
		return f.Backup.Digest(ctx, in)
	}
	return d, u, err
}

func (f FallbackAI) Organise(ctx context.Context, in OrganiseInput) (Outline, Usage, error) {
	o, u, err := f.Primary.Organise(ctx, in)
	if errors.Is(err, ErrAICredit) {
		return f.Backup.Organise(ctx, in)
	}
	return o, u, err
}

func (f FallbackAI) FindReferences(ctx context.Context, in EnrichInput) (map[int][]Reference, Usage, error) {
	r, u, err := f.Primary.FindReferences(ctx, in)
	if errors.Is(err, ErrAICredit) {
		return f.Backup.FindReferences(ctx, in)
	}
	return r, u, err
}

func (f FallbackAI) WriteQuestions(ctx context.Context, in QuestionsInput) ([]Question, Usage, error) {
	q, u, err := f.Primary.WriteQuestions(ctx, in)
	if errors.Is(err, ErrAICredit) {
		return f.Backup.WriteQuestions(ctx, in)
	}
	return q, u, err
}

func (f FallbackAI) MarkAnswers(ctx context.Context, in MarkInput) (map[int]AIMark, Usage, error) {
	m, u, err := f.Primary.MarkAnswers(ctx, in)
	if errors.Is(err, ErrAICredit) {
		return f.Backup.MarkAnswers(ctx, in)
	}
	return m, u, err
}

// AI is everything the course builder asks a model to do. Each task can use
// a different provider/model; see FEATURES.md section 8.
type AI interface {
	// Digest reads one material and returns a short digest (F1.3).
	Digest(ctx context.Context, in DigestInput) (Digest, Usage, error)
	// Organise turns ready materials into units (F1.4).
	Organise(ctx context.Context, in OrganiseInput) (Outline, Usage, error)
}

type DigestInput struct {
	Kind  string
	Title string
	PDF   []byte // KindPDF: the file itself (or one part of it)
	// For one part of a long PDF: its pages in the whole document
	FromPage, ToPage, TotalPages int
	Text                         string // KindSlides: extracted slide text; KindVideo/KindLink: title + description
}

type OrganiseInput struct {
	CourseTitle       string
	CourseDescription string
	Language          string
	Materials         []OrganiseMaterial
	// ExistingUnits is set when adding units for newly read parts: the
	// course's current units, which stay as they are.
	ExistingUnits []ExistingUnit
}

type ExistingUnit struct {
	Title    string   `json:"title"`
	Concepts []string `json:"concepts"`
}

// OrganiseMaterial is what the organiser sees of one material: its summary
// and its concepts, each with a reference key like "M12-C3".
type OrganiseMaterial struct {
	Ref      string            `json:"ref"` // "M12"
	Kind     string            `json:"kind"`
	Title    string            `json:"title"`
	Summary  string            `json:"summary"`
	Concepts []OrganiseConcept `json:"concepts,omitempty"`
}

type OrganiseConcept struct {
	Ref     string `json:"ref"` // "M12-C3"
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Details string `json:"details,omitempty"`
}

// Outline is the AI's proposed course: units made of concepts.
type Outline struct {
	Units []OutlineUnit `json:"units"`
}

type OutlineUnit struct {
	Title    string           `json:"title"`
	Intro    string           `json:"intro"`
	Concepts []OutlineConcept `json:"concepts"`
}

type OutlineConcept struct {
	Title       string   `json:"title"`
	Explanation string   `json:"explanation"`
	Sources     []string `json:"sources"` // refs: "M12-C3" (a located concept) or "M14" (a whole material)
}

// Enricher finds outside references for a unit's concepts (web search).
type Enricher interface {
	FindReferences(ctx context.Context, in EnrichInput) (map[int][]Reference, Usage, error)
}

type EnrichInput struct {
	CourseTitle string
	Language    string
	UnitTitle   string
	Concepts    []EnrichConcept
}

type EnrichConcept struct {
	Index   int    `json:"index"` // 1-based, as returned in the result map
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

// Usage is what one AI call cost.
type Usage struct {
	Provider     string
	Model        string
	InputTokens  int64
	OutputTokens int64
	CostMicroUSD int64
}

// disabledAI answers every task with ErrAIUnavailable.
type disabledAI struct{}

func (disabledAI) Digest(context.Context, DigestInput) (Digest, Usage, error) {
	return Digest{}, Usage{}, ErrAIUnavailable
}

func (disabledAI) Organise(context.Context, OrganiseInput) (Outline, Usage, error) {
	return Outline{}, Usage{}, ErrAIUnavailable
}

// FakeAI is a deterministic provider for local development and tests
// (AI_PROVIDER=fake; refused in production). It makes one unit per material.
type FakeAI struct{}

func (FakeAI) Digest(_ context.Context, in DigestInput) (Digest, Usage, error) {
	d := Digest{Summary: "Digest of " + in.Title, Topics: []string{in.Title}, KeyPoints: []string{"Key point of " + in.Title}}
	for i := 1; i <= 2; i++ {
		c := SourceConcept{Name: fmt.Sprintf("%s concept %d", in.Title, i), Summary: "What concept " + fmt.Sprint(i) + " means"}
		if in.Kind == KindSlides {
			c.Slides = []int{i}
		} else {
			c.Pages = []int{i, i + 1}
		}
		d.Concepts = append(d.Concepts, c)
	}
	return d, Usage{Provider: "fake", Model: "fake"}, nil
}

// FindReferences: one reference per concept.
func (FakeAI) FindReferences(_ context.Context, in EnrichInput) (map[int][]Reference, Usage, error) {
	out := map[int][]Reference{}
	for _, c := range in.Concepts {
		out[c.Index] = []Reference{{Title: "About " + c.Title, URL: "https://en.wikipedia.org/wiki/Special:Search?search=" + strings.ReplaceAll(c.Title, " ", "+"),
			Site: "en.wikipedia.org", Why: "A general overview of " + c.Title}}
	}
	return out, Usage{Provider: "fake", Model: "fake"}, nil
}

// DigestVideoPart: two concepts, 30 s and 120 s into the part.
func (FakeAI) DigestVideoPart(_ context.Context, _, title, _ string, p VideoPart) (Digest, Usage, error) {
	a, b := p.From+30, p.From+120
	return Digest{Summary: "Video digest of " + title, Topics: []string{title}, KeyPoints: []string{},
		Concepts: []SourceConcept{{Name: fmt.Sprintf("%s idea at %d", title, a), Summary: "An idea", StartSeconds: &a},
			{Name: fmt.Sprintf("%s idea at %d", title, b), Summary: "Another idea", StartSeconds: &b}}}, Usage{Provider: "fake", Model: "fake"}, nil
}

// DigestVideo: two concepts, at 0:30 and 2:00.
func (FakeAI) DigestVideo(_ context.Context, _, title, _ string) (Digest, Usage, error) {
	a, b := 30, 120
	return Digest{Summary: "Video digest of " + title, Topics: []string{title}, KeyPoints: []string{},
		Concepts: []SourceConcept{{Name: title + " idea 1", Summary: "First idea", StartSeconds: &a},
			{Name: title + " idea 2", Summary: "Second idea", StartSeconds: &b}}}, Usage{Provider: "fake", Model: "fake"}, nil
}

// Organise: one unit per material; its concepts (or the material itself) as sources.
func (FakeAI) Organise(_ context.Context, in OrganiseInput) (Outline, Usage, error) {
	var o Outline
	for _, m := range in.Materials {
		u := OutlineUnit{Title: "Unit: " + m.Title, Intro: "This unit covers " + m.Title + "."}
		for _, c := range m.Concepts {
			u.Concepts = append(u.Concepts, OutlineConcept{Title: c.Name, Explanation: "**" + c.Name + "**: " + c.Summary, Sources: []string{c.Ref}})
		}
		if len(m.Concepts) == 0 {
			u.Concepts = append(u.Concepts, OutlineConcept{Title: m.Title, Explanation: m.Summary, Sources: []string{m.Ref}})
		}
		o.Units = append(o.Units, u)
	}
	return o, Usage{Provider: "fake", Model: "fake"}, nil
}
