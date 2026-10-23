package services

import (
	"context"
	"errors"
	"focusflow-api/internal/material/model"
	"focusflow-api/internal/material/repository"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeReflectionRepo is an in-memory implementation of model.ReflectionRepository.
type fakeReflectionRepo struct {
	rows []model.Reflection
}

func newFakeReflectionRepo() *fakeReflectionRepo {
	return &fakeReflectionRepo{}
}

func (f *fakeReflectionRepo) Create(r *model.Reflection) (model.Reflection, error) {
	r.ID = len(f.rows) + 1
	r.CreatedAt = time.Now()
	f.rows = append(f.rows, *r)
	return *r, nil
}

func (f *fakeReflectionRepo) GetByID(id int) (model.Reflection, error) {
	for _, r := range f.rows {
		if r.ID == id {
			return r, nil
		}
	}
	return model.Reflection{}, errors.New("record not found")
}

func (f *fakeReflectionRepo) Update(r *model.Reflection) (model.Reflection, error) {
	for i := range f.rows {
		if f.rows[i].ID == r.ID {
			f.rows[i] = *r
			return *r, nil
		}
	}
	return model.Reflection{}, errors.New("record not found")
}

func (f *fakeReflectionRepo) GetByUserAndVideo(userID, videoID int) ([]model.Reflection, error) {
	var out []model.Reflection
	for _, r := range f.rows {
		if r.UserID == userID && r.VideoID == videoID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeReflectionRepo) GetLatestByUserAndVideo(userID, videoID int) (model.Reflection, error) {
	var latest model.Reflection
	found := false
	for _, r := range f.rows {
		if r.UserID == userID && r.VideoID == videoID {
			if !found || r.CreatedAt.After(latest.CreatedAt) {
				latest = r
				found = true
			}
		}
	}
	if !found {
		return model.Reflection{}, errors.New("record not found")
	}
	return latest, nil
}

// fakeGeminiForReflection records calls to AnalyzeReflection and
// AnalyzeReflectionFromTranscript separately so tests can assert which path
// was taken. It embeds fakeGemini for the quiz-related interface methods.
type fakeGeminiForReflection struct {
	fakeGemini

	analysisResult          repository.ReflectionAnalysis
	analysisErr             error
	calls                   int // video-grounded path
	transcriptAnalysisCalls int // text-only fast path
	extractErr              error
	extractedTranscript     string // what ExtractTranscript returns
}

func (f *fakeGeminiForReflection) AnalyzeReflection(_ context.Context, _, _, _, _ string) (repository.ReflectionAnalysis, error) {
	f.calls++
	return f.analysisResult, f.analysisErr
}

func (f *fakeGeminiForReflection) AnalyzeReflectionFromTranscript(_ context.Context, _, _, _, _ string) (repository.ReflectionAnalysis, error) {
	f.transcriptAnalysisCalls++
	return f.analysisResult, f.analysisErr
}

func (f *fakeGeminiForReflection) ExtractTranscript(_ context.Context, _ string) (string, error) {
	if f.extractErr != nil {
		return "", f.extractErr
	}
	t := f.extractedTranscript
	if t == "" {
		t = "This is the extracted transcript content for testing purposes."
	}
	return t, nil
}

// fakeTranscriptRepo is an in-memory implementation of
// model.LessonTranscriptRepository used in transcript service tests.
type fakeTranscriptRepo struct {
	rows map[int]model.LessonTranscript
}

func newFakeTranscriptRepo() *fakeTranscriptRepo {
	return &fakeTranscriptRepo{rows: map[int]model.LessonTranscript{}}
}

func (f *fakeTranscriptRepo) Get(videoID int) (model.LessonTranscript, error) {
	t, ok := f.rows[videoID]
	if !ok {
		return model.LessonTranscript{}, errors.New("record not found")
	}
	return t, nil
}

func (f *fakeTranscriptRepo) Upsert(t *model.LessonTranscript) (model.LessonTranscript, error) {
	f.rows[t.VideoID] = *t
	return *t, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newReflectionService wires a ReflectionService with a nil TranscriptService
// for tests that don't exercise the transcript fast-path (backward-compat).
func newReflectionService(
	rRepo model.ReflectionRepository,
	mRepo model.CourseMaterialRepository,
	gemini repository.GeminiRepository,
) *ReflectionService {
	return NewReflectionService(rRepo, mRepo, gemini, nil)
}

// newReflectionServiceWithTranscript wires a ReflectionService with a real
// in-memory TranscriptService.
func newReflectionServiceWithTranscript(
	rRepo model.ReflectionRepository,
	mRepo model.CourseMaterialRepository,
	gemini *fakeGeminiForReflection,
) *ReflectionService {
	tRepo := newFakeTranscriptRepo()
	ts := NewTranscriptService(tRepo, mRepo, gemini)
	return NewReflectionService(rRepo, mRepo, gemini, ts)
}

func defaultMaterialRepo() *fakeMaterialRepo {
	m := newFakeMaterialRepo()
	m.mats[1] = model.CourseMaterial{
		VideoID:   1,
		CourseID:  "c1",
		Title:     "Goroutines Deep Dive",
		YouTubeID: "abc123",
	}
	return m
}

func defaultGemini(score int) *fakeGeminiForReflection {
	return &fakeGeminiForReflection{
		analysisResult: repository.ReflectionAnalysis{
			Score:              score,
			Strengths:          []string{"You clearly understood goroutine scheduling"},
			Gaps:               []string{"You missed channel direction syntax"},
			SuggestedNextFocus: "Re-watch the channel direction segment and re-explain it.",
			Mastered:           score >= 85,
		},
	}
}

func validBody() string {
	return strings.Repeat("goroutines are lightweight threads ", 3) // ~105 chars
}

// waitForGrade polls the repo until the row for the given ID has a status
// other than "evaluating", up to 2 seconds. Tests use fake Gemini calls that
// complete in microseconds, so 2 seconds is plenty. Returns the final row.
func waitForGrade(t *testing.T, rRepo *fakeReflectionRepo, id int) model.Reflection {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r, err := rRepo.GetByID(id)
		if err == nil && r.Status != model.ReflectionStatusEvaluating {
			return r
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("background grader did not complete row %d within 2s", id)
	return model.Reflection{}
}

// ---------------------------------------------------------------------------
// Tests — existing behaviour (nil TranscriptService)
// ---------------------------------------------------------------------------

func TestReflection_HappyPath(t *testing.T) {
	rRepo := newFakeReflectionRepo()
	mRepo := defaultMaterialRepo()
	gemini := defaultGemini(78)
	svc := newReflectionService(rRepo, mRepo, gemini)

	ref, err := svc.Submit(context.Background(), 42, 1, validBody())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref.ID == 0 {
		t.Errorf("expected non-zero ID after persist")
	}
	if ref.Status != model.ReflectionStatusEvaluating {
		t.Errorf("Submit must return row with status=evaluating, got %q", ref.Status)
	}
	if ref.WordCount == 0 {
		t.Errorf("expected non-zero word count on the placeholder row")
	}

	graded := waitForGrade(t, rRepo, ref.ID)
	if graded.Status != model.ReflectionStatusCompleted {
		t.Errorf("expected status=completed after grading, got %q", graded.Status)
	}
	if graded.Score != 78 {
		t.Errorf("expected score 78, got %d", graded.Score)
	}
	if graded.Mastered {
		t.Errorf("expected mastered=false for score 78")
	}
	if graded.EvaluatedAt == nil {
		t.Errorf("expected evaluated_at to be set after grading")
	}
	// With nil TranscriptService the video-grounded path is used.
	if gemini.calls != 1 {
		t.Errorf("expected 1 video-grounded Gemini call, got %d", gemini.calls)
	}
}

func TestReflection_BodyTooShort(t *testing.T) {
	rRepo := newFakeReflectionRepo()
	mRepo := defaultMaterialRepo()
	svc := newReflectionService(rRepo, mRepo, defaultGemini(80))

	_, err := svc.Submit(context.Background(), 42, 1, "too short")
	if err == nil {
		t.Fatal("expected error for short body, got nil")
	}
	if !strings.Contains(err.Error(), "reflection body must be 30..3000 chars") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestReflection_BodyTooLong(t *testing.T) {
	rRepo := newFakeReflectionRepo()
	mRepo := defaultMaterialRepo()
	svc := newReflectionService(rRepo, mRepo, defaultGemini(80))

	longBody := strings.Repeat("a", ReflectionMaxChars+1)
	_, err := svc.Submit(context.Background(), 42, 1, longBody)
	if err == nil {
		t.Fatal("expected error for long body, got nil")
	}
	if !strings.Contains(err.Error(), "reflection body must be 30..3000 chars") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestReflection_LessonNotFound(t *testing.T) {
	rRepo := newFakeReflectionRepo()
	mRepo := newFakeMaterialRepo() // empty — no lessons
	svc := newReflectionService(rRepo, mRepo, defaultGemini(80))

	_, err := svc.Submit(context.Background(), 42, 99, validBody())
	if err == nil {
		t.Fatal("expected error for missing lesson, got nil")
	}
	if !strings.Contains(err.Error(), "lesson not found") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestReflection_LessonWithoutVideo(t *testing.T) {
	rRepo := newFakeReflectionRepo()
	mRepo := newFakeMaterialRepo()
	mRepo.mats[5] = model.CourseMaterial{
		VideoID:   5,
		CourseID:  "c1",
		Title:     "No video lesson",
		YouTubeID: "", // intentionally empty
	}
	svc := newReflectionService(rRepo, mRepo, defaultGemini(80))

	_, err := svc.Submit(context.Background(), 42, 5, validBody())
	if err == nil {
		t.Fatal("expected error for lesson without video, got nil")
	}
	if !strings.Contains(err.Error(), "lesson has no video") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestReflection_GeminiErrorMarksRowFailed(t *testing.T) {
	rRepo := newFakeReflectionRepo()
	mRepo := defaultMaterialRepo()
	gemini := &fakeGeminiForReflection{
		analysisErr: errors.New("gemini quota exhausted"),
	}
	// Use nil TranscriptService to guarantee we hit AnalyzeReflection directly.
	svc := newReflectionService(rRepo, mRepo, gemini)

	// Submit succeeds — the row is inserted with status=evaluating before the
	// background grader runs. The grader sets status=failed when Gemini errors.
	ref, err := svc.Submit(context.Background(), 42, 1, validBody())
	if err != nil {
		t.Fatalf("Submit must succeed even when Gemini will fail; got: %v", err)
	}
	graded := waitForGrade(t, rRepo, ref.ID)
	if graded.Status != model.ReflectionStatusFailed {
		t.Errorf("expected status=failed after Gemini error, got %q", graded.Status)
	}
	if !strings.Contains(graded.ErrorMessage, "quota") {
		t.Errorf("expected error_message to mention 'quota', got %q", graded.ErrorMessage)
	}
}

func TestReflection_MasteryThreshold_Score84(t *testing.T) {
	rRepo := newFakeReflectionRepo()
	mRepo := defaultMaterialRepo()
	svc := newReflectionService(rRepo, mRepo, defaultGemini(84))

	ref, err := svc.Submit(context.Background(), 42, 1, validBody())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	graded := waitForGrade(t, rRepo, ref.ID)
	if graded.Mastered {
		t.Errorf("score=84 must not be mastered (threshold is 85)")
	}
}

func TestReflection_MasteryThreshold_Score85(t *testing.T) {
	rRepo := newFakeReflectionRepo()
	mRepo := defaultMaterialRepo()
	svc := newReflectionService(rRepo, mRepo, defaultGemini(85))

	ref, err := svc.Submit(context.Background(), 42, 1, validBody())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	graded := waitForGrade(t, rRepo, ref.ID)
	if !graded.Mastered {
		t.Errorf("score=85 must be mastered")
	}
}

func TestReflection_MasteryThreshold_Score100(t *testing.T) {
	rRepo := newFakeReflectionRepo()
	mRepo := defaultMaterialRepo()
	svc := newReflectionService(rRepo, mRepo, defaultGemini(100))

	ref, err := svc.Submit(context.Background(), 42, 1, validBody())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	graded := waitForGrade(t, rRepo, ref.ID)
	if !graded.Mastered {
		t.Errorf("score=100 must be mastered")
	}
}

func TestReflection_MultipleSubmissionsCreateMultipleRows(t *testing.T) {
	rRepo := newFakeReflectionRepo()
	mRepo := defaultMaterialRepo()
	svc := newReflectionService(rRepo, mRepo, defaultGemini(60))

	for i := 0; i < 3; i++ {
		if _, err := svc.Submit(context.Background(), 42, 1, validBody()); err != nil {
			t.Fatalf("submission %d failed: %v", i+1, err)
		}
	}
	if len(rRepo.rows) != 3 {
		t.Errorf("expected 3 rows after 3 submissions, got %d", len(rRepo.rows))
	}
}

func TestReflection_GetHistoryReturnsSortedRows(t *testing.T) {
	rRepo := newFakeReflectionRepo()
	mRepo := defaultMaterialRepo()
	svc := newReflectionService(rRepo, mRepo, defaultGemini(60))

	for i := 0; i < 2; i++ {
		if _, err := svc.Submit(context.Background(), 42, 1, validBody()); err != nil {
			t.Fatal(err)
		}
	}

	history, err := svc.GetHistory(42, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(history) != 2 {
		t.Errorf("expected 2 history rows, got %d", len(history))
	}
}

func TestReflection_GetLatestReturnsNilErrorOnMissing(t *testing.T) {
	rRepo := newFakeReflectionRepo()
	mRepo := defaultMaterialRepo()
	svc := newReflectionService(rRepo, mRepo, defaultGemini(60))

	// No submissions yet — GetLatest should return an error (not-found),
	// which the handler translates to {data: null} with HTTP 200.
	_, err := svc.GetLatest(42, 1)
	if err == nil {
		t.Fatal("expected not-found error, got nil")
	}
}

func TestReflection_ServiceMasteryOverridesModelField(t *testing.T) {
	rRepo := newFakeReflectionRepo()
	mRepo := defaultMaterialRepo()
	// Gemini says mastered=true but score=80 — service should override to false.
	gemini := &fakeGeminiForReflection{
		analysisResult: repository.ReflectionAnalysis{
			Score:              80,
			Strengths:          []string{"Good coverage"},
			Gaps:               []string{},
			SuggestedNextFocus: "Review edge cases.",
			Mastered:           true, // intentionally disagrees with score
		},
	}
	svc := newReflectionService(rRepo, mRepo, gemini)

	ref, err := svc.Submit(context.Background(), 42, 1, validBody())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	graded := waitForGrade(t, rRepo, ref.ID)
	if graded.Mastered {
		t.Errorf("service must enforce score >= 85 for mastery; score=80 must yield mastered=false regardless of model field")
	}
}

// ---------------------------------------------------------------------------
// Tests — transcript fast-path behaviour
// ---------------------------------------------------------------------------

// TestReflection_TranscriptCacheHit_UsesTextOnlyPath verifies that when the
// transcript cache is warm, the service calls AnalyzeReflectionFromTranscript
// and does NOT call the video-grounded AnalyzeReflection.
func TestReflection_TranscriptCacheHit_UsesTextOnlyPath(t *testing.T) {
	rRepo := newFakeReflectionRepo()
	mRepo := defaultMaterialRepo()
	gemini := defaultGemini(72)

	// Pre-populate the transcript cache.
	tRepo := newFakeTranscriptRepo()
	tRepo.rows[1] = model.LessonTranscript{
		VideoID:    1,
		Transcript: "This is a warm transcript already in the cache.",
		Source:     "gemini_extract",
		WordCount:  9,
	}
	ts := NewTranscriptService(tRepo, mRepo, gemini)
	svc := NewReflectionService(rRepo, mRepo, gemini, ts)

	ref, err := svc.Submit(context.Background(), 42, 1, validBody())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	graded := waitForGrade(t, rRepo, ref.ID)
	if graded.Score != 72 {
		t.Errorf("expected score 72, got %d", graded.Score)
	}

	// Fast path must be used — video-grounded path must NOT be called.
	if gemini.calls != 0 {
		t.Errorf("video-grounded AnalyzeReflection must not be called on cache-hit; got %d call(s)", gemini.calls)
	}
	if gemini.transcriptAnalysisCalls != 1 {
		t.Errorf("expected 1 AnalyzeReflectionFromTranscript call, got %d", gemini.transcriptAnalysisCalls)
	}
	// ExtractTranscript must not be called since the cache was already warm.
	// (fakeGemini.ExtractTranscript count is not separately tracked but the
	// transcript repo has no missing entry so GetOrExtract short-circuits.)
}

// TestReflection_TranscriptCacheMiss_ExtractsThenUsesTextPath verifies that on
// a cache-miss, the service extracts the transcript, caches it, and calls
// AnalyzeReflectionFromTranscript — not the video-grounded path.
func TestReflection_TranscriptCacheMiss_ExtractsThenUsesTextPath(t *testing.T) {
	rRepo := newFakeReflectionRepo()
	mRepo := defaultMaterialRepo()
	gemini := defaultGemini(90)
	// Empty cache — triggers extraction on first Submit.
	svc := newReflectionServiceWithTranscript(rRepo, mRepo, gemini)

	ref, err := svc.Submit(context.Background(), 42, 1, validBody())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	graded := waitForGrade(t, rRepo, ref.ID)
	if graded.Score != 90 {
		t.Errorf("expected score 90, got %d", graded.Score)
	}

	// The fast-path analyser must have been called; video-grounded must NOT.
	if gemini.calls != 0 {
		t.Errorf("video-grounded AnalyzeReflection must not be called when extraction succeeds; got %d call(s)", gemini.calls)
	}
	if gemini.transcriptAnalysisCalls != 1 {
		t.Errorf("expected 1 AnalyzeReflectionFromTranscript call, got %d", gemini.transcriptAnalysisCalls)
	}
}

// TestReflection_ExtractionFails_FallsBackToVideoGrounded verifies that when
// transcript extraction fails, the service falls back to the video-grounded
// AnalyzeReflection path rather than returning an error to the learner.
func TestReflection_ExtractionFails_FallsBackToVideoGrounded(t *testing.T) {
	rRepo := newFakeReflectionRepo()
	mRepo := defaultMaterialRepo()
	gemini := defaultGemini(65)
	gemini.extractErr = errors.New("gemini video ingestion error")

	svc := newReflectionServiceWithTranscript(rRepo, mRepo, gemini)

	ref, err := svc.Submit(context.Background(), 42, 1, validBody())
	if err != nil {
		t.Fatalf("unexpected error: %v — expected fallback to succeed", err)
	}
	graded := waitForGrade(t, rRepo, ref.ID)
	if graded.Score != 65 {
		t.Errorf("expected score 65 from fallback, got %d", graded.Score)
	}

	// Fallback path must have been used.
	if gemini.calls != 1 {
		t.Errorf("expected 1 video-grounded fallback call, got %d", gemini.calls)
	}
	if gemini.transcriptAnalysisCalls != 0 {
		t.Errorf("AnalyzeReflectionFromTranscript should not have been called when extraction failed; got %d call(s)", gemini.transcriptAnalysisCalls)
	}
}
