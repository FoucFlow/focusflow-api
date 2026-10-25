package learn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	coursemodels "focusflow-api/internal/course/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// submitGrace accepts answers that arrive a little after the deadline
// (slow networks); later ones are ignored and the saved answers count.
const submitGrace = 30 * time.Second

// maxEvents caps the integrity evidence stored per attempt.
const maxEvents = 300

// SetExaminer turns on question writing and AI marking.
func (s *Service) SetExaminer(e Examiner) { s.exam = e }

// testSettings returns a unit's saved settings, or the defaults.
func (s *Service) testSettings(unitID, courseID int) TestSettings {
	var st TestSettings
	if err := s.db.Where("unit_id = ?", unitID).First(&st).Error; err != nil {
		return defaultTestSettings(unitID, courseID)
	}
	return st
}

func (s *Service) questions(unitID int) []Question {
	qs := []Question{}
	s.db.Where("unit_id = ?", unitID).Order("id").Find(&qs)
	return qs
}

func (s *Service) questionCount(unitID int) int {
	var n int64
	s.db.Model(&Question{}).Where("unit_id = ?", unitID).Count(&n)
	return int(n)
}

// activeTests is the set of units (among ids) whose test is on and has questions:
// those units finish only by passing the test.
func (s *Service) activeTests(unitIDs []int) map[int]bool {
	out := map[int]bool{}
	if len(unitIDs) == 0 {
		return out
	}
	var ids []int
	s.db.Model(&TestSettings{}).Where("unit_id IN ? AND enabled", unitIDs).
		Where("EXISTS (SELECT 1 FROM unit_questions q WHERE q.unit_id = unit_tests.unit_id)").Pluck("unit_id", &ids)
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func (s *Service) saveSettings(st *TestSettings) error {
	return s.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(st).Error
}

// deleteTests removes the tests of these units (pool, settings, attempts, logs).
func deleteTests(tx *gorm.DB, unitIDs []int) error {
	if len(unitIDs) == 0 {
		return nil
	}
	if err := tx.Where("attempt_id IN (?)", tx.Model(&Attempt{}).Select("id").Where("unit_id IN ?", unitIDs)).Delete(&MarkChange{}).Error; err != nil {
		return err
	}
	for _, m := range []any{&Attempt{}, &Question{}, &TestSettings{}} {
		if err := tx.Where("unit_id IN ?", unitIDs).Delete(m).Error; err != nil {
			return err
		}
	}
	return nil
}

// ── question pool (F2.1) ─────────────────────────────────────────────────

// StartQuestions queues the AI to write a unit's question pool. The new
// questions replace the AI ones not yet edited by the creator.
func (s *Service) StartQuestions(u *Unit, c *coursemodels.Course) error {
	if s.exam == nil {
		return errors.New("Writing test questions with AI is not switched on yet on this server.")
	}
	if err := s.checkAI(c.UserId); err != nil {
		return errors.New(limitMessage(err))
	}
	var n int64
	s.db.Model(&Concept{}).Where("unit_id = ?", u.ID).Count(&n)
	if n == 0 && strings.TrimSpace(u.Body) == "" {
		return errors.New("This unit has no lessons yet to write questions from.")
	}
	st := s.testSettings(u.ID, u.CourseID)
	if st.PoolStatus == StatusProcessing {
		return errors.New("Questions are already being written for this unit.")
	}
	if lim := s.limits.QuestionRunsPerUnit; lim > 0 && st.PoolRuns >= lim {
		return fmt.Errorf("The free plan allows %d AI question runs per unit. You can still add and edit questions by hand.", lim)
	}
	st.PoolStatus, st.PoolError = StatusProcessing, ""
	st.PoolRuns++
	if err := s.saveSettings(&st); err != nil {
		return err
	}
	unitID, owner := u.ID, c.UserId
	s.enqueue(func() { s.writeQuestions(unitID, owner) })
	return nil
}

func (s *Service) writeQuestions(unitID, ownerID int) {
	var u Unit
	if err := s.db.First(&u, unitID).Error; err != nil {
		return
	}
	c, err := s.course(u.CourseID)
	if err != nil {
		return
	}
	st := s.testSettings(u.ID, u.CourseID)
	in := QuestionsInput{CourseTitle: c.CourseTitle, Language: c.CourseLanguage, UnitTitle: u.Title}
	var concepts []Concept
	s.db.Where("unit_id = ?", u.ID).Order("position, id").Find(&concepts)
	for _, con := range concepts {
		in.Concepts = append(in.Concepts, ExamConcept{Title: con.Title, Lesson: clip(con.Explanation, 3000)})
	}
	if len(in.Concepts) == 0 {
		in.Concepts = []ExamConcept{{Title: u.Title, Lesson: clip(u.Body, 8000)}}
	}
	// A pool well above one test's size, so learners get different draws
	in.Count = clampInt(3*len(in.Concepts), 2*st.QuestionsPerTest, 40)
	if in.Count < 8 {
		in.Count = 8
	}

	fail := func(msg string) {
		s.db.Model(&TestSettings{}).Where("unit_id = ?", unitID).Updates(map[string]any{
			"pool_status": StatusFailed, "pool_error": msg, "pool_runs": gorm.Expr("GREATEST(pool_runs - 1, 0)")})
	}
	if err := s.checkAI(ownerID); err != nil {
		fail(limitMessage(err))
		return
	}
	started := time.Now()
	qs, usage, err := s.exam.WriteQuestions(context.Background(), in)
	s.logUsage("questions", u.CourseID, ownerID, usage, started, err)
	if err != nil {
		slog.Warn("write questions failed", "unit", unitID, "error", err)
		msg := "The AI could not write questions for this unit. Try again in a minute; this run was not counted."
		if errors.Is(err, ErrAICredit) {
			msg = creditMessage
		}
		fail(msg)
		return
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("unit_id = ? AND source = ?", unitID, "ai").Delete(&Question{}).Error; err != nil {
			return err
		}
		for i := range qs {
			qs[i].ID, qs[i].UnitID, qs[i].CourseID = 0, unitID, u.CourseID
			if qs[i].Options == nil {
				qs[i].Options = []string{}
			}
			if err := tx.Create(&qs[i]).Error; err != nil {
				return err
			}
		}
		return tx.Model(&TestSettings{}).Where("unit_id = ?", unitID).
			Updates(map[string]any{"pool_status": StatusReady, "pool_error": ""}).Error
	})
	if err != nil {
		slog.Error("save questions", "unit", unitID, "error", err)
		fail("Could not save the questions. Please try again.")
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		v = lo
	}
	if v > hi {
		v = hi
	}
	return v
}

// ── attempts (F2.2) ──────────────────────────────────────────────────────

// latestAttempts lists a learner's attempts at a unit test, newest first.
func (s *Service) attempts(userID, unitID int) []Attempt {
	out := []Attempt{}
	s.db.Where("user_id = ? AND unit_id = ?", userID, unitID).Order("id DESC").Find(&out)
	return out
}

// canStart says whether the learner may start a new attempt, and why not.
func (s *Service) canStart(userID int, st TestSettings, attempts []Attempt) (bool, string) {
	for _, a := range attempts {
		switch {
		case a.Passed:
			return false, "You passed this test."
		case a.Status == AttemptMarking:
			return false, "Your last attempt is being marked."
		case a.Status == AttemptReview:
			return false, "Your last attempt is waiting for your teacher's review."
		}
	}
	if st.MaxAttempts > 0 && len(attempts) >= st.MaxAttempts {
		return false, fmt.Sprintf("You have used all %d attempts. Ask the course creator if you need another.", st.MaxAttempts)
	}
	if lim := s.limits.TestsPerDay; lim > 0 {
		var n int64
		s.db.Model(&Attempt{}).Where("user_id = ? AND started_at >= ?", userID, time.Now().Add(-24*time.Hour)).Count(&n)
		if int(n) >= lim {
			return false, fmt.Sprintf("You can start %d tests a day. Please come back tomorrow.", lim)
		}
	}
	return true, ""
}

// StartAttempt resumes the learner's open attempt, or draws a new one.
func (s *Service) StartAttempt(userID int, u *Unit) (*Attempt, error) {
	if a := s.openAttempt(userID, u.ID); a != nil {
		return a, nil
	}
	st := s.testSettings(u.ID, u.CourseID)
	pool := s.questions(u.ID)
	if !st.Enabled || len(pool) == 0 {
		return nil, errors.New("This unit has no test.")
	}
	var a *Attempt
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// One start at a time per learner and unit
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", fmt.Sprintf("test-start:%d:%d", userID, u.ID)).Error; err != nil {
			return err
		}
		var prior []Attempt
		tx.Where("user_id = ? AND unit_id = ?", userID, u.ID).Order("id DESC").Find(&prior)
		for i := range prior {
			if prior[i].Status == AttemptInProgress {
				a = &prior[i]
				return nil
			}
		}
		if ok, why := s.canStart(userID, st, prior); !ok {
			return errors.New(why)
		}
		now := time.Now()
		a = &Attempt{UnitID: u.ID, CourseID: u.CourseID, UserID: userID, Number: len(prior) + 1,
			Status: AttemptInProgress, Items: drawItems(pool, st.QuestionsPerTest), Events: []TestEvent{},
			Settings: st, StartedAt: now}
		if st.TimeLimitMinutes > 0 {
			d := now.Add(time.Duration(st.TimeLimitMinutes) * time.Minute)
			a.Deadline = &d
		}
		return tx.Create(a).Error
	})
	return a, err
}

// openAttempt returns the learner's in-progress attempt, submitting it first
// if its time is up (then nil).
func (s *Service) openAttempt(userID, unitID int) *Attempt {
	var a Attempt
	if err := s.db.Where("user_id = ? AND unit_id = ? AND status = ?", userID, unitID, AttemptInProgress).First(&a).Error; err != nil {
		return nil
	}
	if expired(&a, 0) {
		s.Submit(&a, nil, true)
		return nil
	}
	return &a
}

func expired(a *Attempt, grace time.Duration) bool {
	return a.Deadline != nil && time.Now().After(a.Deadline.Add(grace))
}

// drawItems picks n random questions and shuffles each one's options.
func drawItems(pool []Question, n int) []AttemptItem {
	idx := rand.Perm(len(pool))
	if n > len(idx) || n <= 0 {
		n = len(idx)
	}
	items := make([]AttemptItem, 0, n)
	for _, i := range idx[:n] {
		q := pool[i]
		it := AttemptItem{QuestionID: q.ID, Kind: q.Kind, Prompt: q.Prompt, Guide: q.AnswerGuide, Concept: q.Concept}
		if q.Kind == QuestionMCQ {
			order := rand.Perm(len(q.Options))
			for pos, orig := range order {
				it.Options = append(it.Options, q.Options[orig])
				if orig == q.Correct {
					it.Correct = pos
				}
			}
		}
		items = append(items, it)
	}
	return items
}

// AnswerIn is one answer sent by the learner, by item index.
type AnswerIn struct {
	Index  int    `json:"index"`
	Choice *int   `json:"choice"`
	Answer string `json:"answer"`
}

func applyAnswers(items []AttemptItem, answers []AnswerIn) {
	for _, in := range answers {
		if in.Index < 0 || in.Index >= len(items) {
			continue
		}
		it := &items[in.Index]
		if it.Kind == QuestionMCQ {
			if in.Choice != nil && *in.Choice >= 0 && *in.Choice < len(it.Options) {
				v := *in.Choice
				it.Choice = &v
			} else if in.Choice == nil {
				it.Choice = nil
			}
		} else {
			it.Answer = clip(in.Answer, 5000)
		}
	}
}

// SaveAnswers stores answers while the test is running (autosave).
func (s *Service) SaveAnswers(a *Attempt, answers []AnswerIn) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var cur Attempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&cur, a.ID).Error; err != nil {
			return err
		}
		if cur.Status != AttemptInProgress || expired(&cur, submitGrace) {
			return errors.New("This test is no longer open.")
		}
		applyAnswers(cur.Items, answers)
		*a = cur
		return tx.Model(&cur).Select("items").Updates(&cur).Error
	})
}

var eventKinds = map[string]bool{"tab_hidden": true, "fullscreen_exit": true, "paste": true, "copy": true}

// LogEvents records browser signals for an open attempt.
func (s *Service) LogEvents(a *Attempt, kinds []string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var cur Attempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&cur, a.ID).Error; err != nil {
			return err
		}
		if cur.Status != AttemptInProgress {
			return errors.New("This test is no longer open.")
		}
		now := time.Now()
		for _, k := range kinds {
			if eventKinds[k] && len(cur.Events) < maxEvents {
				cur.Events = append(cur.Events, TestEvent{Kind: k, At: now})
			}
		}
		return tx.Model(&cur).Select("events").Updates(&cur).Error
	})
}

// Submit closes an attempt: multiple choice is marked here, written answers
// go to the AI. auto = the time ran out.
func (s *Service) Submit(a *Attempt, answers []AnswerIn, auto bool) error {
	var toMark bool
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var cur Attempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&cur, a.ID).Error; err != nil {
			return err
		}
		if cur.Status != AttemptInProgress {
			return errors.New("This test was already submitted.")
		}
		if expired(&cur, submitGrace) {
			auto = true // too late: only the saved answers count
		} else {
			applyAnswers(cur.Items, answers)
		}
		now := time.Now()
		cur.SubmittedAt, cur.AutoSubmitted = &now, auto
		if auto && cur.Deadline != nil {
			cur.Events = append(cur.Events, TestEvent{Kind: "timeout", At: now})
		}
		for i := range cur.Items {
			it := &cur.Items[i]
			switch {
			case it.Kind == QuestionMCQ:
				v := 0.0
				if it.Choice != nil && *it.Choice == it.Correct {
					v = 1
				}
				it.AIMark, it.Mark = &v, &v
			case strings.TrimSpace(it.Answer) == "":
				v := 0.0
				it.AIMark, it.Mark, it.AIFeedback = &v, &v, "No answer was given."
			default:
				toMark = true
			}
		}
		cur.Status = AttemptMarking
		if !toMark {
			finalise(&cur)
		}
		*a = cur
		return tx.Model(&cur).Select("status", "items", "events", "submitted_at", "auto_submitted", "score_percent", "passed").Updates(&cur).Error
	})
	if err != nil {
		return err
	}
	if toMark {
		id := a.ID
		s.enqueue(func() { s.markAttempt(id) })
	} else if a.Passed {
		s.completeUnit(a.UserID, a.UnitID, a.CourseID)
	}
	return nil
}

// finalise computes the score and moves the attempt on: to the teacher when
// review is on, otherwise straight to its result.
func finalise(a *Attempt) {
	a.ScorePercent = score(a.Items)
	if a.Settings.TeacherReview {
		a.Status, a.Passed = AttemptReview, false
		return
	}
	a.Status = AttemptDone
	a.Passed = a.ScorePercent >= float64(a.Settings.PassMark)
}

// score is the percentage of marks, rounded to one decimal.
func score(items []AttemptItem) float64 {
	if len(items) == 0 {
		return 0
	}
	var sum float64
	for _, it := range items {
		if it.Mark != nil {
			sum += *it.Mark
		}
	}
	return math.Round(sum/float64(len(items))*1000) / 10
}

// markAttempt sends the written answers to the AI. If the AI fails twice the
// attempt goes to the teacher, who marks it by hand.
func (s *Service) markAttempt(id int) {
	var a Attempt
	if err := s.db.First(&a, id).Error; err != nil || a.Status != AttemptMarking {
		return
	}
	var u Unit
	s.db.First(&u, a.UnitID)
	c, _ := s.course(a.CourseID)
	in := MarkInput{UnitTitle: u.Title}
	if c != nil {
		in.CourseTitle, in.Language = c.CourseTitle, c.CourseLanguage
	}
	for i, it := range a.Items {
		if it.Kind != QuestionMCQ && it.Mark == nil {
			in.Items = append(in.Items, MarkItem{Index: i + 1, Kind: it.Kind, Question: it.Prompt, Guide: it.Guide, Answer: it.Answer})
		}
	}
	var marks map[int]AIMark
	var err error
	if s.exam == nil {
		err = ErrAIUnavailable
	}
	for attempt := 0; attempt < 2 && s.exam != nil; attempt++ {
		if err = s.checkBudget(); err != nil {
			break
		}
		started := time.Now()
		var usage Usage
		marks, usage, err = s.exam.MarkAnswers(context.Background(), in)
		s.logUsage("marking", a.CourseID, 0, usage, started, err) // no learner ids in the usage log
		if err == nil {
			break
		}
		slog.Warn("marking failed", "attempt", id, "try", attempt+1, "error", err)
		time.Sleep(5 * time.Second)
	}
	if err != nil {
		// The teacher marks it: never leave a learner stuck
		a.Status, a.MarkError = AttemptReview, "The AI could not mark the written answers, so your teacher will mark them."
		s.db.Model(&a).Select("status", "mark_error").Updates(&a)
		return
	}
	for idx, m := range marks {
		it := &a.Items[idx-1]
		v := m.Mark
		it.AIMark, it.Mark, it.AIFeedback = &v, &v, m.Feedback
	}
	finalise(&a)
	s.db.Model(&a).Select("status", "items", "score_percent", "passed").Updates(&a)
	if a.Passed {
		s.completeUnit(a.UserID, a.UnitID, a.CourseID)
	}
}

// ReviewMark is the teacher's mark for one item (F2.4).
type ReviewMark struct {
	Index int     `json:"index"`
	Mark  float64 `json:"mark"`
	Note  string  `json:"note"`
}

// Review applies the teacher's marks, logs every change, and gives the
// learner the result. Items not sent keep the AI's mark.
func (s *Service) Review(a *Attempt, reviewerID int, marks []ReviewMark) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var cur Attempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&cur, a.ID).Error; err != nil {
			return err
		}
		if cur.Status != AttemptReview {
			return errors.New("This attempt is not waiting for review.")
		}
		for _, m := range marks {
			if m.Index < 0 || m.Index >= len(cur.Items) {
				return errors.New("Unknown question in the review.")
			}
			if m.Mark != 0 && m.Mark != 0.5 && m.Mark != 1 {
				return errors.New("A mark must be 0, 0.5 or 1.")
			}
			it := &cur.Items[m.Index]
			note := clip(strings.TrimSpace(m.Note), 1000)
			if it.Mark != nil && *it.Mark == m.Mark && note == it.Note {
				continue
			}
			if err := tx.Create(&MarkChange{AttemptID: cur.ID, ItemIndex: m.Index, QuestionID: it.QuestionID,
				OldMark: it.Mark, NewMark: m.Mark, Note: note, ChangedBy: reviewerID}).Error; err != nil {
				return err
			}
			v := m.Mark
			it.Mark, it.Note = &v, note
		}
		for i, it := range cur.Items {
			if it.Mark == nil {
				return fmt.Errorf("Give a mark for question %d.", i+1)
			}
		}
		now := time.Now()
		cur.ScorePercent = score(cur.Items)
		cur.Status, cur.Passed = AttemptDone, cur.ScorePercent >= float64(cur.Settings.PassMark)
		cur.ReviewedBy, cur.ReviewedAt = &reviewerID, &now
		*a = cur
		return tx.Model(&cur).Select("status", "items", "score_percent", "passed", "reviewed_by", "reviewed_at").Updates(&cur).Error
	})
	if err == nil && a.Passed {
		s.completeUnit(a.UserID, a.UnitID, a.CourseID)
	}
	return err
}

func (s *Service) completeUnit(userID, unitID, courseID int) {
	s.db.Where(UnitProgress{UserID: userID, UnitID: unitID}).
		Attrs(UnitProgress{CourseID: courseID, CompletedAt: time.Now()}).FirstOrCreate(&UnitProgress{})
}

// sweepLoop auto-submits attempts whose time ran out (the learner closed the page).
func (s *Service) sweepLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		s.sweepExpired()
	}
}

func (s *Service) sweepExpired() {
	var due []Attempt
	s.db.Where("status = ? AND deadline IS NOT NULL AND deadline < ?", AttemptInProgress, time.Now().Add(-submitGrace)).Limit(100).Find(&due)
	for i := range due {
		_ = s.Submit(&due[i], nil, true)
	}
}

// resumeTests re-queues question writing and marking interrupted by a restart.
func (s *Service) resumeTests() int {
	var pools []TestSettings
	s.db.Where("pool_status = ?", StatusProcessing).Find(&pools)
	for _, p := range pools {
		id := p.UnitID
		var owner int
		s.db.Model(&coursemodels.Course{}).Where("course_id = ?", p.CourseID).Pluck("user_id", &owner)
		s.enqueue(func() { s.writeQuestions(id, owner) })
	}
	var marking []Attempt
	s.db.Where("status = ?", AttemptMarking).Find(&marking)
	for _, a := range marking {
		id := a.ID
		s.enqueue(func() { s.markAttempt(id) })
	}
	go s.sweepLoop()
	return len(pools) + len(marking)
}
