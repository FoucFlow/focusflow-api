package learn

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// One quiz feature (FEATURES.md M2): a unit test, a practice quiz and the
// final exam use the same engine. A quiz placed between units is a unit of
// kind "quiz"; restriction levels are presets of the settings (F2.12).

const (
	LevelPractice   = "practice"
	LevelStandard   = "standard"
	LevelStrict     = "strict"
	LevelSupervised = "supervised"
	LevelCustom     = "custom"
)

// show_answers: after_question (practice), when_finished (after passing or
// the last attempt), after_submit, after_close, never.
var showAnswersValues = map[string]bool{"after_question": true, "when_finished": true, "after_submit": true, "after_close": true, "never": true}

// applyLevel sets the settings of a restriction level. The time limit is kept
// when the level allows one; Strict and Supervised need one.
func applyLevel(st *TestSettings, level string) error {
	switch level {
	case LevelPractice:
		st.Practice, st.TimeLimitMinutes, st.MaxAttempts, st.ShowAnswers = true, 0, 0, "after_question"
		st.TrackTab, st.TrackPaste, st.TeacherReview, st.NoBack = false, false, false, false
		st.AccessCode, st.ExplainAnswers, st.Camera = "", false, false
	case LevelStandard:
		st.Practice, st.MaxAttempts, st.ShowAnswers = false, 3, "when_finished"
		st.TrackTab, st.TrackPaste, st.TeacherReview, st.NoBack = false, false, false, false
		st.AccessCode, st.ExplainAnswers, st.Camera = "", false, false
	case LevelStrict, LevelSupervised:
		st.Practice, st.MaxAttempts, st.ShowAnswers = false, 2, "after_close"
		if st.TimeLimitMinutes == 0 {
			st.TimeLimitMinutes = 20
		}
		st.TrackTab, st.TrackPaste, st.TeacherReview, st.NoBack, st.AllOnOnePage = true, true, true, true, false
		st.AccessCode, st.ExplainAnswers, st.Camera = "", false, false
		if level == LevelSupervised {
			st.AccessCode, st.ExplainAnswers = newAccessCode(), true
		}
	default:
		return errors.New("level must be practice, standard, strict or supervised.")
	}
	st.Level = level
	return nil
}

// levelOf names the settings: a preset when they match one, else custom.
func levelOf(st TestSettings) string {
	switch {
	case st.Practice && st.TimeLimitMinutes == 0 && st.MaxAttempts == 0 && st.ShowAnswers == "after_question" &&
		!st.TrackTab && !st.TrackPaste && !st.TeacherReview && !st.NoBack && st.AccessCode == "" && !st.ExplainAnswers:
		return LevelPractice
	case !st.Practice && st.MaxAttempts == 3 && st.ShowAnswers == "when_finished" &&
		!st.TrackTab && !st.TrackPaste && !st.TeacherReview && !st.NoBack && st.AccessCode == "" && !st.ExplainAnswers:
		return LevelStandard
	case !st.Practice && st.TimeLimitMinutes > 0 && st.MaxAttempts == 2 && st.ShowAnswers == "after_close" &&
		st.TrackTab && st.TrackPaste && st.TeacherReview && st.NoBack && !st.AllOnOnePage:
		if st.AccessCode != "" && st.ExplainAnswers {
			return LevelSupervised
		}
		if st.AccessCode == "" && !st.ExplainAnswers {
			return LevelStrict
		}
	}
	return LevelCustom
}

// newAccessCode: 6 characters, no look-alikes (0/O, 1/I).
func newAccessCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 6)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b[i] = alphabet[n.Int64()]
	}
	return string(b)
}

// syncOptional keeps units.optional right: a quiz never blocks while it is a
// practice quiz, turned off or empty. Lessons are never optional.
func (s *Service) syncOptional(unitID int) {
	var u Unit
	if s.db.First(&u, unitID).Error != nil {
		return
	}
	opt := false
	if u.Kind == "quiz" {
		st := s.testSettings(u.ID, u.CourseID)
		opt = st.Practice || !st.Enabled || s.questionCount(u.ID) == 0
	}
	if opt != u.Optional {
		s.db.Model(&Unit{}).Where("id = ?", u.ID).Update("optional", opt)
	}
}

// windowOpen checks the quiz's open/close dates.
func windowOpen(st TestSettings, now time.Time) (bool, string) {
	if st.OpensAt != nil && now.Before(*st.OpensAt) {
		return false, "This test opens on " + st.OpensAt.UTC().Format("2 Jan 2006 at 15:04 UTC") + "."
	}
	if st.ClosesAt != nil && now.After(*st.ClosesAt) {
		return false, "This test closed on " + st.ClosesAt.UTC().Format("2 Jan 2006 at 15:04 UTC") + "."
	}
	return true, ""
}

// answersVisible: may the learner see the right answers of a finished
// attempt? lastChance: the learner passed or has no attempts left.
func answersVisible(st TestSettings, now time.Time, lastChance bool) bool {
	switch st.ShowAnswers {
	case "after_question", "after_submit":
		return true
	case "when_finished":
		return lastChance
	case "after_close":
		return st.ClosesAt != nil && now.After(*st.ClosesAt)
	}
	return false
}

// markChoice marks a picked-answer item in code: one answer right = 1; for
// multiple select every right option and no wrong one = 1, some right and no
// wrong = 0.5, else 0.
func markChoice(it *AttemptItem) float64 {
	if it.Kind != QuestionMulti {
		if it.Choice != nil && *it.Choice == it.Correct {
			return 1
		}
		return 0
	}
	right := map[int]bool{}
	for _, c := range it.Corrects {
		right[c] = true
	}
	hit, wrong := 0, 0
	for _, c := range it.Choices {
		if right[c] {
			hit++
		} else {
			wrong++
		}
	}
	switch {
	case wrong > 0 || hit == 0:
		return 0
	case hit == len(right):
		return 1
	default:
		return 0.5
	}
}

// examMaterial is what the AI writes questions from: the unit's lessons
// (AI-written courses) or its slides word for word (slides courses). A quiz
// unit uses the units it covers.
func (s *Service) examMaterial(u *Unit) []ExamConcept {
	ids := []int{u.ID}
	if u.Kind == "quiz" {
		ids = s.coveredUnits(u)
	}
	var out []ExamConcept
	for _, id := range ids {
		var lesson Unit
		if s.db.First(&lesson, id).Error != nil {
			continue
		}
		var concepts []Concept
		s.db.Where("unit_id = ?", id).Order("position, id").Find(&concepts)
		for _, c := range concepts {
			out = append(out, ExamConcept{Title: c.Title, Lesson: clip(c.Explanation, 3000)})
		}
		for _, r := range s.unitRanges([]int{id})[id] {
			var pages []SlidePage
			q := s.db.Where("material_id = ? AND NOT hidden", r.MaterialID)
			if r.FirstPage > 0 {
				q = q.Where("number >= ?", r.FirstPage)
			}
			if r.LastPage > 0 {
				q = q.Where("number <= ?", r.LastPage)
			}
			q.Order("number").Find(&pages)
			for _, p := range pages {
				if text := pageText(p); text != "" {
					title := p.Title
					if title == "" {
						title = fmt.Sprintf("%s, slide %d", lesson.Title, p.Number)
					}
					out = append(out, ExamConcept{Title: clip(title, 255), Lesson: clip(text, 3000)})
				}
			}
		}
		if len(concepts) == 0 && strings.TrimSpace(lesson.Body) != "" && lesson.Kind != "quiz" {
			out = append(out, ExamConcept{Title: lesson.Title, Lesson: clip(lesson.Body, 8000)})
		}
	}
	if len(out) > 60 {
		out = out[:60] // keeps the prompt bounded on long courses
	}
	return out
}

// coveredUnits: the lessons a quiz covers (its Covers, or every lesson before it).
func (s *Service) coveredUnits(q *Unit) []int {
	units := s.units(q.CourseID)
	sort.SliceStable(units, func(i, j int) bool { return units[i].Position < units[j].Position })
	allowed := map[int]bool{}
	for _, id := range q.Covers {
		allowed[id] = true
	}
	var out []int
	for _, u := range units {
		if u.ID == q.ID {
			if len(q.Covers) == 0 {
				break
			}
			continue
		}
		if u.Kind == "quiz" {
			continue
		}
		if len(q.Covers) == 0 || allowed[u.ID] {
			out = append(out, u.ID)
		}
	}
	return out
}

// pageText flattens a slide page's text for the AI (titles stay separate).
func pageText(p SlidePage) string {
	var b strings.Builder
	for _, bl := range p.Blocks {
		switch bl.Type {
		case "text":
			for _, l := range bl.Lines {
				b.WriteString(strings.Repeat("  ", l.Level))
				if l.Bullet {
					b.WriteString("- ")
				}
				b.WriteString(joinRuns(l.Runs))
				b.WriteString("\n")
			}
		case "code":
			b.WriteString("```\n" + bl.Code + "\n```\n")
		case "table":
			for _, row := range bl.Rows {
				b.WriteString(strings.Join(row, " | ") + "\n")
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// ── quiz units ───────────────────────────────────────────────────────────

// POST /api/learn/courses/:id/quizzes {title, after_unit_id, covers, final, practice}
// Places a quiz in the course: after the given unit (0 = at the end).
func (h *Handler) CreateQuiz(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	var in struct {
		Title       string `json:"title"`
		AfterUnitID int    `json:"after_unit_id"`
		Covers      []int  `json:"covers"`
		Final       bool   `json:"final"`
		Practice    bool   `json:"practice"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "The request body must be valid JSON.")
		return
	}
	courseID := *course.CourseId
	units := h.s.units(courseID)
	byID := map[int]Unit{}
	for _, u := range units {
		byID[u.ID] = u
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		in.Title = "Quiz"
		if in.Final {
			in.Title = "Final exam"
		}
	}
	if len([]rune(in.Title)) > 255 {
		fail(c, http.StatusUnprocessableEntity, "The title is too long.")
		return
	}
	covers := []int{}
	for _, id := range in.Covers {
		if u, ok := byID[id]; !ok || u.Kind == "quiz" {
			fail(c, http.StatusUnprocessableEntity, "A quiz can only cover lessons of this course.")
			return
		}
		covers = append(covers, id)
	}
	pos := len(units) + 1
	if in.AfterUnitID != 0 {
		after, ok := byID[in.AfterUnitID]
		if !ok {
			fail(c, http.StatusUnprocessableEntity, "Unknown unit to place the quiz after.")
			return
		}
		pos = after.Position + 1
	}
	if in.Final {
		for _, u := range units {
			if u.Final {
				fail(c, http.StatusConflict, "This course already has a final exam.")
				return
			}
		}
		pos, covers = len(units)+1, []int{}
	}
	q := Unit{CourseID: courseID, Title: in.Title, Kind: "quiz", Covers: covers, Final: in.Final, Optional: true, Source: "manual"}
	err := h.s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Unit{}).Where("course_id = ? AND position >= ?", courseID, pos).
			Update("position", gorm.Expr("position + 1")).Error; err != nil {
			return err
		}
		q.Position = pos
		if err := tx.Create(&q).Error; err != nil {
			return err
		}
		if err := renumber(tx, courseID); err != nil {
			return err
		}
		st := defaultTestSettings(q.ID, courseID)
		level := LevelStandard
		if in.Practice {
			level = LevelPractice
		} else if in.Final {
			level = LevelStrict
		}
		_ = applyLevel(&st, level)
		if in.Final {
			st.QuestionsPerTest = 20
		}
		return tx.Create(&st).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "Could not add the quiz.")
		return
	}
	h.s.db.First(&q, q.ID) // renumber may have moved it before the final exam
	c.JSON(http.StatusCreated, gin.H{"data": q})
}

// PUT /api/learn/units/:uid/quiz {title?, covers?}: what a quiz unit covers.
func (h *Handler) UpdateQuiz(c *gin.Context) {
	u, _, ok := h.ownedUnit(c)
	if !ok {
		return
	}
	if u.Kind != "quiz" {
		fail(c, http.StatusUnprocessableEntity, "This unit is not a quiz.")
		return
	}
	var in struct {
		Title  *string `json:"title"`
		Covers *[]int  `json:"covers"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "The request body must be valid JSON.")
		return
	}
	updates := map[string]any{}
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" || len([]rune(t)) > 255 {
			fail(c, http.StatusUnprocessableEntity, "A quiz needs a title (up to 255 characters).")
			return
		}
		updates["title"] = t
	}
	if in.Covers != nil {
		lessons := map[int]bool{}
		for _, x := range h.s.units(u.CourseID) {
			if x.Kind != "quiz" {
				lessons[x.ID] = true
			}
		}
		covers := []int{}
		for _, id := range *in.Covers {
			if !lessons[id] {
				fail(c, http.StatusUnprocessableEntity, "A quiz can only cover lessons of this course.")
				return
			}
			covers = append(covers, id)
		}
		u.Covers = covers
		if err := h.s.db.Model(u).Select("covers").Updates(u).Error; err != nil {
			fail(c, http.StatusInternalServerError, "Could not save the quiz.")
			return
		}
	}
	if len(updates) > 0 {
		h.s.db.Model(&Unit{}).Where("id = ?", u.ID).Updates(updates)
	}
	h.s.db.First(u, u.ID)
	c.JSON(http.StatusOK, gin.H{"data": u})
}

// POST /api/learn/attempts/:aid/check {index}: practice quizzes show the
// right answer after each question ("after_question").
func (h *Handler) CheckAnswer(c *gin.Context) {
	a, ok := h.mine(c)
	if !ok {
		return
	}
	var in struct {
		Index int `json:"index"`
	}
	_ = c.ShouldBindJSON(&in)
	if a.Settings.ShowAnswers != "after_question" {
		fail(c, http.StatusForbidden, "Answers are shown at the end of this test.")
		return
	}
	if a.Status != AttemptInProgress || in.Index < 0 || in.Index >= len(a.Items) {
		fail(c, http.StatusUnprocessableEntity, "Unknown question.")
		return
	}
	it := a.Items[in.Index]
	out := gin.H{"index": in.Index, "answer_guide": it.Guide}
	if choiceKind(it.Kind) {
		out["correct"], out["corrects"], out["right"] = it.Correct, it.Corrects, markChoice(&it) == 1
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

// readyQuizzes: quiz units (among units) learners can take: on, with questions.
func (s *Service) readyQuizzes(units []Unit) map[int]bool {
	var ids []int
	for _, u := range units {
		if u.Kind == "quiz" {
			ids = append(ids, u.ID)
		}
	}
	out := map[int]bool{}
	if len(ids) == 0 {
		return out
	}
	var ready []int
	s.db.Model(&TestSettings{}).Where("unit_id IN ? AND enabled", ids).
		Where("EXISTS (SELECT 1 FROM unit_questions q WHERE q.unit_id = unit_tests.unit_id)").Pluck("unit_id", &ready)
	for _, id := range ready {
		out[id] = true
	}
	return out
}

// learnerUnits: the units learners see (quizzes not ready are left out).
func (s *Service) learnerUnits(courseID int) []Unit {
	all := s.units(courseID)
	ready := s.readyQuizzes(all)
	out := make([]Unit, 0, len(all))
	for _, u := range all {
		if u.Kind != "quiz" || ready[u.ID] {
			out = append(out, u)
		}
	}
	return out
}

// courseProgressFor: required units done out of required units (practice
// quizzes do not count), and whether the course is finished.
func (s *Service) courseProgressFor(userID, courseID int) (done, total int, finished bool) {
	completed := s.completedUnits(userID, courseID)
	for _, u := range s.learnerUnits(courseID) {
		if u.Optional {
			continue
		}
		total++
		if completed[u.ID] {
			done++
		}
	}
	return done, total, total > 0 && done >= total
}

func doneOf(s *Service, userID, courseID int) int {
	d, _, _ := s.courseProgressFor(userID, courseID)
	return d
}
func totalOf(s *Service, userID, courseID int) int {
	_, t, _ := s.courseProgressFor(userID, courseID)
	return t
}
