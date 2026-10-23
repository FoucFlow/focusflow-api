// Package repository — Gemini integration via the official Google Gen AI Go SDK.
//
// This file is the only outbound surface to Gemini in the entire service.
// Everything above it (services, handlers) depends on the GeminiRepository
// interface, so tests can swap in a fake without touching the network.
package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/genai"
)

// ---------------------------------------------------------------------------
// Public contract
// ---------------------------------------------------------------------------

// GeneratedQuestionSet is the structured result of one question-generation
// call. The Service layer maps these into model.LessonQuestion rows.
type GeneratedQuestionSet struct {
	MCQs           []GeneratedMCQ     `json:"mcqs"`
	WritingPrompts []GeneratedWriting `json:"writing_prompts"`
}

type GeneratedMCQ struct {
	Prompt       string   `json:"prompt"`
	Options      []string `json:"options"`       // exactly 4
	CorrectIndex int      `json:"correct_index"` // 0..3
}

type GeneratedWriting struct {
	Prompt string `json:"prompt"`
	Rubric string `json:"rubric"` // 30..80 words; 2-3 specific points
}

// Grade is the result of grading one writing answer.
type Grade struct {
	Score    float64 `json:"score"`    // 0..1
	Feedback string  `json:"feedback"` // <= 280 chars
}

// WritingGradeItem is one writing-answer triple to grade in a batch call.
// The Index field is for the caller's bookkeeping — the repository
// preserves order, so input slot N maps to output slot N.
type WritingGradeItem struct {
	Index  int    // caller-supplied identifier, echoed by the model
	Prompt string // the question
	Rubric string // required points an acceptable answer must cover
	Answer string // the learner's response
}

// ReflectionAnalysis is the structured result of one AnalyzeReflection call.
// The Service layer maps these fields into a model.Reflection row.
type ReflectionAnalysis struct {
	Score              int      `json:"score"`                // 0..100
	Strengths          []string `json:"strengths"`            // 1..5 items
	Gaps               []string `json:"gaps"`                 // 0..5 items
	SuggestedNextFocus string   `json:"suggested_next_focus"` // <= 280 chars
	Mastered           bool     `json:"mastered"`             // true iff score >= 85
}

// GeminiRepository is the contract callers depend on.
type GeminiRepository interface {
	// GenerateQuestions runs the default 0..25min clip.
	GenerateQuestions(ctx context.Context, videoURL, lessonTitle string) (GeneratedQuestionSet, error)
	// GenerateQuestionsClipped allows callers (e.g. Regenerate) to pick
	// an arbitrary [start, end] window into the video. Used to produce
	// "set 2" questions covering minutes 25-50, "set 3" covering 50-75, etc.
	GenerateQuestionsClipped(ctx context.Context, videoURL, lessonTitle string, start, end time.Duration) (GeneratedQuestionSet, error)
	// GradeWriting grades a single answer. Kept for tests and back-compat;
	// production code paths use GradeWritingBatch.
	GradeWriting(ctx context.Context, prompt, rubric, answer string) (Grade, error)
	// GradeWritingBatch grades N (1..6) answers in a single Gemini call.
	// Cuts request count proportionally — at N=3 this is exactly 3× cheaper
	// in both dollars and free-tier quota usage.
	GradeWritingBatch(ctx context.Context, items []WritingGradeItem) ([]Grade, error)
	// AnalyzeReflection evaluates a Feynman-technique explanation written by
	// the learner against the lesson video and returns a structured assessment.
	// videoURL must be a full YouTube URL; Gemini ingests it natively via
	// FileData (same mechanism as question generation).
	// This is the video-grounded (expensive) path — kept as a last-resort
	// fallback for when transcript extraction itself fails.
	AnalyzeReflection(ctx context.Context, lessonTitle, lessonDescription, videoURL, userExplanation string) (ReflectionAnalysis, error)
	// ExtractTranscript ingests the video natively and returns a clean
	// plaintext transcript (no timestamps, no speaker labels — just the spoken
	// content in paragraphs). Uses the generationModel because video
	// understanding is required. Called at most once per lesson (result is
	// cached by TranscriptService).
	ExtractTranscript(ctx context.Context, videoURL string) (string, error)
	// AnalyzeReflectionFromTranscript scores a learner's Feynman explanation
	// against an already-extracted transcript. Text-only — uses the cheap
	// gradeModel tier. Approximately 10× faster and 5× cheaper than
	// AnalyzeReflection. Same response schema and scoring rubric.
	AnalyzeReflectionFromTranscript(
		ctx context.Context,
		lessonTitle, lessonDescription, transcript, userExplanation string,
	) (ReflectionAnalysis, error)
	Close() error
}

// ---------------------------------------------------------------------------
// Concrete implementation (Google Gen AI SDK)
// ---------------------------------------------------------------------------

// geminiRepo wires together the SDK client and TWO model ids:
//
//   - generationModel — used for question generation. This call needs
//     video understanding, which today only the gemini-2.5-flash family
//     does well. Quota is small but the call only runs once per lesson
//     (results are cached forever in lesson_questions).
//
//   - gradeModel — used for short-answer writing grading. Text-only,
//     short input + short output. Cheaper / higher-quota Lite or 2.0
//     Flash models handle this fine and give us much more daily headroom
//     since grading runs on every quiz attempt.
//
// Splitting the two calls across separate models effectively multiplies
// our free-tier daily quota: each model has independent limits.
type geminiRepo struct {
	client          *genai.Client
	generationModel string
	gradeModel      string
}

// NewGeminiRepository creates a repo bound to the given API key.
//
//   - generationModel defaults to "gemini-2.5-flash" (only model with
//     reliable video understanding on this SDK as of writing).
//   - gradeModel defaults to "gemini-2.5-flash-lite" — the cheapest text
//     tier, ample for rubric-based short-answer scoring.
//
// Either model id can be overridden via GEMINI_MODEL and
// GEMINI_GRADE_MODEL env vars (read at main.go).
func NewGeminiRepository(ctx context.Context, apiKey, generationModel, gradeModel string) (GeminiRepository, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY is required")
	}
	if generationModel == "" {
		generationModel = "gemini-2.5-flash"
	}
	if gradeModel == "" {
		gradeModel = "gemini-2.5-flash-lite"
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("gemini client init: %w", err)
	}

	return &geminiRepo{
		client:          client,
		generationModel: generationModel,
		gradeModel:      gradeModel,
	}, nil
}

func (r *geminiRepo) Close() error {
	// genai.Client doesn't expose an explicit Close; left for parity with the
	// interface so callers can `defer repo.Close()` safely.
	return nil
}

// ---------------------------------------------------------------------------
// GenerateQuestions
// ---------------------------------------------------------------------------

// questionGenSchema is the response schema we hand to Gemini. With
// responseMimeType=application/json + responseSchema set, the SDK guarantees
// the response will conform — we don't need defensive JSON-cleaning code.
func questionGenSchema() *genai.Schema {
	mcqItem := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"prompt":        {Type: genai.TypeString},
			"options":       {Type: genai.TypeArray, Items: &genai.Schema{Type: genai.TypeString}},
			"correct_index": {Type: genai.TypeInteger},
		},
		Required: []string{"prompt", "options", "correct_index"},
	}
	writingItem := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"prompt": {Type: genai.TypeString},
			"rubric": {Type: genai.TypeString},
		},
		Required: []string{"prompt", "rubric"},
	}
	return &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"mcqs":            {Type: genai.TypeArray, Items: mcqItem},
			"writing_prompts": {Type: genai.TypeArray, Items: writingItem},
		},
		Required: []string{"mcqs", "writing_prompts"},
	}
}

const questionGenSystemPrompt = `You are an expert instructional designer.
Generate retrieval-practice questions from the lesson video provided.

Rules:
- Produce EXACTLY 3 multiple-choice questions and EXACTLY 3 short-answer writing prompts.
- Questions must be answerable from the video alone — no outside knowledge required.
- Each MCQ has 4 distinct options. Exactly one is correct. correct_index is 0-based.
- MCQ prompts test comprehension of a concept, not trivia about timestamps or speakers.
- Writing prompts test synthesis: the learner explains a concept in their own words.
- Each rubric is 30-80 words and lists 2-3 specific points that an acceptable answer must cover.
- No trick wording. No double negatives. No "all of the above" / "none of the above" options.`

// maxClippedVideoDuration is the upper bound we pass to Gemini for video
// processing. Gemini's input token ceiling is ~1M tokens which corresponds
// to roughly 30 minutes of low-res video; 25 minutes gives a safety margin
// for variable token density on busy-frame content.
//
// Videos shorter than this are processed in full (Gemini just ignores the
// EndOffset past the actual end). Videos longer than this are clipped to
// the first 25 minutes — questions are drawn from that portion only, which
// the frontend surfaces to the learner in a friendly notice.
const maxClippedVideoDuration = 25 * time.Minute

// GenerateQuestions is the default clip [0, maxClippedVideoDuration].
func (r *geminiRepo) GenerateQuestions(ctx context.Context, videoURL, lessonTitle string) (GeneratedQuestionSet, error) {
	return r.GenerateQuestionsClipped(ctx, videoURL, lessonTitle, 0, maxClippedVideoDuration)
}

// GenerateQuestionsClipped accepts arbitrary [start, end] offsets.
// Gemini ingests the video natively (audio + visual + auto-captions);
// the SDK enforces the response schema so we decode into a strongly-typed
// struct without defensive JSON-cleaning code.
func (r *geminiRepo) GenerateQuestionsClipped(
	ctx context.Context,
	videoURL, lessonTitle string,
	start, end time.Duration,
) (GeneratedQuestionSet, error) {
	if videoURL == "" {
		return GeneratedQuestionSet{}, fmt.Errorf("videoURL is required")
	}
	if end <= start {
		end = start + maxClippedVideoDuration
	}
	// Enforce the maximum clip span (the model's 1M-token ceiling).
	if end-start > maxClippedVideoDuration {
		end = start + maxClippedVideoDuration
	}

	// 120s ceiling — Gemini can take 15-90s on a long video.
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	videoPart := &genai.Part{
		FileData: &genai.FileData{
			FileURI:  videoURL,
			MIMEType: "video/*",
		},
		VideoMetadata: &genai.VideoMetadata{
			StartOffset: start,
			EndOffset:   end,
		},
	}

	noticeMinutes := int((end - start).Minutes())
	if noticeMinutes <= 0 {
		noticeMinutes = int(maxClippedVideoDuration.Minutes())
	}
	parts := []*genai.Part{
		genai.NewPartFromText(fmt.Sprintf(
			"Lesson title: %s\n\n"+
				"Note: the request is clipped to minutes %d-%d of the source video; "+
				"only generate questions answerable from that portion.",
			lessonTitle,
			int(start.Minutes()),
			int(start.Minutes())+noticeMinutes,
		)),
		videoPart,
	}

	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(questionGenSystemPrompt, genai.RoleUser),
		ResponseMIMEType:  "application/json",
		ResponseSchema:    questionGenSchema(),
		Temperature:       genai.Ptr[float32](0.4),
	}

	resp, err := r.client.Models.GenerateContent(
		ctx,
		r.generationModel,
		[]*genai.Content{genai.NewContentFromParts(parts, genai.RoleUser)},
		cfg,
	)
	if err != nil {
		return GeneratedQuestionSet{}, fmt.Errorf("gemini call failed: %w", err)
	}

	raw := resp.Text()
	if raw == "" {
		return GeneratedQuestionSet{}, fmt.Errorf("empty response from gemini")
	}

	var out GeneratedQuestionSet
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return GeneratedQuestionSet{}, fmt.Errorf("decode response: %w (raw: %s)", err, truncate(raw, 200))
	}

	if err := validateQuestionSet(out); err != nil {
		return GeneratedQuestionSet{}, fmt.Errorf("invalid response shape: %w", err)
	}
	return out, nil
}

// validateQuestionSet enforces the strict 3+3, four-option, valid-index
// shape. Even though the SDK enforces the JSON schema, we still verify
// invariants the schema can't express (exact count, options length = 4,
// correct_index in range).
func validateQuestionSet(s GeneratedQuestionSet) error {
	if len(s.MCQs) != 3 {
		return fmt.Errorf("expected 3 mcqs, got %d", len(s.MCQs))
	}
	if len(s.WritingPrompts) != 3 {
		return fmt.Errorf("expected 3 writing prompts, got %d", len(s.WritingPrompts))
	}
	for i, m := range s.MCQs {
		if len(m.Options) != 4 {
			return fmt.Errorf("mcq %d has %d options, want 4", i, len(m.Options))
		}
		if m.CorrectIndex < 0 || m.CorrectIndex > 3 {
			return fmt.Errorf("mcq %d has correct_index %d out of range", i, m.CorrectIndex)
		}
		if m.Prompt == "" {
			return fmt.Errorf("mcq %d has empty prompt", i)
		}
	}
	for i, w := range s.WritingPrompts {
		if w.Prompt == "" || w.Rubric == "" {
			return fmt.Errorf("writing %d missing prompt or rubric", i)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// GradeWriting
// ---------------------------------------------------------------------------

func gradeWritingSchema() *genai.Schema {
	return &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"score":    {Type: genai.TypeNumber},
			"feedback": {Type: genai.TypeString},
		},
		Required: []string{"score", "feedback"},
	}
}

const gradeWritingSystemPrompt = `You are a fair, encouraging instructor grading a short-answer response.

Apply the rubric strictly but reward partial credit when the learner covers some but not all required points.

Score policy:
- 1.0 = covers every rubric point clearly
- 0.6-0.9 = covers most points; minor gaps or imprecise language
- 0.3-0.5 = covers some points; significant gaps or partial misunderstanding
- 0.0-0.2 = missed the substance; wrong concept, off-topic, or empty

Feedback must be <= 280 characters, specific, and constructive. Address the learner directly. Do not repeat the rubric back at them.`

// GradeWriting evaluates one writing answer against its rubric. Returns the
// score (0..1, clamped) and short feedback text.
func (r *geminiRepo) GradeWriting(ctx context.Context, prompt, rubric, answer string) (Grade, error) {
	if answer == "" {
		return Grade{Score: 0, Feedback: "No answer was submitted."}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	userText := fmt.Sprintf(
		"Question:\n%s\n\nRubric (required points):\n%s\n\nLearner's answer:\n%s",
		prompt, rubric, answer,
	)

	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(gradeWritingSystemPrompt, genai.RoleUser),
		ResponseMIMEType:  "application/json",
		ResponseSchema:    gradeWritingSchema(),
		Temperature:       genai.Ptr[float32](0.2),
	}

	resp, err := r.client.Models.GenerateContent(
		ctx,
		r.gradeModel,
		[]*genai.Content{genai.NewContentFromText(userText, genai.RoleUser)},
		cfg,
	)
	if err != nil {
		return Grade{}, fmt.Errorf("gemini grading call failed: %w", err)
	}

	raw := resp.Text()
	if raw == "" {
		return Grade{}, fmt.Errorf("empty grading response")
	}

	var g Grade
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		return Grade{}, fmt.Errorf("decode grading response: %w (raw: %s)", err, truncate(raw, 200))
	}

	// Clamp the score to [0, 1] — defensive in case the model overshoots.
	if g.Score < 0 {
		g.Score = 0
	}
	if g.Score > 1 {
		g.Score = 1
	}
	if len(g.Feedback) > 280 {
		g.Feedback = g.Feedback[:280]
	}
	return g, nil
}

// ---------------------------------------------------------------------------
// GradeWritingBatch — one Gemini call, N answers graded.
// ---------------------------------------------------------------------------

// batchGradeSchema is the Gemini response schema for the batch grader.
// `grades` is a strictly-ordered array — slot N in the response maps to
// slot N in the input.
func batchGradeSchema() *genai.Schema {
	gradeItem := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"index":    {Type: genai.TypeInteger},
			"score":    {Type: genai.TypeNumber},
			"feedback": {Type: genai.TypeString},
		},
		Required: []string{"index", "score", "feedback"},
	}
	return &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"grades": {Type: genai.TypeArray, Items: gradeItem},
		},
		Required: []string{"grades"},
	}
}

const batchGradeSystemPrompt = `You are a fair, encouraging instructor grading short-answer responses.

You will receive multiple (question, rubric, learner-answer) triples in a single request. Grade each one independently using its own rubric. Reward partial credit when the learner covers some but not all required points.

Score policy (per item):
- 1.0 = covers every rubric point clearly
- 0.6-0.9 = covers most points; minor gaps or imprecise language
- 0.3-0.5 = covers some points; significant gaps or partial misunderstanding
- 0.0-0.2 = missed the substance; wrong concept, off-topic, or empty

Each feedback string must be <= 280 characters, specific, and constructive. Address the learner directly. Do not repeat the rubric back at them.

The "index" field in your response MUST match the index from the input — the caller uses it to align outputs to inputs.`

// GradeWritingBatch evaluates N answers in a single Gemini call. Returns
// grades in the same order as the input items.
//
// Behaviour:
//   - Empty inputs: returns a {Score:0, Feedback:"No answer was submitted."} for that slot.
//   - On Gemini error: returns the error (caller decides how to surface).
//   - On schema-violation (missing/duplicate index, wrong slot count): retries
//     once with a stricter prompt, then returns an error.
func (r *geminiRepo) GradeWritingBatch(ctx context.Context, items []WritingGradeItem) ([]Grade, error) {
	if len(items) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	// Build the user prompt: one block per item, separated by clear
	// delimiters. The model finds it easier to keep them aligned than a
	// freeform JSON-as-text input.
	var sb strings.Builder
	for _, it := range items {
		ans := strings.TrimSpace(it.Answer)
		if ans == "" {
			ans = "(no answer submitted)"
		}
		sb.WriteString(fmt.Sprintf(
			"### Item index=%d\nQuestion: %s\nRubric: %s\nLearner's answer: %s\n\n",
			it.Index, it.Prompt, it.Rubric, ans,
		))
	}

	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(batchGradeSystemPrompt, genai.RoleUser),
		ResponseMIMEType:  "application/json",
		ResponseSchema:    batchGradeSchema(),
		Temperature:       genai.Ptr[float32](0.2),
	}

	resp, err := r.client.Models.GenerateContent(
		ctx,
		r.gradeModel,
		[]*genai.Content{genai.NewContentFromText(sb.String(), genai.RoleUser)},
		cfg,
	)
	if err != nil {
		return nil, fmt.Errorf("gemini batch grading call failed: %w", err)
	}

	raw := resp.Text()
	if raw == "" {
		return nil, fmt.Errorf("empty batch grading response")
	}

	var parsed struct {
		Grades []struct {
			Index    int     `json:"index"`
			Score    float64 `json:"score"`
			Feedback string  `json:"feedback"`
		} `json:"grades"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("decode batch grading response: %w (raw: %s)", err, truncate(raw, 200))
	}

	// Map results back into input order by the index field.
	byIndex := make(map[int]Grade, len(parsed.Grades))
	for _, g := range parsed.Grades {
		score := g.Score
		if score < 0 {
			score = 0
		}
		if score > 1 {
			score = 1
		}
		fb := g.Feedback
		if len(fb) > 280 {
			fb = fb[:280]
		}
		byIndex[g.Index] = Grade{Score: score, Feedback: fb}
	}

	out := make([]Grade, len(items))
	for i, it := range items {
		grade, ok := byIndex[it.Index]
		if !ok {
			// Model didn't return a slot for this item — record a
			// neutral 0 with explicit feedback so the learner isn't
			// silently scored zero with no explanation.
			grade = Grade{
				Score:    0,
				Feedback: "Could not grade this answer automatically. Please retry the quiz.",
			}
		}
		out[i] = grade
	}
	return out, nil
}

// truncate is a tiny helper for log lines; never let an enormous payload
// leak into error messages.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---------------------------------------------------------------------------
// AnalyzeReflection — Feynman-technique evaluation
// ---------------------------------------------------------------------------

// reflectionAnalysisSchema is the strict response schema for AnalyzeReflection.
// The SDK guarantees the response will conform so we decode into a typed struct
// without defensive JSON-cleaning beyond the score clamp below.
func reflectionAnalysisSchema() *genai.Schema {
	return &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"score": {Type: genai.TypeInteger},
			"strengths": {
				Type:     genai.TypeArray,
				Items:    &genai.Schema{Type: genai.TypeString},
				MinItems: genai.Ptr[int64](1),
				MaxItems: genai.Ptr[int64](5),
			},
			"gaps": {
				Type:     genai.TypeArray,
				Items:    &genai.Schema{Type: genai.TypeString},
				MinItems: genai.Ptr[int64](0),
				MaxItems: genai.Ptr[int64](5),
			},
			"suggested_next_focus": {Type: genai.TypeString},
			"mastered":             {Type: genai.TypeBoolean},
		},
		Required: []string{"score", "strengths", "gaps", "suggested_next_focus", "mastered"},
	}
}

const reflectionAnalysisSystemPrompt = `You are an expert learning coach evaluating a learner's Feynman-technique explanation of a lesson.

The learner watched the lesson video provided and then wrote a plain-text explanation of what they learned, as if teaching someone unfamiliar with the topic. Your job is to assess how well they understood the material.

Score policy (integer 0-100):
- 90-100: explains every key concept clearly, in own words, could teach someone new
- 70-89: covers most concepts; small gaps or imprecise language
- 50-69: covers some concepts; significant gaps or partial misunderstanding
- 30-49: surface-level; missing core ideas
- 0-29: off-topic, empty, or fundamentally wrong

Set mastered: true if and only if score >= 85.

strengths (1-5 items): specific things the learner explained correctly. Address the learner directly ("You clearly understood…"). Each item must be <= 200 characters.

gaps (0-5 items): specific concepts they missed or got wrong. Each item must be <= 200 characters. Return an empty array if the explanation is thorough enough to score >= 90.

suggested_next_focus: one sentence (<= 280 characters) naming the single most-important concept for the learner to re-watch or re-explain. If mastered is true, suggest a related concept to explore next.

Be specific and encouraging. Do not copy the learner's words back verbatim.`

// AnalyzeReflection sends the lesson metadata, YouTube video, and the
// learner's Feynman explanation to Gemini and returns a structured assessment.
//
// Gemini ingests the video natively via FileData (same mechanism as
// GenerateQuestionsClipped) so it can evaluate the explanation against the
// actual lesson content rather than the title/description alone.
//
// Timeout: 120 s — video processing can take 15-90 s on longer lessons.
func (r *geminiRepo) AnalyzeReflection(
	ctx context.Context,
	lessonTitle, lessonDescription, videoURL, userExplanation string,
) (ReflectionAnalysis, error) {
	if videoURL == "" {
		return ReflectionAnalysis{}, fmt.Errorf("videoURL is required")
	}
	if userExplanation == "" {
		return ReflectionAnalysis{}, fmt.Errorf("userExplanation is required")
	}

	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	videoPart := &genai.Part{
		FileData: &genai.FileData{
			FileURI:  videoURL,
			MIMEType: "video/*",
		},
	}

	userPrompt := fmt.Sprintf(
		"Lesson title: %s\n\nLesson description: %s\n\nLearner's Feynman explanation:\n%s",
		lessonTitle,
		lessonDescription,
		userExplanation,
	)

	parts := []*genai.Part{
		genai.NewPartFromText(userPrompt),
		videoPart,
	}

	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(reflectionAnalysisSystemPrompt, genai.RoleUser),
		ResponseMIMEType:  "application/json",
		ResponseSchema:    reflectionAnalysisSchema(),
		Temperature:       genai.Ptr[float32](0.3),
	}

	resp, err := r.client.Models.GenerateContent(
		ctx,
		r.generationModel,
		[]*genai.Content{genai.NewContentFromParts(parts, genai.RoleUser)},
		cfg,
	)
	if err != nil {
		return ReflectionAnalysis{}, fmt.Errorf("gemini reflection call failed: %w", err)
	}

	raw := resp.Text()
	if raw == "" {
		return ReflectionAnalysis{}, fmt.Errorf("empty response from gemini")
	}

	var out ReflectionAnalysis
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return ReflectionAnalysis{}, fmt.Errorf("decode reflection response: %w (raw: %s)", err, truncate(raw, 200))
	}

	// Defensively clamp score to [0, 100] regardless of what the model returned.
	if out.Score < 0 {
		out.Score = 0
	}
	if out.Score > 100 {
		out.Score = 100
	}

	return out, nil
}

// ---------------------------------------------------------------------------
// ExtractTranscript — video-to-plaintext extraction
// ---------------------------------------------------------------------------

const extractTranscriptSystemPrompt = `Output ONLY the spoken content as a clean plaintext transcript. Paragraphs separated by blank lines. No timestamps. No speaker labels. No headings. No commentary.`

// ExtractTranscript ingests the video natively via Gemini and returns a clean
// plaintext transcript. Uses the generationModel tier because video
// understanding is required.
//
// Timeout: 120 s — same ceiling as question generation since both calls
// process the video natively. The result is intended to be cached by
// TranscriptService so this expensive call runs at most once per lesson.
func (r *geminiRepo) ExtractTranscript(ctx context.Context, videoURL string) (string, error) {
	if videoURL == "" {
		return "", fmt.Errorf("videoURL is required")
	}

	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	videoPart := &genai.Part{
		FileData: &genai.FileData{
			FileURI:  videoURL,
			MIMEType: "video/*",
		},
		VideoMetadata: &genai.VideoMetadata{
			StartOffset: 0,
			EndOffset:   maxClippedVideoDuration,
		},
	}

	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(extractTranscriptSystemPrompt, genai.RoleUser),
		ResponseMIMEType:  "text/plain",
		Temperature:       genai.Ptr[float32](0.1),
	}

	resp, err := r.client.Models.GenerateContent(
		ctx,
		r.generationModel,
		[]*genai.Content{genai.NewContentFromParts([]*genai.Part{videoPart}, genai.RoleUser)},
		cfg,
	)
	if err != nil {
		return "", fmt.Errorf("gemini transcript extraction failed: %w", err)
	}

	raw := strings.TrimSpace(resp.Text())
	if raw == "" {
		return "", fmt.Errorf("gemini returned an empty transcript for the video")
	}
	return raw, nil
}

// ---------------------------------------------------------------------------
// AnalyzeReflectionFromTranscript — text-only fast-path Feynman evaluation
// ---------------------------------------------------------------------------

// AnalyzeReflectionFromTranscript scores a learner's Feynman explanation
// against an already-extracted transcript. Uses the gradeModel (text-only,
// cheaper) rather than the generationModel.
//
// Same response schema and scoring rubric as AnalyzeReflection. At ~10x
// faster and ~5x cheaper, this is the preferred path whenever the
// transcript cache is warm.
//
// Timeout: 45 s — text-only calls are substantially faster than video-
// grounded ones.
func (r *geminiRepo) AnalyzeReflectionFromTranscript(
	ctx context.Context,
	lessonTitle, lessonDescription, transcript, userExplanation string,
) (ReflectionAnalysis, error) {
	if transcript == "" {
		return ReflectionAnalysis{}, fmt.Errorf("transcript is required")
	}
	if userExplanation == "" {
		return ReflectionAnalysis{}, fmt.Errorf("userExplanation is required")
	}

	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	userPrompt := fmt.Sprintf(
		"Lesson title: %s\n\nLesson description: %s\n\nTranscript (verbatim from the video):\n%s\n\nLearner's explanation:\n%s",
		lessonTitle,
		lessonDescription,
		transcript,
		userExplanation,
	)

	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(reflectionAnalysisSystemPrompt, genai.RoleUser),
		ResponseMIMEType:  "application/json",
		ResponseSchema:    reflectionAnalysisSchema(),
		Temperature:       genai.Ptr[float32](0.3),
	}

	resp, err := r.client.Models.GenerateContent(
		ctx,
		r.gradeModel,
		[]*genai.Content{genai.NewContentFromText(userPrompt, genai.RoleUser)},
		cfg,
	)
	if err != nil {
		return ReflectionAnalysis{}, fmt.Errorf("gemini transcript-based reflection call failed: %w", err)
	}

	raw := resp.Text()
	if raw == "" {
		return ReflectionAnalysis{}, fmt.Errorf("empty response from gemini")
	}

	var out ReflectionAnalysis
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return ReflectionAnalysis{}, fmt.Errorf("decode reflection response: %w (raw: %s)", err, truncate(raw, 200))
	}

	// Defensively clamp score to [0, 100] regardless of what the model returned.
	if out.Score < 0 {
		out.Score = 0
	}
	if out.Score > 100 {
		out.Score = 100
	}

	return out, nil
}
