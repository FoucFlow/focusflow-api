package learn

import (
	"context"
	"errors"
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

type OrganiseMaterial struct {
	ID     int    `json:"id"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Digest Digest `json:"digest"`
}

// Outline is the AI's proposed unit structure.
type Outline struct {
	Units []OutlineUnit `json:"units"`
}

type OutlineUnit struct {
	Title       string `json:"title"`
	Body        string `json:"body"`
	MaterialIDs []int  `json:"material_ids"`
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
	return Digest{Summary: "Digest of " + in.Title, Topics: []string{in.Title}, KeyPoints: []string{"Key point of " + in.Title}},
		Usage{Provider: "fake", Model: "fake"}, nil
}

func (FakeAI) Organise(_ context.Context, in OrganiseInput) (Outline, Usage, error) {
	var o Outline
	for _, m := range in.Materials {
		o.Units = append(o.Units, OutlineUnit{Title: "Unit: " + m.Title, Body: "## " + m.Title + "\n\n" + m.Digest.Summary, MaterialIDs: []int{m.ID}})
	}
	return o, Usage{Provider: "fake", Model: "fake"}, nil
}
