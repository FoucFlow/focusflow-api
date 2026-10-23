package services

import (
	"context"
	"encoding/json"
	"errors"
	"focusflow-api/internal/material/model"
	"focusflow-api/internal/material/repository"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/datatypes"
)

// ----- Fakes ----------------------------------------------------------------

type fakeGemini struct {
	mu        sync.Mutex
	gen       repository.GeneratedQuestionSet
	genErr    error
	gradeErr  error
	gradeFunc func(prompt, rubric, answer string) (repository.Grade, error)
	calls     int
}

func (f *fakeGemini) GenerateQuestions(_ context.Context, _, _ string) (repository.GeneratedQuestionSet, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.gen, f.genErr
}
func (f *fakeGemini) GenerateQuestionsClipped(_ context.Context, _, _ string, _, _ time.Duration) (repository.GeneratedQuestionSet, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.gen, f.genErr
}
func (f *fakeGemini) GradeWriting(_ context.Context, prompt, rubric, answer string) (repository.Grade, error) {
	if f.gradeErr != nil {
		return repository.Grade{}, f.gradeErr
	}
	if f.gradeFunc != nil {
		return f.gradeFunc(prompt, rubric, answer)
	}
	return repository.Grade{Score: 0.8, Feedback: "ok"}, nil
}
func (f *fakeGemini) GradeWritingBatch(_ context.Context, items []repository.WritingGradeItem) ([]repository.Grade, error) {
	if f.gradeErr != nil {
		return nil, f.gradeErr
	}
	out := make([]repository.Grade, len(items))
	for i, it := range items {
		if f.gradeFunc != nil {
			g, err := f.gradeFunc(it.Prompt, it.Rubric, it.Answer)
			if err != nil {
				return nil, err
			}
			out[i] = g
		} else {
			out[i] = repository.Grade{Score: 0.8, Feedback: "ok"}
		}
	}
	return out, nil
}
func (f *fakeGemini) AnalyzeReflection(_ context.Context, _, _, _, _ string) (repository.ReflectionAnalysis, error) {
	return repository.ReflectionAnalysis{}, nil
}
func (f *fakeGemini) ExtractTranscript(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (f *fakeGemini) AnalyzeReflectionFromTranscript(_ context.Context, _, _, _, _ string) (repository.ReflectionAnalysis, error) {
	return repository.ReflectionAnalysis{}, nil
}
func (f *fakeGemini) Close() error { return nil }

type fakeQuestionRepo struct {
	rows []model.LessonQuestion
}

func (f *fakeQuestionRepo) ListByVideo(videoID int) ([]model.LessonQuestion, error) {
	latest, _ := f.LatestSetNumber(videoID)
	if latest == 0 {
		return nil, nil
	}
	return f.ListByVideoSet(videoID, latest)
}
func (f *fakeQuestionRepo) ListByVideoSet(videoID, setNumber int) ([]model.LessonQuestion, error) {
	var out []model.LessonQuestion
	for _, r := range f.rows {
		if r.VideoID == videoID && (r.SetNumber == setNumber || (r.SetNumber == 0 && setNumber == 1)) {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *fakeQuestionRepo) LatestSetNumber(videoID int) (int, error) {
	max := 0
	for _, r := range f.rows {
		if r.VideoID == videoID {
			s := r.SetNumber
			if s == 0 {
				s = 1
			}
			if s > max {
				max = s
			}
		}
	}
	return max, nil
}
func (f *fakeQuestionRepo) CountByVideo(videoID int) (int64, error) {
	n := 0
	for _, r := range f.rows {
		if r.VideoID == videoID {
			n++
		}
	}
	return int64(n), nil
}
func (f *fakeQuestionRepo) BulkInsert(qs []model.LessonQuestion) ([]model.LessonQuestion, error) {
	for i := range qs {
		qs[i].ID = len(f.rows) + i + 1
		if qs[i].SetNumber == 0 {
			qs[i].SetNumber = 1
		}
	}
	f.rows = append(f.rows, qs...)
	return qs, nil
}

type fakeQuizRepo struct {
	mu       sync.Mutex
	attempts []model.QuizAttempt
}

func (f *fakeQuizRepo) CreateAttempt(a *model.QuizAttempt) (model.QuizAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a.ID = len(f.attempts) + 1
	f.attempts = append(f.attempts, *a)
	return *a, nil
}
func (f *fakeQuizRepo) UpdateAttempt(a *model.QuizAttempt) (model.QuizAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.attempts {
		if f.attempts[i].ID == a.ID {
			// Merge the update: keep fields not set by the caller.
			existing := f.attempts[i]
			existing.Status = a.Status
			existing.ErrorMessage = a.ErrorMessage
			existing.WritingScore = a.WritingScore
			existing.TotalScore = a.TotalScore
			existing.UpdatedAt = a.UpdatedAt
			if len(a.Answers) > 0 {
				existing.Answers = a.Answers
			}
			f.attempts[i] = existing
			return existing, nil
		}
	}
	return model.QuizAttempt{}, errors.New("not found")
}
func (f *fakeQuizRepo) ListByUser(userID, limit, offset int) ([]model.QuizAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.QuizAttempt
	for _, a := range f.attempts {
		if a.UserID == userID {
			out = append(out, a)
		}
	}
	return out, nil
}
func (f *fakeQuizRepo) GetAttemptByID(id int) (model.QuizAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.attempts {
		if a.ID == id {
			return a, nil
		}
	}
	return model.QuizAttempt{}, errors.New("not found")
}
func (f *fakeQuizRepo) LatestByUserVideo(userID, videoID int) (model.QuizAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.attempts) - 1; i >= 0; i-- {
		a := f.attempts[i]
		if a.UserID == userID && a.VideoID == videoID {
			return a, nil
		}
	}
	return model.QuizAttempt{}, errors.New("no attempt")
}

// ----- helpers --------------------------------------------------------------

func makeQuestionSet(videoID int) []model.LessonQuestion {
	opts, _ := json.Marshal([]string{"Apple", "Banana", "Cherry", "Date"})
	correct := 2 // "Cherry"
	return []model.LessonQuestion{
		{ID: 1, VideoID: videoID, Position: 1, QuestionType: model.QuestionTypeMCQ, Prompt: "Q1", Options: datatypes.JSON(opts), CorrectIndex: &correct},
		{ID: 2, VideoID: videoID, Position: 2, QuestionType: model.QuestionTypeMCQ, Prompt: "Q2", Options: datatypes.JSON(opts), CorrectIndex: &correct},
		{ID: 3, VideoID: videoID, Position: 3, QuestionType: model.QuestionTypeMCQ, Prompt: "Q3", Options: datatypes.JSON(opts), CorrectIndex: &correct},
		{ID: 4, VideoID: videoID, Position: 4, QuestionType: model.QuestionTypeWriting, Prompt: "W1", Rubric: "Cover X, Y, Z"},
		{ID: 5, VideoID: videoID, Position: 5, QuestionType: model.QuestionTypeWriting, Prompt: "W2", Rubric: "Cover A, B, C"},
		{ID: 6, VideoID: videoID, Position: 6, QuestionType: model.QuestionTypeWriting, Prompt: "W3", Rubric: "Cover P, Q, R"},
	}
}

func newServiceWithQuestions(videoID int) (*QuizService, *fakeGemini, *fakeQuestionRepo, *fakeQuizRepo, *fakeMaterialRepo) {
	gemini := &fakeGemini{}
	qRepo := &fakeQuestionRepo{rows: makeQuestionSet(videoID)}
	zRepo := &fakeQuizRepo{}
	mRepo := newFakeMaterialRepo()
	mRepo.mats[videoID] = model.CourseMaterial{VideoID: videoID, CourseID: "1", Title: "Demo", YouTubeID: "abc"}
	takeawayRepo := newFakeTakeawayRepo()
	takeaway := NewTakeawayService(takeawayRepo, mRepo)
	svc := NewQuizService(gemini, qRepo, zRepo, mRepo, takeaway)
	return svc, gemini, qRepo, zRepo, mRepo
}

// waitForStatus polls the fake repo until the attempt reaches the target
// status or the deadline fires.
func waitForStatus(t *testing.T, repo *fakeQuizRepo, attemptID int, target model.AttemptStatus) model.QuizAttempt {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a, err := repo.GetAttemptByID(attemptID)
		if err == nil && a.Status == target {
			return a
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("attempt %d did not reach status %q in time", attemptID, target)
	return model.QuizAttempt{}
}

// ----- Tests ----------------------------------------------------------------

func TestGenerateOrFetch_IdempotentOnCachedQuestions(t *testing.T) {
	svc, gemini, _, _, _ := newServiceWithQuestions(42)

	got, err := svc.GenerateOrFetch(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("expected 6 cached questions, got %d", len(got))
	}
	if gemini.calls != 0 {
		t.Fatalf("expected zero Gemini calls when cached, got %d", gemini.calls)
	}
}

func TestGenerateOrFetch_CallsGeminiWhenNoQuestionsExist(t *testing.T) {
	svc, gemini, qRepo, _, _ := newServiceWithQuestions(99)
	qRepo.rows = nil
	gemini.gen = repository.GeneratedQuestionSet{
		MCQs: []repository.GeneratedMCQ{
			{Prompt: "Q1", Options: []string{"a", "b", "c", "d"}, CorrectIndex: 0},
			{Prompt: "Q2", Options: []string{"a", "b", "c", "d"}, CorrectIndex: 1},
			{Prompt: "Q3", Options: []string{"a", "b", "c", "d"}, CorrectIndex: 2},
		},
		WritingPrompts: []repository.GeneratedWriting{
			{Prompt: "W1", Rubric: "rubric"},
			{Prompt: "W2", Rubric: "rubric"},
			{Prompt: "W3", Rubric: "rubric"},
		},
	}

	got, err := svc.GenerateOrFetch(context.Background(), 99)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gemini.calls != 1 {
		t.Fatalf("expected one Gemini call, got %d", gemini.calls)
	}
	if len(got) != 6 {
		t.Fatalf("expected 6 inserted rows, got %d", len(got))
	}
}

func TestPrepare_ReturnsReadyWhenCached(t *testing.T) {
	svc, gemini, _, _, _ := newServiceWithQuestions(42)
	status, err := svc.Prepare(42)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready {
		t.Errorf("expected Ready=true on cached lesson")
	}
	if gemini.calls != 0 {
		t.Errorf("expected zero Gemini calls when cached, got %d", gemini.calls)
	}
}

func TestPrepare_KicksOffGoroutineWhenMissing(t *testing.T) {
	svc, gemini, qRepo, _, _ := newServiceWithQuestions(99)
	qRepo.rows = nil
	gemini.gen = repository.GeneratedQuestionSet{
		MCQs: []repository.GeneratedMCQ{
			{Prompt: "Q1", Options: []string{"a", "b", "c", "d"}, CorrectIndex: 0},
			{Prompt: "Q2", Options: []string{"a", "b", "c", "d"}, CorrectIndex: 0},
			{Prompt: "Q3", Options: []string{"a", "b", "c", "d"}, CorrectIndex: 0},
		},
		WritingPrompts: []repository.GeneratedWriting{
			{Prompt: "W1", Rubric: "r"}, {Prompt: "W2", Rubric: "r"}, {Prompt: "W3", Rubric: "r"},
		},
	}

	status, err := svc.Prepare(99)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Generating {
		t.Errorf("expected Generating=true")
	}

	// Wait for the goroutine to land questions.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, _ := qRepo.CountByVideo(99)
		if c == 6 {
			return // pass
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("background generation never persisted questions")
}

func TestListForTaking_StripsAnswers(t *testing.T) {
	svc, _, _, _, _ := newServiceWithQuestions(42)
	out, err := svc.ListForTaking(42)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range out {
		if q.CorrectIndex != nil {
			t.Errorf("correct_index leaked for question %d", q.ID)
		}
		if q.Rubric != "" {
			t.Errorf("rubric leaked for question %d", q.ID)
		}
	}
}

func TestCreateAttempt_ReturnsEvaluatingImmediately(t *testing.T) {
	svc, _, _, zRepo, _ := newServiceWithQuestions(42)

	in := SubmitAttemptInput{
		UserID:   7,
		Takeaway: strings.Repeat("x", 40),
		Answers: []SubmittedAnswer{
			{QuestionID: 1, Answer: "C"},
			{QuestionID: 2, Answer: "C"},
			{QuestionID: 3, Answer: "C"},
			{QuestionID: 4, Answer: strings.Repeat("a", 50)},
			{QuestionID: 5, Answer: strings.Repeat("b", 50)},
			{QuestionID: 6, Answer: strings.Repeat("c", 50)},
		},
	}

	got, err := svc.CreateAttempt(42, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != model.AttemptStatusEvaluating {
		t.Errorf("expected status=evaluating immediately, got %q", got.Status)
	}
	if got.MCQScore != 100 {
		t.Errorf("expected MCQ score 100 (deterministic), got %v", got.MCQScore)
	}

	// Wait for the goroutine to flip status → completed.
	final := waitForStatus(t, zRepo, got.ID, model.AttemptStatusCompleted)
	if final.WritingScore < 70 {
		t.Errorf("expected writing score ~80 (fake returns 0.8), got %v", final.WritingScore)
	}
}

func TestCreateAttempt_RejectsShortTakeaway(t *testing.T) {
	svc, _, _, _, _ := newServiceWithQuestions(42)
	answers := []SubmittedAnswer{
		{QuestionID: 1, Answer: "C"}, {QuestionID: 2, Answer: "C"}, {QuestionID: 3, Answer: "C"},
		{QuestionID: 4, Answer: strings.Repeat("a", 50)},
		{QuestionID: 5, Answer: strings.Repeat("b", 50)},
		{QuestionID: 6, Answer: strings.Repeat("c", 50)},
	}
	in := SubmitAttemptInput{UserID: 7, Answers: answers, Takeaway: "too short"}

	_, err := svc.CreateAttempt(42, in)
	if err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("expected takeaway min-length error, got %v", err)
	}
}

func TestCreateAttempt_RejectsWrongAnswerCount(t *testing.T) {
	svc, _, _, _, _ := newServiceWithQuestions(42)
	in := SubmitAttemptInput{UserID: 7, Answers: []SubmittedAnswer{{QuestionID: 1, Answer: "C"}}, Takeaway: strings.Repeat("x", 40)}
	_, err := svc.CreateAttempt(42, in)
	if err == nil || !strings.Contains(err.Error(), "6 answers") {
		t.Fatalf("expected 6-answer error, got %v", err)
	}
}

func TestSequentialLock_BlocksCompletionUntilEarlierLessonsDone(t *testing.T) {
	svc, _, _, _, mRepo := newServiceWithQuestions(42)

	// Set up two earlier lessons in the same course — one complete, one not.
	mRepo.mats[40] = model.CourseMaterial{
		VideoID: 40, CourseID: "1", Title: "Earlier lesson 1", Completed: true,
	}
	mRepo.mats[41] = model.CourseMaterial{
		VideoID: 41, CourseID: "1", Title: "Earlier lesson 2 (not done)", Completed: false,
	}
	// Make the current target also share course "1".
	cur := mRepo.mats[42]
	cur.CourseID = "1"
	cur.Title = "Current lesson"
	mRepo.mats[42] = cur

	in := SubmitAttemptInput{
		UserID:   7,
		Takeaway: strings.Repeat("x", 40),
		Answers: []SubmittedAnswer{
			{QuestionID: 1, Answer: "C"}, {QuestionID: 2, Answer: "C"}, {QuestionID: 3, Answer: "C"},
			{QuestionID: 4, Answer: strings.Repeat("a", 50)},
			{QuestionID: 5, Answer: strings.Repeat("b", 50)},
			{QuestionID: 6, Answer: strings.Repeat("c", 50)},
		},
	}

	_, err := svc.CreateAttempt(42, in)
	if err == nil {
		t.Fatal("expected SequentialLockError, got nil")
	}
	var lockErr *SequentialLockError
	if !errors.As(err, &lockErr) {
		t.Fatalf("expected *SequentialLockError, got %T: %v", err, err)
	}
	if lockErr.BlockingVideoID != 41 {
		t.Errorf("expected blocking video 41, got %d", lockErr.BlockingVideoID)
	}
	if lockErr.BlockingTitle != "Earlier lesson 2 (not done)" {
		t.Errorf("expected blocking title 'Earlier lesson 2 (not done)', got %q", lockErr.BlockingTitle)
	}
}

func TestSequentialLock_AllowsWhenAllEarlierLessonsDone(t *testing.T) {
	svc, _, _, _, mRepo := newServiceWithQuestions(42)
	mRepo.mats[40] = model.CourseMaterial{VideoID: 40, CourseID: "1", Title: "L1", Completed: true}
	mRepo.mats[41] = model.CourseMaterial{VideoID: 41, CourseID: "1", Title: "L2", Completed: true}
	cur := mRepo.mats[42]
	cur.CourseID = "1"
	mRepo.mats[42] = cur

	in := SubmitAttemptInput{
		UserID:   7,
		Takeaway: strings.Repeat("x", 40),
		Answers: []SubmittedAnswer{
			{QuestionID: 1, Answer: "C"}, {QuestionID: 2, Answer: "C"}, {QuestionID: 3, Answer: "C"},
			{QuestionID: 4, Answer: strings.Repeat("a", 50)},
			{QuestionID: 5, Answer: strings.Repeat("b", 50)},
			{QuestionID: 6, Answer: strings.Repeat("c", 50)},
		},
	}
	if _, err := svc.CreateAttempt(42, in); err != nil {
		t.Fatalf("expected success when all earlier lessons done, got %v", err)
	}
}

func TestSequentialLock_AllowsFirstLesson(t *testing.T) {
	svc, _, _, _, mRepo := newServiceWithQuestions(42)
	cur := mRepo.mats[42]
	cur.CourseID = "1"
	mRepo.mats[42] = cur

	in := SubmitAttemptInput{
		UserID:   7,
		Takeaway: strings.Repeat("x", 40),
		Answers: []SubmittedAnswer{
			{QuestionID: 1, Answer: "C"}, {QuestionID: 2, Answer: "C"}, {QuestionID: 3, Answer: "C"},
			{QuestionID: 4, Answer: strings.Repeat("a", 50)},
			{QuestionID: 5, Answer: strings.Repeat("b", 50)},
			{QuestionID: 6, Answer: strings.Repeat("c", 50)},
		},
	}
	if _, err := svc.CreateAttempt(42, in); err != nil {
		t.Fatalf("first lesson should never be blocked, got %v", err)
	}
}

func TestAsyncGrading_TransitionsToFailedOnGeminiError(t *testing.T) {
	svc, gemini, _, zRepo, _ := newServiceWithQuestions(42)
	gemini.gradeErr = errors.New("gemini timeout")

	in := SubmitAttemptInput{
		UserID:   7,
		Takeaway: strings.Repeat("x", 40),
		Answers: []SubmittedAnswer{
			{QuestionID: 1, Answer: "C"},
			{QuestionID: 2, Answer: "C"},
			{QuestionID: 3, Answer: "C"},
			{QuestionID: 4, Answer: strings.Repeat("a", 50)},
			{QuestionID: 5, Answer: strings.Repeat("b", 50)},
			{QuestionID: 6, Answer: strings.Repeat("c", 50)},
		},
	}

	got, err := svc.CreateAttempt(42, in)
	if err != nil {
		t.Fatal(err)
	}
	final := waitForStatus(t, zRepo, got.ID, model.AttemptStatusFailed)
	if final.ErrorMessage == "" {
		t.Errorf("expected error_message to be set on failed attempt")
	}
}
