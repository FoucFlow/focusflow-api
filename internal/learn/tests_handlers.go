package learn

import (
	"net/http"
	"strings"
	"time"

	"focusflow-api/internal/auth"
	coursemodels "focusflow-api/internal/course/models"

	"github.com/gin-gonic/gin"
)

// ── views ────────────────────────────────────────────────────────────────

// learnerItem is what a learner sees of one question: never the correct
// option or the answer guide (the pool is reused across learners).
type learnerItem struct {
	Kind     string   `json:"kind"`
	Prompt   string   `json:"prompt"`
	Options  []string `json:"options,omitempty"`
	Choice   *int     `json:"choice"`
	Answer   string   `json:"answer"`
	Mark     *float64 `json:"mark,omitempty"`
	Feedback string   `json:"feedback,omitempty"`
	Note     string   `json:"teacher_note,omitempty"`
}

type learnerAttempt struct {
	ID            int           `json:"id"`
	UnitID        int           `json:"unit_id"`
	Number        int           `json:"number"`
	Status        string        `json:"status"`
	Items         []learnerItem `json:"items"`
	StartedAt     time.Time     `json:"started_at"`
	Deadline      *time.Time    `json:"deadline"`
	SubmittedAt   *time.Time    `json:"submitted_at"`
	AutoSubmitted bool          `json:"auto_submitted"`
	ScorePercent  *float64      `json:"score_percent"`
	Passed        bool          `json:"passed"`
	PassMark      int           `json:"pass_mark"`
	TrackTab      bool          `json:"track_tab"`
	TrackPaste    bool          `json:"track_paste"`
	Message       string        `json:"message,omitempty"`
	ServerTime    time.Time     `json:"server_time"` // for the countdown
}

// toLearner hides marks until the result is final (after review when on).
func toLearner(a *Attempt) learnerAttempt {
	v := learnerAttempt{ID: a.ID, UnitID: a.UnitID, Number: a.Number, Status: a.Status, StartedAt: a.StartedAt,
		Deadline: a.Deadline, SubmittedAt: a.SubmittedAt, AutoSubmitted: a.AutoSubmitted, Passed: a.Passed,
		PassMark: a.Settings.PassMark, TrackTab: a.Settings.TrackTab, TrackPaste: a.Settings.TrackPaste,
		ServerTime: time.Now(), Items: make([]learnerItem, len(a.Items))}
	done := a.Status == AttemptDone
	for i, it := range a.Items {
		li := learnerItem{Kind: it.Kind, Prompt: it.Prompt, Options: it.Options, Choice: it.Choice, Answer: it.Answer}
		if done {
			li.Mark, li.Feedback, li.Note = it.Mark, it.AIFeedback, it.Note
		}
		v.Items[i] = li
	}
	switch a.Status {
	case AttemptDone:
		sc := a.ScorePercent
		v.ScorePercent = &sc
	case AttemptMarking:
		v.Message = "Your written answers are being marked. This usually takes under a minute."
	case AttemptReview:
		v.Message = "Your teacher reviews this test before you get the result."
	}
	return v
}

// learnerSettings is the part of the settings shown before starting (F2.3).
func learnerSettings(st TestSettings, pool int) gin.H {
	n := st.QuestionsPerTest
	if n > pool {
		n = pool
	}
	return gin.H{"questions": n, "pass_mark": st.PassMark, "time_limit_minutes": st.TimeLimitMinutes,
		"max_attempts": st.MaxAttempts, "teacher_review": st.TeacherReview, "track_tab": st.TrackTab, "track_paste": st.TrackPaste}
}

// testSummary goes into each unit of the course view.
type testSummary struct {
	Enabled        bool   `json:"enabled"`
	Questions      int    `json:"questions,omitempty"`
	PoolStatus     string `json:"pool_status,omitempty"`
	PendingReviews int    `json:"pending_reviews,omitempty"`
	Passed         bool   `json:"passed,omitempty"`
}

// testSummaries: creator=true adds pool details and reviews waiting.
func (s *Service) testSummaries(unitIDs []int, creator bool, userID int) map[int]*testSummary {
	out := map[int]*testSummary{}
	if len(unitIDs) == 0 {
		return out
	}
	active := s.activeTests(unitIDs)
	if creator {
		var sts []TestSettings
		s.db.Where("unit_id IN ?", unitIDs).Find(&sts)
		for _, st := range sts {
			out[st.UnitID] = &testSummary{Enabled: st.Enabled, PoolStatus: st.PoolStatus}
		}
		type cnt struct {
			UnitID int
			N      int
		}
		var qs, rv []cnt
		s.db.Model(&Question{}).Select("unit_id, COUNT(*) AS n").Where("unit_id IN ?", unitIDs).Group("unit_id").Scan(&qs)
		s.db.Model(&Attempt{}).Select("unit_id, COUNT(*) AS n").Where("unit_id IN ? AND status = ?", unitIDs, AttemptReview).Group("unit_id").Scan(&rv)
		for _, c := range qs {
			if out[c.UnitID] == nil {
				out[c.UnitID] = &testSummary{PoolStatus: StatusIdle}
			}
			out[c.UnitID].Questions = c.N
		}
		for _, c := range rv {
			if out[c.UnitID] != nil {
				out[c.UnitID].PendingReviews = c.N
			}
		}
		return out
	}
	var passed []int
	s.db.Model(&Attempt{}).Where("user_id = ? AND unit_id IN ? AND passed", userID, unitIDs).Distinct().Pluck("unit_id", &passed)
	for id := range active {
		out[id] = &testSummary{Enabled: true}
	}
	for _, id := range passed {
		if out[id] != nil {
			out[id].Passed = true
		}
	}
	return out
}

// ── creator: pool and settings ───────────────────────────────────────────

// GET /api/learn/units/:uid/test: the creator's pool and settings, or the
// learner's test page (settings, past attempts, whether they can start).
func (h *Handler) GetTest(c *gin.Context) {
	uid, ok := paramID(c, "uid")
	if !ok {
		return
	}
	var u Unit
	if err := h.s.db.First(&u, uid).Error; err != nil {
		fail(c, http.StatusNotFound, "Unit not found.")
		return
	}
	course, err := h.s.course(u.CourseID)
	if err != nil {
		fail(c, http.StatusNotFound, "Unit not found.")
		return
	}
	me := c.GetInt(auth.CtxUserID)
	st := h.s.testSettings(u.ID, u.CourseID)
	if course.UserId == me || isAdmin(c) {
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"role": "creator", "unit": u, "settings": st,
			"questions": h.s.questions(u.ID), "ai_available": h.s.exam != nil, "limits": h.s.limits}})
		return
	}
	if !h.learnerCanOpen(c, course, &u) {
		return
	}
	pool := h.s.questionCount(u.ID)
	if !st.Enabled || pool == 0 {
		fail(c, http.StatusNotFound, "This unit has no test.")
		return
	}
	h.s.openAttempt(me, u.ID) // submits it if its time ran out
	attempts := h.s.attempts(me, u.ID)
	canStart, reason := h.s.canStart(me, st, attempts)
	views := make([]learnerAttempt, len(attempts))
	var inProgress *int
	for i := range attempts {
		views[i] = toLearner(&attempts[i])
		views[i].Items = nil // results list only; open one attempt for its answers
		if attempts[i].Status == AttemptInProgress {
			id := attempts[i].ID
			inProgress, canStart, reason = &id, true, ""
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"role": "learner", "unit": gin.H{"id": u.ID, "title": u.Title, "course_id": u.CourseID},
		"settings": learnerSettings(st, pool), "attempts": views, "can_start": canStart, "reason": reason,
		"in_progress_attempt_id": inProgress}})
}

// learnerCanOpen: the course is published, the user joined it and the unit is open.
func (h *Handler) learnerCanOpen(c *gin.Context, course *coursemodels.Course, u *Unit) bool {
	me := c.GetInt(auth.CtxUserID)
	if course.CourseStatus != "published" || !h.s.isEnrolled(me, u.CourseID) {
		fail(c, http.StatusNotFound, "Unit not found.")
		return false
	}
	if !unlocked(h.s.units(u.CourseID), h.s.completedUnits(me, u.CourseID))[u.ID] {
		fail(c, http.StatusForbidden, "Finish the previous unit first.")
		return false
	}
	return true
}

// PUT /api/learn/units/:uid/test {enabled, questions_per_test, pass_mark, time_limit_minutes, max_attempts, teacher_review, track_tab, track_paste}
func (h *Handler) UpdateTest(c *gin.Context) {
	u, _, ok := h.ownedUnit(c)
	if !ok {
		return
	}
	var in struct {
		Enabled          *bool `json:"enabled"`
		QuestionsPerTest *int  `json:"questions_per_test"`
		PassMark         *int  `json:"pass_mark"`
		TimeLimitMinutes *int  `json:"time_limit_minutes"`
		MaxAttempts      *int  `json:"max_attempts"`
		TeacherReview    *bool `json:"teacher_review"`
		TrackTab         *bool `json:"track_tab"`
		TrackPaste       *bool `json:"track_paste"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "The request body must be valid JSON.")
		return
	}
	st := h.s.testSettings(u.ID, u.CourseID)
	rng := func(p *int, lo, hi int, dst *int, msg string) bool {
		if p == nil {
			return true
		}
		if *p < lo || *p > hi {
			fail(c, http.StatusUnprocessableEntity, msg)
			return false
		}
		*dst = *p
		return true
	}
	if !rng(in.QuestionsPerTest, 1, 50, &st.QuestionsPerTest, "Questions per test must be between 1 and 50.") ||
		!rng(in.PassMark, 1, 100, &st.PassMark, "The pass mark must be between 1 and 100 percent.") ||
		!rng(in.TimeLimitMinutes, 0, 180, &st.TimeLimitMinutes, "The time limit must be between 0 (none) and 180 minutes.") ||
		!rng(in.MaxAttempts, 0, 20, &st.MaxAttempts, "Attempts must be between 0 (unlimited) and 20.") {
		return
	}
	for p, dst := range map[*bool]*bool{in.TeacherReview: &st.TeacherReview, in.TrackTab: &st.TrackTab, in.TrackPaste: &st.TrackPaste} {
		if p != nil {
			*dst = *p
		}
	}
	if in.Enabled != nil {
		if *in.Enabled && h.s.questionCount(u.ID) == 0 {
			fail(c, http.StatusUnprocessableEntity, "Add questions before turning the test on.")
			return
		}
		st.Enabled = *in.Enabled
	}
	if err := h.s.saveSettings(&st); err != nil {
		fail(c, http.StatusInternalServerError, "Could not save the test settings.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": st})
}

// POST /api/learn/units/:uid/questions/generate
func (h *Handler) GenerateQuestions(c *gin.Context) {
	u, course, ok := h.ownedUnit(c)
	if !ok {
		return
	}
	if err := h.s.StartQuestions(u, course); err != nil {
		fail(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message": "Writing questions. This usually takes under a minute.", "pool_status": StatusProcessing})
}

type questionIn struct {
	Kind        *string   `json:"kind"`
	Prompt      *string   `json:"prompt"`
	Options     *[]string `json:"options"`
	Correct     *int      `json:"correct"`
	AnswerGuide *string   `json:"answer_guide"`
	Concept     *string   `json:"concept"`
}

// apply validates and copies the fields into q.
func (in questionIn) apply(q *Question) string {
	if in.Kind != nil {
		q.Kind = *in.Kind
	}
	if in.Prompt != nil {
		q.Prompt = strings.TrimSpace(*in.Prompt)
	}
	if in.Options != nil {
		q.Options = []string{}
		for _, o := range *in.Options {
			q.Options = append(q.Options, strings.TrimSpace(o))
		}
	}
	if in.Correct != nil {
		q.Correct = *in.Correct
	}
	if in.AnswerGuide != nil {
		q.AnswerGuide = strings.TrimSpace(*in.AnswerGuide)
	}
	if in.Concept != nil {
		q.Concept = clip(strings.TrimSpace(*in.Concept), 255)
	}
	if q.Prompt == "" {
		return "A question needs its text."
	}
	switch q.Kind {
	case QuestionMCQ:
		if err := validOptions(q.Options, q.Correct); err != nil {
			return err.Error()
		}
	case QuestionShort, QuestionExplain:
		q.Options, q.Correct = []string{}, 0
		if q.AnswerGuide == "" {
			return "Written questions need an answer guide (what a full-mark answer contains), so they can be marked."
		}
	default:
		return "kind must be mcq, short or explain."
	}
	return ""
}

// POST /api/learn/units/:uid/questions
func (h *Handler) AddQuestion(c *gin.Context) {
	u, _, ok := h.ownedUnit(c)
	if !ok {
		return
	}
	var in questionIn
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "The request body must be valid JSON.")
		return
	}
	q := Question{UnitID: u.ID, CourseID: u.CourseID, Source: "manual", Options: []string{}}
	if msg := in.apply(&q); msg != "" {
		fail(c, http.StatusUnprocessableEntity, msg)
		return
	}
	if err := h.s.db.Create(&q).Error; err != nil {
		fail(c, http.StatusInternalServerError, "Could not add the question.")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": q})
}

func (h *Handler) ownedQuestion(c *gin.Context) (*Question, bool) {
	qid, ok := paramID(c, "qid")
	if !ok {
		return nil, false
	}
	var q Question
	if err := h.s.db.First(&q, qid).Error; err != nil {
		fail(c, http.StatusNotFound, "Question not found.")
		return nil, false
	}
	course, err := h.s.course(q.CourseID)
	if err != nil || (course.UserId != c.GetInt(auth.CtxUserID) && !isAdmin(c)) {
		fail(c, http.StatusForbidden, "Only the course creator can do this.")
		return nil, false
	}
	return &q, true
}

// PUT /api/learn/questions/:qid: an edited AI question is kept on the next AI run.
func (h *Handler) UpdateQuestion(c *gin.Context) {
	q, ok := h.ownedQuestion(c)
	if !ok {
		return
	}
	var in questionIn
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "The request body must be valid JSON.")
		return
	}
	if msg := in.apply(q); msg != "" {
		fail(c, http.StatusUnprocessableEntity, msg)
		return
	}
	if q.Source == "ai" {
		q.Source = "edited"
	}
	if err := h.s.db.Save(q).Error; err != nil {
		fail(c, http.StatusInternalServerError, "Could not save the question.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": q})
}

// DELETE /api/learn/questions/:qid (past attempts keep their copy)
func (h *Handler) DeleteQuestion(c *gin.Context) {
	q, ok := h.ownedQuestion(c)
	if !ok {
		return
	}
	h.s.db.Delete(q)
	if h.s.questionCount(q.UnitID) == 0 {
		h.s.db.Model(&TestSettings{}).Where("unit_id = ?", q.UnitID).Update("enabled", false)
	}
	c.JSON(http.StatusOK, gin.H{"message": "Question deleted."})
}

// ── learner: taking a test ───────────────────────────────────────────────

// POST /api/learn/units/:uid/test/start: resumes an open attempt or starts one.
func (h *Handler) StartTest(c *gin.Context) {
	uid, ok := paramID(c, "uid")
	if !ok {
		return
	}
	var u Unit
	if err := h.s.db.First(&u, uid).Error; err != nil {
		fail(c, http.StatusNotFound, "Unit not found.")
		return
	}
	course, err := h.s.course(u.CourseID)
	if err != nil {
		fail(c, http.StatusNotFound, "Unit not found.")
		return
	}
	if course.UserId == c.GetInt(auth.CtxUserID) {
		fail(c, http.StatusUnprocessableEntity, "You created this course; learners take its tests.")
		return
	}
	if !h.learnerCanOpen(c, course, &u) {
		return
	}
	a, err := h.s.StartAttempt(c.GetInt(auth.CtxUserID), &u)
	if err != nil {
		fail(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": toLearner(a)})
}

// attemptFor loads :aid for its learner (mine) or the course creator.
func (h *Handler) attemptFor(c *gin.Context) (a *Attempt, creator bool, ok bool) {
	aid, ok := paramID(c, "aid")
	if !ok {
		return nil, false, false
	}
	var at Attempt
	if err := h.s.db.First(&at, aid).Error; err != nil {
		fail(c, http.StatusNotFound, "Test not found.")
		return nil, false, false
	}
	me := c.GetInt(auth.CtxUserID)
	if at.UserID == me {
		return &at, false, true
	}
	course, err := h.s.course(at.CourseID)
	if err == nil && (course.UserId == me || isAdmin(c)) {
		return &at, true, true
	}
	fail(c, http.StatusNotFound, "Test not found.")
	return nil, false, false
}

// mine loads :aid only for the learner who sits it.
func (h *Handler) mine(c *gin.Context) (*Attempt, bool) {
	a, creator, ok := h.attemptFor(c)
	if ok && creator {
		fail(c, http.StatusForbidden, "Only the learner can answer this test.")
		return nil, false
	}
	return a, ok
}

// PUT /api/learn/attempts/:aid/answers {answers: [{index, choice?, answer?}]}
func (h *Handler) SaveAnswers(c *gin.Context) {
	a, ok := h.mine(c)
	if !ok {
		return
	}
	var in struct {
		Answers []AnswerIn `json:"answers"`
	}
	_ = c.ShouldBindJSON(&in)
	if err := h.s.SaveAnswers(a, in.Answers); err != nil {
		fail(c, http.StatusConflict, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Saved.", "server_time": time.Now()})
}

// POST /api/learn/attempts/:aid/events {events: ["tab_hidden", "paste", ...]}
func (h *Handler) LogEvents(c *gin.Context) {
	a, ok := h.mine(c)
	if !ok {
		return
	}
	var in struct {
		Events []string `json:"events"`
	}
	_ = c.ShouldBindJSON(&in)
	if len(in.Events) > 50 {
		in.Events = in.Events[:50]
	}
	if err := h.s.LogEvents(a, in.Events); err != nil {
		fail(c, http.StatusConflict, err.Error())
		return
	}
	c.Status(http.StatusNoContent)
}

// POST /api/learn/attempts/:aid/submit {answers}
func (h *Handler) SubmitTest(c *gin.Context) {
	a, ok := h.mine(c)
	if !ok {
		return
	}
	var in struct {
		Answers []AnswerIn `json:"answers"`
	}
	_ = c.ShouldBindJSON(&in)
	if err := h.s.Submit(a, in.Answers, false); err != nil {
		fail(c, http.StatusConflict, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": toLearner(a)})
}

// GET /api/learn/attempts/:aid: the learner's own view, or the creator's full
// view with the answers, marks, evidence and change log.
func (h *Handler) GetAttempt(c *gin.Context) {
	a, creator, ok := h.attemptFor(c)
	if !ok {
		return
	}
	if !creator {
		if a.Status == AttemptInProgress && expired(a, 0) {
			_ = h.s.Submit(a, nil, true)
			h.s.db.First(a, a.ID)
		}
		c.JSON(http.StatusOK, gin.H{"data": toLearner(a)})
		return
	}
	var learner auth.UserBrief
	h.s.db.Where("id = ?", a.UserID).First(&learner)
	changes := []MarkChange{}
	h.s.db.Where("attempt_id = ?", a.ID).Order("id").Find(&changes)
	var u Unit
	h.s.db.First(&u, a.UnitID)
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"attempt": a, "learner": learner, "unit": gin.H{"id": u.ID, "title": u.Title},
		"evidence": evidenceSummary(a.Events), "changes": changes}})
}

// evidenceSummary counts each kind of browser signal.
func evidenceSummary(events []TestEvent) map[string]int {
	out := map[string]int{}
	for _, e := range events {
		out[e.Kind]++
	}
	return out
}

// ── teacher review (F2.4) ────────────────────────────────────────────────

// GET /api/learn/courses/:id/attempts?status=review|done|all
func (h *Handler) CourseAttempts(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	q := h.s.db.Where("course_id = ?", *course.CourseId)
	switch st := c.DefaultQuery("status", AttemptReview); st {
	case "all":
		q = q.Where("status <> ?", AttemptInProgress)
	case AttemptReview, AttemptDone, AttemptMarking:
		q = q.Where("status = ?", st)
	default:
		fail(c, http.StatusUnprocessableEntity, "status must be review, done, marking or all.")
		return
	}
	var attempts []Attempt
	q.Order("submitted_at DESC NULLS LAST, id DESC").Limit(200).Find(&attempts)
	userIDs, unitIDs := []int{0}, []int{0}
	for _, a := range attempts {
		userIDs, unitIDs = append(userIDs, a.UserID), append(unitIDs, a.UnitID)
	}
	var users []auth.UserBrief
	h.s.db.Where("id IN ?", userIDs).Find(&users)
	names := map[int]string{}
	for _, u := range users {
		names[u.ID] = u.Name
	}
	var units []Unit
	h.s.db.Where("id IN ?", unitIDs).Find(&units)
	titles := map[int]string{}
	for _, u := range units {
		titles[u.ID] = u.Title
	}
	type row struct {
		ID            int            `json:"id"`
		UnitID        int            `json:"unit_id"`
		UnitTitle     string         `json:"unit_title"`
		LearnerID     int            `json:"learner_id"`
		LearnerName   string         `json:"learner_name"`
		Number        int            `json:"number"`
		Status        string         `json:"status"`
		ScorePercent  float64        `json:"score_percent"`
		Passed        bool           `json:"passed"`
		SubmittedAt   *time.Time     `json:"submitted_at"`
		AutoSubmitted bool           `json:"auto_submitted"`
		Evidence      map[string]int `json:"evidence"`
		MarkError     string         `json:"mark_error,omitempty"`
	}
	out := make([]row, 0, len(attempts))
	for _, a := range attempts {
		out = append(out, row{ID: a.ID, UnitID: a.UnitID, UnitTitle: titles[a.UnitID], LearnerID: a.UserID, LearnerName: names[a.UserID],
			Number: a.Number, Status: a.Status, ScorePercent: a.ScorePercent, Passed: a.Passed, SubmittedAt: a.SubmittedAt,
			AutoSubmitted: a.AutoSubmitted, Evidence: evidenceSummary(a.Events), MarkError: a.MarkError})
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

// POST /api/learn/attempts/:aid/review {marks: [{index, mark, note}]}
func (h *Handler) ReviewAttempt(c *gin.Context) {
	a, creator, ok := h.attemptFor(c)
	if !ok {
		return
	}
	if !creator {
		fail(c, http.StatusForbidden, "Only the course creator can review tests.")
		return
	}
	var in struct {
		Marks []ReviewMark `json:"marks"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "The request body must be valid JSON.")
		return
	}
	if err := h.s.Review(a, c.GetInt(auth.CtxUserID), in.Marks); err != nil {
		fail(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": a, "message": "Review saved. The learner can see the result."})
}
