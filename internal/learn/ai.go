package learn

import (
	"context"
	"errors"
	"fmt"
)

// ErrAIUnavailable means no AI provider is configured (no API key).
var ErrAIUnavailable = errors.New("AI is not configured on this server")

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
	PDF   []byte // KindPDF: the file itself
	Text  string // KindSlides: extracted slide text; KindVideo/KindLink: title + description
}

type OrganiseInput struct {
	CourseTitle       string
	CourseDescription string
	Language          string
	Materials         []OrganiseMaterial
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
