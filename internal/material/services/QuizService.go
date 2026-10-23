// Package services contains the business-logic layer for the Course Material Service.
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"focusflow-api/internal/material/model"
	"focusflow-api/internal/material/repository"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/datatypes"
)

// US-026 quiz constants.
const (
	writingMinChars = 20
	writingMaxChars = 2000

	// How long a single async grading job is allowed to run before we abort
	// the goroutine and mark the attempt failed. Long enough for 3 sequential
	// (or parallel) Gemini calls plus overhead.
	asyncGradingTimeout = 90 * time.Second

	// Same idea for generation. Generation is one Gemini call but on a
	// video, which is slower than a text call.
	asyncGenerationTimeout = 110 * time.Second
)

// SubmittedAnswer is a single line item posted by the frontend at the end
// of a quiz attempt. We do NOT trust the frontend's correctness flag.
type SubmittedAnswer struct {
	QuestionID int    `json:"question_id"`
	Answer     string `json:"answer"`
}

// SubmitAttemptInput captures everything the frontend sends in one POST.
type SubmitAttemptInput struct {
	UserID   int               `json:"-"` // set from the session
	Answers  []SubmittedAnswer `json:"answers"`
	Takeaway string            `json:"takeaway"`
}

// QuizService orchestrates question generation, grading, and attempt
// persistence. It depends on every other layer through its interfaces so
// each method is unit-testable with fakes.
//
// Two pieces of in-process state live here:
//   - `generating`  : a sync.Map[videoID]bool guarding against duplicate
//     concurrent Gemini generation calls for the same
//     lesson. It's process-local, which is fine because
//     the service is single-instance today; if we
//     horizontally scale, a DB-level uniqueness constraint
//     on lesson_questions(video_id) is the long-term
//     answer (and BulkInsert already enforces that).
type QuizService struct {
	gemini       repository.GeminiRepository
	questionRepo model.LessonQuestionRepository
	quizRepo     model.QuizRepository
	materialRepo model.CourseMaterialRepository
	takeaway     *TakeawayService

	generating sync.Map // map[int]bool — video_ids currently being generated
}

// NewQuizService wires the orchestrator.
func NewQuizService(
	gemini repository.GeminiRepository,
	questionRepo model.LessonQuestionRepository,
	quizRepo model.QuizRepository,
	materialRepo model.CourseMaterialRepository,
	takeaway *TakeawayService,
) *QuizService {
	return &QuizService{
		gemini:       gemini,
		questionRepo: questionRepo,
		quizRepo:     quizRepo,
		materialRepo: materialRepo,
		takeaway:     takeaway,
	}
}

// ---------------------------------------------------------------------------
// Prepare — fire-and-forget background generation.
// ---------------------------------------------------------------------------

// PrepareStatus is what the frontend polls.
type PrepareStatus struct {
	Ready      bool   `json:"ready"`      // questions exist in the DB
	Generating bool   `json:"generating"` // a goroutine is currently producing them
	Error      string `json:"error,omitempty"`
}

// Prepare kicks off question generation in the background if none exists
// for the lesson yet. Returns immediately. Calling it repeatedly while
// generation is already in flight is safe — the second caller observes
// `Generating: true` and waits.
//
// The frontend calls this when the learner selects a lesson in the player,
// so by the time they click "Mark as complete" the quiz is already in the
// DB and the wait is invisible.
func (s *QuizService) Prepare(videoID int) (PrepareStatus, error) {
	if videoID <= 0 {
		return PrepareStatus{}, fmt.Errorf("video_id must be positive")
	}

	count, err := s.questionRepo.CountByVideo(videoID)
	if err != nil {
		return PrepareStatus{}, err
	}
	if count > 0 {
		return PrepareStatus{Ready: true}, nil
	}

	// Already in flight from another caller?
	if _, inFlight := s.generating.LoadOrStore(videoID, true); inFlight {
		return PrepareStatus{Generating: true}, nil
	}

	// We claimed the in-flight slot. Spawn the goroutine.
	cm, err := s.materialRepo.GetCourseMaterialByID(videoID)
	if err != nil {
		s.generating.Delete(videoID)
		return PrepareStatus{}, fmt.Errorf("lesson not found: %w", err)
	}
	if cm.YouTubeID == "" {
		s.generating.Delete(videoID)
		return PrepareStatus{}, fmt.Errorf("lesson has no YouTube video to analyse")
	}

	go s.generateInBackground(videoID, cm.YouTubeID, cm.Title)

	return PrepareStatus{Generating: true}, nil
}

// Regenerate creates a NEW set of questions for a lesson covering the
// next ~25-minute window of the video. Use cases:
//
//  1. Long lessons (> 25 min) where the first quiz covered only the
//     opening segment. Each call shifts the window forward.
//  2. Short lessons where the learner wants varied practice — the new
//     set still covers the same content but with different questions.
//
// Returns the freshly-inserted question set (without answers, ready for
// the frontend to display).
func (s *QuizService) Regenerate(ctx context.Context, videoID int) ([]model.LessonQuestion, error) {
	if videoID <= 0 {
		return nil, fmt.Errorf("video_id must be positive")
	}

	currentLatest, err := s.questionRepo.LatestSetNumber(videoID)
	if err != nil {
		return nil, err
	}
	nextSet := currentLatest + 1

	cm, err := s.materialRepo.GetCourseMaterialByID(videoID)
	if err != nil {
		return nil, fmt.Errorf("lesson not found: %w", err)
	}
	if cm.YouTubeID == "" {
		return nil, fmt.Errorf("lesson has no YouTube video to analyse")
	}

	// Each set covers a 25-minute window stepping forward. Set 1 → 0–25,
	// Set 2 → 25–50, Set 3 → 50–75…
	startSec := (nextSet - 1) * 1500
	endSec := startSec + 1500

	videoURL := fmt.Sprintf("https://www.youtube.com/watch?v=%s", cm.YouTubeID)
	gen, err := s.gemini.GenerateQuestionsClipped(
		ctx,
		videoURL,
		cm.Title,
		time.Duration(startSec)*time.Second,
		time.Duration(endSec)*time.Second,
	)
	if err != nil {
		return nil, fmt.Errorf("regenerate failed: %w", err)
	}

	rows, err := mapGeneratedToRowsForSet(videoID, nextSet, startSec, endSec, gen)
	if err != nil {
		return nil, err
	}
	return s.questionRepo.BulkInsert(rows)
}

// Status reports whether questions exist or generation is in flight.
// Cheap — never calls Gemini.
func (s *QuizService) Status(videoID int) (PrepareStatus, error) {
	if videoID <= 0 {
		return PrepareStatus{}, fmt.Errorf("video_id must be positive")
	}
	count, err := s.questionRepo.CountByVideo(videoID)
	if err != nil {
		return PrepareStatus{}, err
	}
	if count > 0 {
		return PrepareStatus{Ready: true}, nil
	}
	if _, inFlight := s.generating.Load(videoID); inFlight {
		return PrepareStatus{Generating: true}, nil
	}
	return PrepareStatus{}, nil
}

// generateInBackground runs Gemini's question generation off the request
// goroutine. It always clears its sync.Map entry on return so the next
// caller of Prepare can retry on failure.
func (s *QuizService) generateInBackground(videoID int, youtubeID, lessonTitle string) {
	defer s.generating.Delete(videoID)

	ctx, cancel := context.WithTimeout(context.Background(), asyncGenerationTimeout)
	defer cancel()

	videoURL := fmt.Sprintf("https://www.youtube.com/watch?v=%s", youtubeID)
	gen, err := s.gemini.GenerateQuestions(ctx, videoURL, lessonTitle)
	if err != nil {
		slog.Error("background generation failed",
			"component", "quiz",
			"video_id", videoID,
			"error", err,
		)
		return
	}

	rows, err := mapGeneratedToRows(videoID, gen)
	if err != nil {
		slog.Error("background generation produced invalid output",
			"component", "quiz",
			"video_id", videoID,
			"error", err,
		)
		return
	}
	if _, err := s.questionRepo.BulkInsert(rows); err != nil {
		slog.Error("background generation insert failed",
			"component", "quiz",
			"video_id", videoID,
			"error", err,
		)
		return
	}
	slog.Info("background generation succeeded",
		"component", "quiz",
		"video_id", videoID,
		"rows", len(rows),
	)
}

// ---------------------------------------------------------------------------
// GenerateOrFetch — synchronous path (kept for callers who need it).
// ---------------------------------------------------------------------------

// GenerateOrFetch returns the six questions for a lesson, generating them
// via Gemini synchronously if they don't exist yet. This is the slow path
// — the frontend now usually hits Prepare and only falls back to this if
// the learner clicks Mark-as-complete faster than background generation
// finished.
func (s *QuizService) GenerateOrFetch(ctx context.Context, videoID int) ([]model.LessonQuestion, error) {
	if videoID <= 0 {
		return nil, fmt.Errorf("video_id must be positive")
	}

	existing, err := s.questionRepo.CountByVideo(videoID)
	if err != nil {
		return nil, err
	}
	if existing > 0 {
		return s.questionRepo.ListByVideo(videoID)
	}

	cm, err := s.materialRepo.GetCourseMaterialByID(videoID)
	if err != nil {
		return nil, fmt.Errorf("lesson not found: %w", err)
	}
	if cm.YouTubeID == "" {
		return nil, fmt.Errorf("lesson has no YouTube video to analyse")
	}

	videoURL := fmt.Sprintf("https://www.youtube.com/watch?v=%s", cm.YouTubeID)
	generated, err := s.gemini.GenerateQuestions(ctx, videoURL, cm.Title)
	if err != nil {
		return nil, fmt.Errorf("question generation failed: %w", err)
	}

	rows, err := mapGeneratedToRows(videoID, generated)
	if err != nil {
		return nil, err
	}
	return s.questionRepo.BulkInsert(rows)
}

// ListForTaking returns the six questions WITHOUT correct_index or rubric.
func (s *QuizService) ListForTaking(videoID int) ([]model.LessonQuestion, error) {
	qs, err := s.questionRepo.ListByVideo(videoID)
	if err != nil {
		return nil, err
	}
	out := make([]model.LessonQuestion, 0, len(qs))
	for _, q := range qs {
		safe := q
		safe.CorrectIndex = nil
		safe.Rubric = ""
		out = append(out, safe)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// CreateAttempt — synchronous setup + async grading.
// ---------------------------------------------------------------------------

// SequentialLockError is returned by CreateAttempt when the learner tries
// to complete a lesson while an earlier one is still incomplete.
type SequentialLockError struct {
	// BlockingTitle is the title of the earliest lesson the learner still
	// needs to complete before the requested one unlocks.
	BlockingTitle string
	// BlockingVideoID is the DB id of that lesson — frontend uses it to
	// link the learner straight to the player.
	BlockingVideoID int
}

func (e *SequentialLockError) Error() string {
	return fmt.Sprintf("complete the earlier lesson first: %q", e.BlockingTitle)
}

// CreateAttempt validates the submission, enforces sequential progression
// (v2: cannot complete lesson N until every lower-position lesson is done),
// scores the MCQs deterministically (no LLM), upserts the takeaway, persists
// a QuizAttempt with status `evaluating`, and kicks off a goroutine that
// grades the writing answers. Returns the freshly-created (partially-graded)
// attempt so the frontend can navigate to the results page immediately.
//
// The frontend polls GetAttempt until status flips to `completed`.
func (s *QuizService) CreateAttempt(videoID int, in SubmitAttemptInput) (model.QuizAttempt, error) {
	if videoID <= 0 || in.UserID <= 0 {
		return model.QuizAttempt{}, fmt.Errorf("video_id and user_id must be positive")
	}
	if len(in.Answers) != 6 {
		return model.QuizAttempt{}, fmt.Errorf("expected 6 answers, got %d", len(in.Answers))
	}

	// v2 sequential-progression guard. The learner is free to *watch* any
	// lesson, but completing lesson N is gated on every earlier lesson
	// being marked complete. We compare by video_id (which equals
	// insertion order under our schema; until US-014 lands and adds an
	// explicit `lesson_order` column, this is the truthful ordering).
	if err := s.enforceSequentialLock(videoID); err != nil {
		return model.QuizAttempt{}, err
	}

	questions, err := s.questionRepo.ListByVideo(videoID)
	if err != nil {
		return model.QuizAttempt{}, err
	}
	if len(questions) != 6 {
		return model.QuizAttempt{}, fmt.Errorf("quiz not generated for this lesson yet")
	}

	byID := make(map[int]model.LessonQuestion, len(questions))
	for _, q := range questions {
		byID[q.ID] = q
	}

	// MCQ grading is synchronous — it's deterministic and free.
	answers := make([]model.QuizAnswer, 0, 6)
	var mcqSum float64
	var mcqCount int
	for _, sub := range in.Answers {
		q, ok := byID[sub.QuestionID]
		if !ok {
			return model.QuizAttempt{}, fmt.Errorf("unknown question_id %d", sub.QuestionID)
		}
		switch q.QuestionType {
		case model.QuestionTypeMCQ:
			isCorrect, score := gradeMCQ(q, sub.Answer)
			answers = append(answers, model.QuizAnswer{
				QuestionID: q.ID,
				Answer:     sub.Answer,
				IsCorrect:  &isCorrect,
				Score:      score,
			})
			mcqSum += score
			mcqCount++
		case model.QuestionTypeWriting:
			// Placeholder — the goroutine will overwrite this row.
			answers = append(answers, model.QuizAnswer{
				QuestionID: q.ID,
				Answer:     sub.Answer,
				Score:      0,
				Feedback:   "Evaluating…",
			})
		}
	}
	if mcqCount != 3 {
		return model.QuizAttempt{}, fmt.Errorf("expected 3 MCQ answers, got %d", mcqCount)
	}

	// Upsert the US-001 takeaway synchronously — same transaction
	// semantics as before. The lesson is also flipped to completed here.
	_, takeaway, err := s.takeaway.SubmitTakeaway(in.UserID, videoID, in.Takeaway)
	if err != nil {
		return model.QuizAttempt{}, fmt.Errorf("takeaway submission failed: %w", err)
	}
	takeawayID := takeaway.ID

	mcqScore := (mcqSum / 3.0) * 100.0
	attempt := &model.QuizAttempt{
		VideoID:      videoID,
		UserID:       in.UserID,
		TakeawayID:   &takeawayID,
		Status:       model.AttemptStatusEvaluating,
		MCQScore:     round2(mcqScore),
		WritingScore: 0,
		TotalScore:   0,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
		Answers:      answers,
	}
	saved, err := s.quizRepo.CreateAttempt(attempt)
	if err != nil {
		return model.QuizAttempt{}, err
	}

	// Spawn the async grading goroutine. We pass the question set and the
	// submitted answers explicitly so the goroutine doesn't have to re-read
	// from the DB.
	go s.gradeWritingAsync(saved.ID, mcqSum, byID, in.Answers)

	return saved, nil
}

// gradeWritingAsync writes the final grading result back to the attempt
// row. The three writing-answer items are graded in a single Gemini batch
// call (one network round-trip, one quota slot — 3x cheaper than per-item).
//
// MCQ items are re-graded here too (cheap and deterministic) to produce a
// consistent answer set without round-tripping the DB.
//
// On Gemini failure the row transitions to status=failed with the error
// message; the frontend stops polling and surfaces the message.
func (s *QuizService) gradeWritingAsync(
	attemptID int,
	mcqSum float64,
	questionsByID map[int]model.LessonQuestion,
	submitted []SubmittedAnswer,
) {
	ctx, cancel := context.WithTimeout(context.Background(), asyncGradingTimeout)
	defer cancel()

	// Build the batch input: one WritingGradeItem per writing question,
	// in submission order. Items that fail the min-length guard get
	// auto-graded 0 here and are NOT sent to Gemini — saves quota.
	type pending struct {
		questionID int
		answer     string
		autoZero   bool
		autoMsg    string
	}
	pendings := make([]pending, 0, 3)
	batchItems := make([]repository.WritingGradeItem, 0, 3)

	for _, sub := range submitted {
		q, ok := questionsByID[sub.QuestionID]
		if !ok || q.QuestionType != model.QuestionTypeWriting {
			continue
		}
		trimmed := strings.TrimSpace(sub.Answer)
		if len(trimmed) < writingMinChars {
			pendings = append(pendings, pending{
				questionID: q.ID,
				answer:     sub.Answer,
				autoZero:   true,
				autoMsg:    fmt.Sprintf("Answer too short (needs at least %d characters of substance).", writingMinChars),
			})
			continue
		}
		if len(trimmed) > writingMaxChars {
			trimmed = trimmed[:writingMaxChars]
		}
		idx := len(batchItems)
		pendings = append(pendings, pending{questionID: q.ID, answer: sub.Answer})
		batchItems = append(batchItems, repository.WritingGradeItem{
			Index:  idx,
			Prompt: q.Prompt,
			Rubric: q.Rubric,
			Answer: trimmed,
		})
	}

	// One round-trip for whatever writing items survived the short-circuit.
	var gradeErr error
	var grades []repository.Grade
	if len(batchItems) > 0 {
		grades, gradeErr = s.gemini.GradeWritingBatch(ctx, batchItems)
	}

	// Map Gemini's output back to pending items by aligning indices.
	gradeIdx := 0
	gradedByQID := make(map[int]repository.Grade, len(pendings))
	for _, p := range pendings {
		if p.autoZero {
			gradedByQID[p.questionID] = repository.Grade{Score: 0, Feedback: p.autoMsg}
			continue
		}
		if gradeErr == nil && gradeIdx < len(grades) {
			gradedByQID[p.questionID] = grades[gradeIdx]
		} else {
			// gradeErr != nil — propagate a generic 0 for this slot;
			// the attempt will be marked failed below.
			gradedByQID[p.questionID] = repository.Grade{Score: 0, Feedback: ""}
		}
		gradeIdx++
	}

	// Reconstruct the full answer set in the original submission order.
	finalAnswers := make([]model.QuizAnswer, 0, 6)
	var writingSum float64
	for _, sub := range submitted {
		q, ok := questionsByID[sub.QuestionID]
		if !ok {
			continue
		}
		switch q.QuestionType {
		case model.QuestionTypeMCQ:
			isCorrect, score := gradeMCQ(q, sub.Answer)
			finalAnswers = append(finalAnswers, model.QuizAnswer{
				QuestionID: q.ID,
				Answer:     sub.Answer,
				IsCorrect:  &isCorrect,
				Score:      score,
			})
		case model.QuestionTypeWriting:
			g := gradedByQID[q.ID]
			finalAnswers = append(finalAnswers, model.QuizAnswer{
				QuestionID: q.ID,
				Answer:     sub.Answer,
				Score:      g.Score,
				Feedback:   g.Feedback,
			})
			writingSum += g.Score
		}
	}

	writingScore := (writingSum / 3.0) * 100.0
	totalScore := (mcqSum/3.0)*0.5*100.0 + (writingSum/3.0)*0.5*100.0
	// Total = average of mcq and writing, both on 0..100. Equivalent to the
	// per-question average across all six (since the two halves are equally
	// weighted) but easier to reason about.

	attempt := &model.QuizAttempt{
		ID:           attemptID,
		WritingScore: round2(writingScore),
		TotalScore:   round2(totalScore),
		UpdatedAt:    time.Now().UTC(),
		Answers:      finalAnswers,
	}

	if gradeErr != nil {
		attempt.Status = model.AttemptStatusFailed
		attempt.ErrorMessage = gradeErr.Error()
		slog.Error("async grading failed",
			"component", "quiz",
			"attempt_id", attemptID,
			"error", gradeErr,
		)
	} else {
		attempt.Status = model.AttemptStatusCompleted
	}

	// We need to preserve mcq_score — pull it from the existing row rather
	// than computing again, since we only handed `mcqSum` in.
	if existing, err := s.quizRepo.GetAttemptByID(attemptID); err == nil {
		attempt.MCQScore = existing.MCQScore
		attempt.TakeawayID = existing.TakeawayID
	}

	if _, err := s.quizRepo.UpdateAttempt(attempt); err != nil {
		slog.Error("async grading update attempt failed",
			"component", "quiz",
			"attempt_id", attemptID,
			"error", err,
		)
	}
}

// enforceSequentialLock returns a SequentialLockError when the learner is
// trying to complete a lesson while an earlier-ordered one in the same
// course is still incomplete. The earliest such blocking lesson is named
// so the frontend can deep-link the learner straight to it.
//
// Ordering: we sort by video_id ascending, which is the insertion order
// under our schema. Until US-014 introduces an explicit `lesson_order`
// column, this is the truthful "lesson 1, 2, 3…" ordering.
func (s *QuizService) enforceSequentialLock(videoID int) error {
	current, err := s.materialRepo.GetCourseMaterialByID(videoID)
	if err != nil {
		return fmt.Errorf("lesson not found: %w", err)
	}

	siblings, err := s.materialRepo.GetCourseMaterialsByCourseID(current.CourseID)
	if err != nil {
		return fmt.Errorf("list lessons for course: %w", err)
	}
	if len(siblings) == 0 {
		return nil // first lesson in the course — nothing to block on
	}

	sort.Slice(siblings, func(i, j int) bool {
		return siblings[i].VideoID < siblings[j].VideoID
	})

	// Walk lessons in order. The first one that is BOTH lower-id than the
	// current and incomplete is the blocker.
	for _, sib := range siblings {
		if sib.VideoID >= videoID {
			break
		}
		if !sib.Completed {
			return &SequentialLockError{
				BlockingTitle:   sib.Title,
				BlockingVideoID: sib.VideoID,
			}
		}
	}
	return nil
}

// GetAttempt returns one attempt for the results-page poll.
//
// Enriches each answer line item with the question prompt, type, options,
// and (for MCQ) the correct option text. The frontend uses these to show
// the correct answer alongside the learner's submission. Safe to expose
// because the caller has already submitted the attempt — there's no way
// to use the correct-answer payload to cheat on the same attempt.
func (s *QuizService) GetAttempt(attemptID int) (model.QuizAttempt, error) {
	if attemptID <= 0 {
		return model.QuizAttempt{}, fmt.Errorf("attempt_id must be positive")
	}
	attempt, err := s.quizRepo.GetAttemptByID(attemptID)
	if err != nil {
		return model.QuizAttempt{}, err
	}
	s.enrichAttempt(&attempt)
	return attempt, nil
}

// ListAttemptsForUser returns the learner's attempt history for the
// /quizzes page. Each attempt is enriched with its lesson title and
// course id so the list renders meaningfully without a second fetch.
// Answer line items are NOT included — the list view doesn't need them.
func (s *QuizService) ListAttemptsForUser(userID, limit, offset int) ([]model.QuizAttempt, error) {
	if userID <= 0 {
		return nil, fmt.Errorf("user_id must be positive")
	}
	attempts, err := s.quizRepo.ListByUser(userID, limit, offset)
	if err != nil {
		return nil, err
	}
	for i := range attempts {
		s.enrichLessonTitle(&attempts[i])
	}
	return attempts, nil
}

// LatestForUser returns the learner's most recent attempt for a lesson.
func (s *QuizService) LatestForUser(userID, videoID int) (model.QuizAttempt, error) {
	if userID <= 0 || videoID <= 0 {
		return model.QuizAttempt{}, fmt.Errorf("user_id and video_id must be positive")
	}
	attempt, err := s.quizRepo.LatestByUserVideo(userID, videoID)
	if err != nil {
		return model.QuizAttempt{}, err
	}
	s.enrichAttempt(&attempt)
	return attempt, nil
}

// enrichAttempt loads the lesson title and the question metadata for each
// answer line item. Best-effort — failures don't abort the response, they
// just leave the enrichment fields empty.
func (s *QuizService) enrichAttempt(attempt *model.QuizAttempt) {
	s.enrichLessonTitle(attempt)
	if len(attempt.Answers) == 0 {
		return
	}

	// Pull the matching question set (by set_number recorded on the
	// attempt). Falling back to the latest set if SetNumber is zero (old
	// rows from before the column existed).
	setNumber := attempt.SetNumber
	if setNumber == 0 {
		setNumber = 1
	}
	questions, err := s.questionRepo.ListByVideoSet(attempt.VideoID, setNumber)
	if err != nil || len(questions) == 0 {
		return
	}

	byID := make(map[int]model.LessonQuestion, len(questions))
	for _, q := range questions {
		byID[q.ID] = q
	}
	for i := range attempt.Answers {
		ans := &attempt.Answers[i]
		q, ok := byID[ans.QuestionID]
		if !ok {
			continue
		}
		ans.QuestionPrompt = q.Prompt
		ans.QuestionType = string(q.QuestionType)
		if q.QuestionType == model.QuestionTypeMCQ {
			opts := decodeOptions(q.Options)
			ans.QuestionOptions = opts
			if q.CorrectIndex != nil && *q.CorrectIndex >= 0 && *q.CorrectIndex < len(opts) {
				ans.CorrectOption = opts[*q.CorrectIndex]
			}
		}
	}
}

// enrichLessonTitle fills in the lesson title + course id on an attempt
// row. Cheap — one DB row.
func (s *QuizService) enrichLessonTitle(attempt *model.QuizAttempt) {
	cm, err := s.materialRepo.GetCourseMaterialByID(attempt.VideoID)
	if err != nil {
		return
	}
	attempt.LessonTitle = cm.Title
	attempt.CourseID = cm.CourseID
}

// ---------------------------------------------------------------------------
// Grading helpers (unchanged from the synchronous version)
// ---------------------------------------------------------------------------

func gradeMCQ(q model.LessonQuestion, answer string) (bool, float64) {
	if q.CorrectIndex == nil {
		return false, 0
	}
	target := *q.CorrectIndex
	options := decodeOptions(q.Options)
	if target < 0 || target >= len(options) {
		return false, 0
	}

	cleaned := strings.TrimSpace(answer)
	if cleaned == "" {
		return false, 0
	}

	if cleaned == fmt.Sprintf("%d", target) {
		return true, 1
	}
	if len(cleaned) == 1 {
		idx := int(strings.ToUpper(cleaned)[0]) - int('A')
		if idx == target {
			return true, 1
		}
	}
	if strings.EqualFold(cleaned, options[target]) {
		return true, 1
	}
	return false, 0
}

func decodeOptions(raw datatypes.JSON) []string {
	if len(raw) == 0 {
		return nil
	}
	var asStrings []string
	if err := json.Unmarshal(raw, &asStrings); err == nil {
		return asStrings
	}
	var asObjects []struct {
		Label string `json:"label"`
		Text  string `json:"text"`
	}
	if err := json.Unmarshal(raw, &asObjects); err == nil {
		out := make([]string, 0, len(asObjects))
		for _, o := range asObjects {
			out = append(out, o.Text)
		}
		return out
	}
	return nil
}

func mapGeneratedToRows(videoID int, gen repository.GeneratedQuestionSet) ([]model.LessonQuestion, error) {
	return mapGeneratedToRowsForSet(videoID, 1, 0, 1500, gen)
}

// mapGeneratedToRowsForSet is the generalised builder that records the
// (set_number, start, end) trio on every row so regeneration tracks which
// video segment each set covers.
func mapGeneratedToRowsForSet(
	videoID, setNumber, startSec, endSec int,
	gen repository.GeneratedQuestionSet,
) ([]model.LessonQuestion, error) {
	rows := make([]model.LessonQuestion, 0, 6)
	pos := 1
	for _, m := range gen.MCQs {
		optsJSON, err := json.Marshal(m.Options)
		if err != nil {
			return nil, fmt.Errorf("encode mcq options: %w", err)
		}
		ci := m.CorrectIndex
		rows = append(rows, model.LessonQuestion{
			VideoID:           videoID,
			SetNumber:         setNumber,
			VideoStartSeconds: startSec,
			VideoEndSeconds:   endSec,
			Position:          pos,
			QuestionType:      model.QuestionTypeMCQ,
			Prompt:            m.Prompt,
			Options:           optsJSON,
			CorrectIndex:      &ci,
		})
		pos++
	}
	for _, w := range gen.WritingPrompts {
		rows = append(rows, model.LessonQuestion{
			VideoID:           videoID,
			SetNumber:         setNumber,
			VideoStartSeconds: startSec,
			VideoEndSeconds:   endSec,
			Position:          pos,
			QuestionType:      model.QuestionTypeWriting,
			Prompt:            w.Prompt,
			Rubric:            w.Rubric,
		})
		pos++
	}
	return rows, nil
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
