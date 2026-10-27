package learn

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	coursemodels "focusflow-api/internal/course/models"
	"focusflow-api/internal/platform/mailer"
	"focusflow-api/internal/platform/storage"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Limits are the free-tier settings (FEATURES.md section 7).
type Limits struct {
	// MonthlyBudgetUSD pauses new AI work once this calendar month's logged
	// AI spend reaches it (0 = no cap). Set in AI_MONTHLY_BUDGET_USD.
	MonthlyBudgetUSD      float64 `json:"-"`
	UserDailyUSD          float64 `json:"-"` // each creator's AI allowance (allowance.go); 0 = none
	UserMonthlyUSD        float64 `json:"-"`
	CoursesPerUser        int     `json:"courses_per_user"`
	MaterialsPerCourse    int     `json:"materials_per_course"`
	MaxFileBytes          int64   `json:"max_file_bytes"`
	OrganiseRunsPerCourse int     `json:"organise_runs_per_course"`
	QuestionRunsPerUnit   int     `json:"question_runs_per_unit"` // AI question pools per unit
	TestsPerDay           int     `json:"tests_per_day"`          // test starts per learner per day (written answers cost AI)
	NewCoursesPerDay      int     `json:"new_courses_per_day"`    // courses a creator may start per day (UTC), with the AI allowance
	PartPages             int     `json:"part_pages"`             // long PDFs are read this many pages at a time (0 = whole file)
	VideoPartSeconds      int     `json:"video_part_seconds"`     // long videos are watched this long at a time (0 = whole video)
	// The first part is short, so units can be made from it quickly
	FirstPartPages    int `json:"first_part_pages"`
	FirstVideoSeconds int `json:"first_video_seconds"`
	// Pro is what the Pro plan and institution members get (plan.go)
	Pro ProLimits `json:"-"`
	// Plan is filled in by limitsFor: the plan these limits belong to
	Plan string `json:"plan,omitempty"`
}

func (l Limits) partPages(first bool) int {
	if first && l.FirstPartPages > 0 && l.FirstPartPages < l.PartPages {
		return l.FirstPartPages
	}
	return l.PartPages
}

func (l Limits) videoPart(first bool) int {
	if first && l.FirstVideoSeconds > 0 && l.FirstVideoSeconds < l.VideoPartSeconds {
		return l.FirstVideoSeconds
	}
	return l.VideoPartSeconds
}

var DefaultLimits = Limits{CoursesPerUser: 2, MaterialsPerCourse: 15, MaxFileBytes: 20 << 20, OrganiseRunsPerCourse: 3, QuestionRunsPerUnit: 3, TestsPerDay: 20, PartPages: 25, VideoPartSeconds: 600, FirstPartPages: 8, FirstVideoSeconds: 180}

type Service struct {
	db          *gorm.DB
	joinCheck   func(c *coursemodels.Course, userID int) string // institution roster rule (institutions.go)
	mail        mailer.Mailer                                   // nil: no email (SetMailer)
	frontendURL string
	ai          AI
	videoAI     VideoAI       // nil: videos are described by their title and note only
	refs        Enricher      // nil: no "expand your knowledge" references
	videoLength VideoDuration // nil: videos are watched in one piece
	exam        Examiner      // nil: no AI questions or marking
	limits      Limits
	appKey      []byte // signs upload tokens
	jobs        chan func()
}

// NewService starts `workers` background workers for AI jobs. AI calls never
// run inside a request: the request saves a status and returns.
func NewService(db *gorm.DB, ai AI, limits Limits, appKey string, workers int) *Service {
	s := &Service{db: db, ai: ai, limits: limits, appKey: []byte(appKey), jobs: make(chan func(), 256)}
	for i := 0; i < workers; i++ {
		go func() {
			for job := range s.jobs {
				runJob(job)
			}
		}()
	}
	return s
}

// runJob runs one background job; a panic is logged instead of stopping the server.
func runJob(job func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("background job panicked", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	job()
}

func (s *Service) enqueue(job func()) {
	select {
	case s.jobs <- job:
	default:
		// Queue full: run on its own goroutine rather than drop the work
		go runJob(job)
	}
}

// ErrBudget means this month's AI budget is used up.
var ErrBudget = errors.New("the monthly AI budget is used up")

// MonthSpendUSD is the AI spend logged since the start of this month (UTC).
func (s *Service) MonthSpendUSD() float64 {
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	var micro int64
	s.db.Model(&AIUsage{}).Where("created_at >= ?", start).Select("COALESCE(SUM(cost_micro_usd), 0)").Scan(&micro)
	return float64(micro) / 1e6
}

// checkBudget returns ErrBudget when the monthly cap is reached.
func (s *Service) checkBudget() error {
	if s.limits.MonthlyBudgetUSD > 0 && s.MonthSpendUSD() >= s.limits.MonthlyBudgetUSD {
		return ErrBudget
	}
	return nil
}

const budgetMessage = "FocusFlow has reached its AI budget for this month, so new AI work is paused. Please try again later."

const creditMessage = "FocusFlow's AI account has run out of credit, so this can't be done right now. The site admin needs to top it up; then press Retry."

// VideoAI is the video reader (nil if none).
func (s *Service) VideoAI() VideoAI { return s.videoAI }

// SetVideoAI turns on video reading (Gemini).
func (s *Service) SetVideoAI(v VideoAI) { s.videoAI = v }

// Recorder returns the function that logs every AI request (ai_calls).
func (s *Service) Recorder() CallRecorder { return recorder(s.db) }

// SetVideoDuration lets long videos be watched in parts.
func (s *Service) SetVideoDuration(d VideoDuration) { s.videoLength = d }

// SetEnricher turns on reference finding (web search).
func (s *Service) SetEnricher(e Enricher) { s.refs = e }

// StartReferences queues the reference search for all of a course's concepts.
func (s *Service) StartReferences(c *coursemodels.Course) error { return s.startReferences(c, false) }

// startReferences: onlyMissing skips concepts that were searched before
// (used after new units are added for newly read pages).
func (s *Service) startReferences(c *coursemodels.Course, onlyMissing bool) error {
	if s.refs == nil {
		return errors.New("Finding references is not switched on yet on this server.")
	}
	if err := s.checkAI(c.UserId); err != nil {
		return errors.New(limitMessage(err))
	}
	res := s.db.Model(&coursemodels.Course{}).Where("course_id = ? AND references_status <> ?", *c.CourseId, StatusProcessing).
		Updates(map[string]any{"references_status": StatusProcessing, "references_done": 0, "references_total": 0})
	if res.RowsAffected == 0 {
		return errors.New("References are already being found.")
	}
	id, owner := *c.CourseId, c.UserId
	s.enqueue(func() { s.findReferences(id, owner, onlyMissing) })
	return nil
}

// findReferences runs unit by unit, so one failure does not lose the rest.
func (s *Service) findReferences(courseID, ownerID int, onlyMissing bool) {
	c, err := s.course(courseID)
	if err != nil {
		return
	}
	failed, busy := 0, false
	units := s.units(courseID)
	progress := func(done int) {
		s.db.Model(&coursemodels.Course{}).Where("course_id = ?", courseID).
			Updates(map[string]any{"references_done": done, "references_total": len(units)})
	}
	progress(0)
	for i, u := range units {
		if i > 0 {
			progress(i)
		}
		var concepts []Concept
		s.db.Where("unit_id = ?", u.ID).Order("position, id").Find(&concepts)
		if onlyMissing {
			var todo []Concept
			for _, con := range concepts {
				if con.References == nil {
					todo = append(todo, con)
				}
			}
			concepts = todo
		}
		if len(concepts) == 0 {
			continue
		}
		if s.checkAI(ownerID) != nil {
			slog.Warn("AI budget or allowance reached: reference search stopped", "course", courseID)
			failed++
			break
		}
		in := EnrichInput{CourseTitle: c.CourseTitle, Language: c.CourseLanguage, UnitTitle: u.Title}
		for i, con := range concepts {
			summary := con.Explanation
			if len(summary) > 600 {
				summary = summary[:600]
			}
			in.Concepts = append(in.Concepts, EnrichConcept{Index: i + 1, Title: con.Title, Summary: summary})
		}
		started := time.Now()
		refs, usage, err := s.refs.FindReferences(context.Background(), in)
		s.logUsage("references", courseID, ownerID, usage, started, err)
		if err != nil {
			slog.Warn("find references failed", "course", courseID, "unit", u.ID, "error", err)
			failed++
			if errors.Is(err, ErrAllKeysResting) {
				busy = true
				break // the other units would fail too; resumePaused goes on later
			}
			continue
		}
		for i := range concepts {
			if r, ok := refs[i+1]; ok {
				concepts[i].References = r
				s.db.Model(&concepts[i]).Select("references").Updates(&concepts[i])
			}
		}
	}
	status := StatusReady
	if failed > 0 {
		status = StatusFailed
	}
	s.db.Model(&coursemodels.Course{}).Where("course_id = ?", courseID).
		Updates(map[string]any{"references_status": status, "references_waiting": busy})
}

// ResumePending re-queues work that was interrupted by a restart.
func (s *Service) ResumePending() {
	go s.resumeLoop()
	var mats []Material
	s.db.Where("status = ? OR reading", StatusProcessing).Find(&mats)
	for _, m := range mats {
		id := m.ID
		s.enqueue(func() { s.digest(id, nil) })
	}
	var courses []coursemodels.Course
	s.db.Where("organise_status = ?", StatusProcessing).Find(&courses)
	for _, c := range courses {
		id, owner := *c.CourseId, c.UserId
		if c.OrganiseMore {
			s.enqueue(func() { s.extend(id, owner) })
		} else {
			s.enqueue(func() { s.organise(id, owner) })
		}
	}
	var refCourses []coursemodels.Course
	s.db.Where("references_status = ?", StatusProcessing).Find(&refCourses)
	for _, c := range refCourses {
		id, owner := *c.CourseId, c.UserId
		s.enqueue(func() { s.findReferences(id, owner, false) })
	}
	tests := s.resumeTests()
	if len(mats)+len(courses)+len(refCourses)+tests > 0 {
		slog.Info("resumed AI jobs", "materials", len(mats), "courses", len(courses), "tests", tests)
	}
}

func (s *Service) logUsage(task string, courseID, userID int, u Usage, started time.Time, err error) {
	row := AIUsage{
		Task: task, Provider: u.Provider, Model: u.Model, CourseID: courseID, UserID: userID,
		InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CostMicroUSD: u.CostMicroUSD,
		DurationMS: time.Since(started).Milliseconds(), OK: err == nil,
	}
	if row.Provider == "" {
		row.Provider = "none"
	}
	if row.Model == "" {
		row.Model = "none"
	}
	if err != nil {
		row.Error = err.Error()
	}
	s.db.Create(&row)
}

// ── courses ──────────────────────────────────────────────────────────────

var errNotFound = errors.New("not found")

func (s *Service) course(id int) (*coursemodels.Course, error) {
	var c coursemodels.Course
	if err := s.db.Where("course_id = ?", id).First(&c).Error; err != nil {
		return nil, errNotFound
	}
	return &c, nil
}

// CanCreateCourse enforces the plan's course count (admins are exempt).
func (s *Service) CanCreateCourse(userID int, isAdmin bool) (bool, string) {
	lim := s.limitsFor(userID)
	if isAdmin || lim.CoursesPerUser <= 0 {
		return true, ""
	}
	var n int64
	s.db.Model(&coursemodels.Course{}).Where("user_id = ?", userID).Count(&n)
	if int(n) >= lim.CoursesPerUser {
		return false, fmt.Sprintf("%s includes %d courses. Delete one to create another%s.", planLabel(lim), lim.CoursesPerUser, upgradeHint(lim))
	}
	if lim := lim.NewCoursesPerDay; lim > 0 {
		day, next, _, _ := allowanceWindows(time.Now())
		var today int64
		s.db.Model(&coursemodels.Course{}).Where("user_id = ? AND created_at >= ?", userID, day).Count(&today)
		if int(today) >= lim {
			return false, "You can start one new course a day, so the AI has time to build it. Your next course can be started in " +
				untilText(time.Until(next)) + ", when your AI allowance refills."
		}
	}
	return true, ""
}

// ── upload tokens: "<userID>.<unix expiry>.<hmac>", bound to one course ───

func (s *Service) signUpload(courseID, userID int, exp int64) string {
	m := hmac.New(sha256.New, s.appKey)
	fmt.Fprintf(m, "upload|%d|%d|%d", courseID, userID, exp)
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Service) UploadToken(courseID, userID int, exp time.Time) string {
	e := exp.Unix()
	return fmt.Sprintf("%d.%d.%s", userID, e, s.signUpload(courseID, userID, e))
}

// CheckUploadToken returns the user the token was issued to, if it is valid
// for this course and not expired.
func (s *Service) CheckUploadToken(token string, courseID int) (int, bool) {
	p := strings.Split(token, ".")
	if len(p) != 3 {
		return 0, false
	}
	userID, err1 := strconv.Atoi(p[0])
	exp, err2 := strconv.ParseInt(p[1], 10, 64)
	if err1 != nil || err2 != nil || time.Now().Unix() > exp {
		return 0, false
	}
	if !hmac.Equal([]byte(p[2]), []byte(s.signUpload(courseID, userID, exp))) {
		return 0, false
	}
	return userID, true
}

// ── materials ────────────────────────────────────────────────────────────

func (s *Service) materialCount(courseID int) int {
	var n int64
	s.db.Model(&Material{}).Where("course_id = ?", courseID).Count(&n)
	return int(n)
}

// AddFile stores an uploaded PDF or .pptx and queues its digest.
func (s *Service) AddFile(ctx context.Context, c *coursemodels.Course, ownerID int, kind, title, filename, mime string, data []byte) (*Material, error) {
	m := &Material{CourseID: *c.CourseId, OwnerID: ownerID, Kind: kind, Title: title, MimeType: mime,
		SizeBytes: int64(len(data)), Status: StatusProcessing}
	if kind == KindSlides {
		text, slides, err := ExtractPptxText(data)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(text) == "" && !slidesMode(c) {
			return nil, errors.New("these slides contain no text to read (images-only slides are not supported yet)")
		}
		m.Text = text
		_ = slides
	}
	if err := s.db.Create(m).Error; err != nil {
		return nil, err
	}
	ext := map[string]string{KindPDF: ".pdf", KindSlides: ".pptx"}[kind]
	key := fmt.Sprintf("courses/%d/materials/%d%s", m.CourseID, m.ID, ext)
	url, err := storage.UploadKey(ctx, key, data, mime)
	if err != nil {
		s.db.Delete(m)
		return nil, fmt.Errorf("could not store the file: %w", err)
	}
	m.URL, m.StorageKey = url, key
	s.db.Model(m).Updates(map[string]any{"url": url, "storage_key": key})
	if kind == KindSlides {
		// Pages for "slides as written" (F3.1). Not fatal: they are built
		// again from the stored file when first asked for.
		if _, err := s.buildSlidePages(ctx, m, data); err != nil {
			slog.Warn("slide pages not built", "material", m.ID, "error", err)
		}
	}

	if slidesMode(c) {
		if kind == KindPDF {
			if n := pdfPageCount(data); n > 0 {
				s.db.Model(m).Update("total_pages", n)
				m.TotalPages = n
			}
		}
		s.markReadyWithoutAI(m) // slides as written: no AI reading
		return m, nil
	}
	id := m.ID
	if kind == KindPDF {
		s.enqueue(func() { s.digest(id, data) })
	} else {
		s.enqueue(func() { s.digest(id, nil) })
	}
	return m, nil
}

// AddLink saves a video or web link. Its digest is its title and note, so it
// is ready at once (no AI call).
func (s *Service) AddLink(c *coursemodels.Course, ownerID int, kind, title, url, youtubeID, note string) (*Material, error) {
	summary := strings.TrimSpace(note)
	if summary == "" {
		summary = title
	}
	m := &Material{CourseID: *c.CourseId, OwnerID: ownerID, Kind: kind, Title: title, URL: url,
		YouTubeID: youtubeID, Status: StatusReady, Text: note, Digest: Digest{Summary: summary, Topics: []string{}, KeyPoints: []string{}}}
	if kind == KindVideo && s.videoAI != nil && !slidesMode(c) {
		m.Status = StatusProcessing // Gemini watches it in the background
	}
	if err := s.db.Create(m).Error; err != nil {
		return nil, err
	}
	if m.Status == StatusProcessing {
		id := m.ID
		s.enqueue(func() { s.digest(id, nil) })
	}
	return m, nil
}

// partlyRead: a long PDF or video whose reading stopped before the end.
func partlyRead(m *Material) bool {
	return m.Status == StatusReady && !m.Reading &&
		((m.ReadPages > 0 && m.ReadPages < m.TotalPages) || (m.ReadSeconds > 0 && m.ReadSeconds < m.TotalSeconds))
}

func (s *Service) Retry(m *Material) {
	if partlyRead(m) {
		s.db.Model(m).Updates(map[string]any{"reading": true, "paused": false, "keep_reading": true, "error": ""})
	} else {
		s.db.Model(m).Updates(map[string]any{"status": StatusProcessing, "error": "", "read_pages": 0, "read_seconds": 0, "reading": false, "paused": false, "keep_reading": true})
	}
	id := m.ID
	s.enqueue(func() { s.digest(id, nil) })
}

func (s *Service) DeleteMaterial(ctx context.Context, m *Material) error {
	var pictures []string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("material_id = ?", m.ID).Delete(&UnitMaterial{}).Error; err != nil {
			return err
		}
		var err error
		if pictures, err = deleteSlidePages(tx, m.ID); err != nil {
			return err
		}
		return tx.Delete(m).Error
	})
	if err == nil {
		storage.DeleteKey(ctx, m.StorageKey)
		for _, k := range pictures {
			storage.DeleteKey(ctx, k)
		}
	}
	return err
}

// digest runs in a worker. data is the PDF when the caller still has it.
// A PDF longer than PartPages is read one part at a time: after each part
// the material is ready with what was read so far, and the next part is
// queued (see digestPart).
func (s *Service) digest(materialID int, data []byte) {
	var m Material
	if err := s.db.First(&m, materialID).Error; err != nil {
		return
	}
	if s.courseInSlidesMode(m.CourseID) {
		s.markReadyWithoutAI(&m) // slides as written: no AI reading
		return
	}
	defer s.afterRead(m.CourseID)
	if m.Status == StatusProcessing && !m.KeepReading && s.shouldWait(&m) {
		s.db.Model(&m).Updates(map[string]any{"status": StatusPaused, "error": waitMessage})
		return
	}
	ctx := context.Background()
	in := DigestInput{Kind: m.Kind, Title: m.Title, Text: m.Text}
	if m.Kind == KindPDF {
		if data == nil {
			var err error
			if data, err = storage.Get(ctx, m.StorageKey, s.limits.MaxFileBytes); err != nil {
				s.failMaterial(&m, "Could not read the stored file. Please upload it again.")
				return
			}
		}
		in.PDF = data
		pages := pdfPageCount(data)
		if first := s.limits.partPages(true); first > 0 && pages > first && s.digestPart(&m, data, pages) {
			return
		}
		m.TotalPages = pages // short, or cannot be split: read in one piece
	}
	if err := s.checkAI(m.OwnerID); err != nil {
		slog.Warn("AI budget or allowance reached: digest skipped", "material", m.ID)
		s.failMaterial(&m, limitMessage(err))
		return
	}
	started := time.Now()
	var d Digest
	var usage Usage
	var err error
	task := "digest"
	if m.Kind == KindVideo && s.videoAI != nil && s.videoLength != nil && s.limits.VideoPartSeconds > 0 {
		total := m.TotalSeconds
		if total == 0 {
			if n, err := s.videoLength(ctx, m.YouTubeID); err != nil {
				slog.Warn("video length", "material", m.ID, "error", err)
			} else {
				total = n
			}
		}
		if first := s.limits.videoPart(true); total > first+first/2 {
			s.digestVideoPart(&m, total)
			return
		}
	}
	if m.Kind == KindVideo {
		task = "video_digest"
		if s.videoAI == nil {
			err = ErrAIUnavailable
		} else {
			d, usage, err = s.videoAI.DigestVideo(ctx, m.YouTubeID, m.Title, m.Text)
		}
	} else {
		d, usage, err = s.ai.Digest(ctx, in)
	}
	s.logUsage(task, m.CourseID, m.OwnerID, usage, started, err)
	if err != nil {
		slog.Warn("digest failed", "material", m.ID, "error", err)
		if s.waitForAI(&m, err) {
			return
		}
		s.failMaterial(&m, digestErrorMessage(m.Kind, err))
		return
	}
	cleanDigest(&d, 0)
	// Struct update (not a map) so the JSON serializer encodes the digest
	m.Status, m.Error, m.Digest, m.ReadPages, m.Reading = StatusReady, "", d, m.TotalPages, false
	if err := s.db.Model(&m).Select("status", "error", "digest", "total_pages", "read_pages", "reading").Updates(&m).Error; err != nil {
		slog.Error("save digest", "material", m.ID, "error", err)
		s.failMaterial(&m, "Could not save what the AI read. Please retry.")
	}
}

func digestErrorMessage(kind string, err error) string {
	switch {
	case errors.Is(err, ErrAIUnavailable):
		return "AI reading is not switched on yet on this server."
	case errors.Is(err, ErrAICredit):
		return creditMessage
	case errors.Is(err, ErrBudget), errors.Is(err, ErrAllowance):
		return limitMessage(err)
	case kind == KindVideo:
		return "The AI could not watch this video (it may be private, age-restricted, too long, or the daily video limit was reached). Try again later."
	}
	return "The AI could not read this material. Try again in a minute."
}

// cleanDigest fills empty lists and moves page numbers by offset (the pages
// before this part).
func cleanDigest(d *Digest, offset int) {
	if d.Topics == nil {
		d.Topics = []string{}
	}
	if d.KeyPoints == nil {
		d.KeyPoints = []string{}
	}
	for i := range d.Concepts {
		pages := d.Concepts[i].Pages
		for j := range pages {
			pages[j] += offset
		}
		d.Concepts[i].Pages = mergeInts(nil, pages)
		d.Concepts[i].Slides = mergeInts(nil, d.Concepts[i].Slides)
		d.Concepts[i].Organised = false
	}
}

// digestPart reads the next part of a long PDF and adds it to the digest.
// It returns false only when the first part cannot be cut out of the file;
// the caller then reads the whole file in one piece.
func (s *Service) digestPart(m *Material, data []byte, total int) bool {
	from := m.ReadPages + 1
	if from > total {
		s.db.Model(m).Updates(map[string]any{"reading": false, "status": StatusReady})
		return true
	}
	first := m.ReadPages == 0
	to := from + s.limits.partPages(first) - 1
	if to > total {
		to = total
	}
	// A later part that fails keeps what was read; Retry continues from here
	partFail := func(msg string) {
		if first {
			s.failMaterial(m, msg)
			return
		}
		s.db.Model(m).Updates(map[string]any{"reading": false, "status": StatusReady,
			"error": fmt.Sprintf("Pages %d to %d of %d were not read yet: %s", from, to, total, msg)})
	}
	part, err := pdfPages(data, from, to)
	if err != nil {
		slog.Warn("split pdf", "material", m.ID, "error", err)
		if first {
			return false
		}
		partFail("This PDF could not be split into parts. Try saving it again as PDF and uploading it.")
		return true
	}
	if err := s.checkAI(m.OwnerID); err != nil {
		partFail(limitMessage(err))
		return true
	}
	if !first && !m.KeepReading && !s.canReadMore(m.OwnerID) {
		s.pauseReading(m)
		return true
	}
	started := time.Now()
	d, usage, err := s.ai.Digest(context.Background(), DigestInput{Kind: KindPDF, Title: m.Title, PDF: part,
		FromPage: from, ToPage: to, TotalPages: total})
	s.logUsage("digest", m.CourseID, m.OwnerID, usage, started, err)
	if err != nil {
		slog.Warn("digest part failed", "material", m.ID, "pages", fmt.Sprintf("%d-%d", from, to), "error", err)
		if !s.waitForAI(m, err) {
			partFail(digestErrorMessage(KindPDF, err))
		}
		return true
	}
	cleanDigest(&d, from-1)
	more := to < total
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var cur Material
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&cur, m.ID).Error; err != nil {
			return err
		}
		cur.Digest = mergeDigests(cur.Digest, d, first)
		cur.Status, cur.Error, cur.TotalPages, cur.ReadPages, cur.Reading = StatusReady, "", total, to, more
		*m = cur
		return tx.Model(&cur).Select("status", "error", "digest", "total_pages", "read_pages", "reading").Updates(&cur).Error
	})
	if err != nil {
		slog.Error("save digest part", "material", m.ID, "error", err)
		partFail("Could not save what the AI read. Please retry.")
		return true
	}
	// (digest's afterRead gives organised courses units for these pages)
	if more {
		id := m.ID
		s.enqueue(func() { s.digest(id, data) })
	}
	return true
}

// digestVideoPart watches the next stretch of a long video and adds what it
// finds, telling the AI which concepts earlier parts already found.
func (s *Service) digestVideoPart(m *Material, total int) {
	from := m.ReadSeconds
	first := from == 0
	size := s.limits.videoPart(first)
	to := from + size
	if total-to < size/2 {
		to = total // no tiny last part
	}
	partFail := func(msg string) {
		if first {
			s.failMaterial(m, msg)
			return
		}
		s.db.Model(m).Updates(map[string]any{"reading": false, "status": StatusReady,
			"error": fmt.Sprintf("%s to %s of %s was not watched yet: %s", clock(from), clock(to), clock(total), msg)})
	}
	if err := s.checkAI(m.OwnerID); err != nil {
		partFail(limitMessage(err))
		return
	}
	if !first && !m.KeepReading && !s.canReadMore(m.OwnerID) {
		s.pauseReading(m)
		return
	}
	part := VideoPart{From: from, To: to, Total: total}
	for _, c := range m.Digest.Concepts {
		part.Earlier = append(part.Earlier, c.Name)
	}
	if n := len(part.Earlier); n > 80 {
		part.Earlier = part.Earlier[n-80:] // the most recent ones matter most
	}
	started := time.Now()
	d, usage, err := s.videoAI.DigestVideoPart(context.Background(), m.YouTubeID, m.Title, m.Text, part)
	s.logUsage("video_digest", m.CourseID, m.OwnerID, usage, started, err)
	if err != nil {
		slog.Warn("video part failed", "material", m.ID, "from", from, "to", to, "error", err)
		if !s.waitForAI(m, err) {
			partFail(digestErrorMessage(KindVideo, err))
		}
		return
	}
	cleanDigest(&d, 0)
	more := to < total
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var cur Material
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&cur, m.ID).Error; err != nil {
			return err
		}
		cur.Digest = mergeDigests(cur.Digest, d, first)
		cur.Status, cur.Error, cur.TotalSeconds, cur.ReadSeconds, cur.Reading = StatusReady, "", total, to, more
		*m = cur
		return tx.Model(&cur).Select("status", "error", "digest", "total_seconds", "read_seconds", "reading").Updates(&cur).Error
	})
	if err != nil {
		slog.Error("save video part", "material", m.ID, "error", err)
		partFail("Could not save what the AI watched. Please retry.")
		return
	}
	if more {
		id := m.ID
		s.enqueue(func() { s.digest(id, nil) })
	}
}

// mergeDigests adds a newly read part to what was read before.
func mergeDigests(old, part Digest, first bool) Digest {
	if first {
		return part
	}
	if old.Summary == "" {
		old.Summary = part.Summary
	}
	old.Topics = appendNew(old.Topics, part.Topics, 60)
	old.KeyPoints = appendNew(old.KeyPoints, part.KeyPoints, 80)
	old.Concepts = append(old.Concepts, part.Concepts...)
	return old
}

func appendNew(a, b []string, max int) []string {
	seen := map[string]bool{}
	for _, v := range a {
		seen[strings.ToLower(v)] = true
	}
	for _, v := range b {
		if k := strings.ToLower(v); !seen[k] && len(a) < max {
			seen[k] = true
			a = append(a, v)
		}
	}
	return a
}

func (s *Service) failMaterial(m *Material, msg string) {
	s.db.Model(m).Updates(map[string]any{"status": StatusFailed, "error": msg})
}

// ── organise (F1.4) ──────────────────────────────────────────────────────

// StartOrganise checks the rules and queues the AI run.
func (s *Service) StartOrganise(c *coursemodels.Course) error {
	if slidesMode(c) {
		return errSlidesMode
	}
	if c.CourseStatus == "published" && len(s.units(*c.CourseId)) > 0 {
		return errors.New("Unpublish the course before organising it again (learners' progress depends on its units).")
	}
	if c.OrganiseStatus == StatusProcessing {
		return errors.New("The course is already being organised.")
	}
	if err := s.checkAI(c.UserId); err != nil {
		return errors.New(limitMessage(err))
	}
	if lim := s.limitsFor(c.UserId); lim.OrganiseRunsPerCourse > 0 && c.OrganiseRuns >= lim.OrganiseRunsPerCourse {
		return fmt.Errorf("%s allows %d AI organise runs per course. You can still edit the units by hand.", planLabel(lim), lim.OrganiseRunsPerCourse)
	}
	var processing, ready int64
	s.db.Model(&Material{}).Where("course_id = ? AND status = ?", *c.CourseId, StatusProcessing).Count(&processing)
	s.db.Model(&Material{}).Where("course_id = ? AND status = ?", *c.CourseId, StatusReady).Count(&ready)
	if processing > 0 {
		return errors.New("Some materials are still being read. Wait until they are ready.")
	}
	if ready == 0 {
		return errors.New("Add at least one material (and wait until it is ready) before organising.")
	}
	res := s.db.Model(&coursemodels.Course{}).
		Where("course_id = ? AND organise_status <> ?", *c.CourseId, StatusProcessing).
		Updates(map[string]any{"organise_status": StatusProcessing, "organise_more": false, "organise_error": "", "organise_runs": gorm.Expr("organise_runs + 1"),
			"organise_done": 0, "organise_total": 0})
	if res.RowsAffected == 0 {
		return errors.New("The course is already being organised.")
	}
	id, owner := *c.CourseId, c.UserId
	s.enqueue(func() { s.organise(id, owner) })
	return nil
}

func (s *Service) organise(courseID, ownerID int) {
	c, err := s.course(courseID)
	if err != nil {
		return
	}
	var mats []Material
	s.db.Where("course_id = ? AND status = ?", courseID, StatusReady).Order(materialOrder).Find(&mats)
	in, refs, taken := organiseInput(c, mats, false)
	in.Progress = s.organiseProgress(courseID)
	// First build: each unit is saved as soon as it is written
	var saver *unitSaver
	if len(s.units(courseID)) == 0 {
		saver = s.newUnitSaver(courseID, 0, refs)
		in.OnUnit = saver.add
	}

	started := time.Now()
	outline, usage, err := s.ai.Organise(context.Background(), in)
	s.logUsage("organise", courseID, ownerID, usage, started, err)
	if err == nil && len(outline.Units) == 0 {
		err = errors.New("the AI proposed no units")
	}
	if err == nil && saver != nil && saver.saved() < len(outline.Units) {
		err = fmt.Errorf("only %d of %d units saved: %v", saver.saved(), len(outline.Units), saver.err)
	}
	if err != nil && saver != nil && saver.saved() > 0 {
		// Keep the units already written; the rest is added later
		slog.Warn("organise stopped part way", "course", courseID, "saved", saver.saved(), "error", err)
		s.markRefsOrganised(saver.cited)
		s.organiseDone(courseID, ownerID, partialMessage)
		return
	}
	if err != nil {
		slog.Warn("organise failed", "course", courseID, "error", err)
		msg := "The AI could not organise this course. Try again in a minute; this run was not counted."
		if errors.Is(err, ErrAIUnavailable) {
			msg = "AI organising is not switched on yet on this server."
		} else if errors.Is(err, ErrAICredit) {
			msg = creditMessage
		} else if errors.Is(err, ErrAllKeysResting) {
			msg = aiBusyOrganise // resumePaused starts it again
		}
		// A failed run does not use up the creator's allowance
		s.db.Model(&coursemodels.Course{}).Where("course_id = ?", courseID).Updates(map[string]any{
			"organise_status": StatusFailed, "organise_error": msg, "organise_runs": gorm.Expr("GREATEST(organise_runs - 1, 0)")})
		return
	}
	if saver != nil {
		s.markOrganised(taken)
		s.organiseDone(courseID, ownerID, "")
		return
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.deleteUnits(tx, courseID); err != nil {
			return err
		}
		return saveUnits(tx, courseID, 0, outline, refs)
	})
	if err != nil {
		slog.Error("save organised units", "course", courseID, "error", err)
		s.db.Model(&coursemodels.Course{}).Where("course_id = ?", courseID).
			Updates(map[string]any{"organise_status": StatusFailed, "organise_error": "Could not save the units. Please try again."})
		return
	}
	s.markOrganised(taken)
	s.organiseDone(courseID, ownerID, "")
}

// organiseDone ends a run with new units: references are searched for them
// (started in the same update, so the course never looks finished in
// between) and pages read meanwhile get units of their own.
func (s *Service) organiseDone(courseID, ownerID int, msg string) {
	done := map[string]any{"organise_status": StatusReady, "organise_error": msg}
	if s.refs != nil {
		done["references_status"], done["references_done"], done["references_total"] = StatusProcessing, 0, 0
	}
	s.db.Model(&coursemodels.Course{}).Where("course_id = ?", courseID).Updates(done)
	if s.refs != nil {
		s.enqueue(func() { s.findReferences(courseID, ownerID, false) })
	}
	if msg == "" {
		s.maybeExtend(courseID)
	}
}

// saveUnits creates the outline's units from position start+1 on, with
// their concepts and the materials those concepts cite.
func saveUnits(tx *gorm.DB, courseID, start int, outline Outline, refs map[string]ConceptSource) error {
	for i, ou := range outline.Units {
		u := Unit{CourseID: courseID, Position: start + i + 1, Title: strings.TrimSpace(ou.Title), Body: ou.Intro, Source: "ai"}
		if u.Title == "" {
			u.Title = fmt.Sprintf("Unit %d", start+i+1)
		}
		if err := tx.Create(&u).Error; err != nil {
			return err
		}
		var unitMats []int // materials cited by this unit's concepts, first-seen order
		cited := map[int]bool{}
		for j, oc := range ou.Concepts {
			sources := resolveSources(oc.Sources, refs)
			for _, src := range sources {
				if !cited[src.MaterialID] {
					cited[src.MaterialID] = true
					unitMats = append(unitMats, src.MaterialID)
				}
			}
			title := strings.TrimSpace(oc.Title)
			if title == "" {
				continue
			}
			if err := tx.Create(&Concept{UnitID: u.ID, Position: j + 1, Title: clip(title, 255),
				Explanation: oc.Explanation, Sources: sources}).Error; err != nil {
				return err
			}
		}
		for k, mid := range unitMats {
			if err := tx.Create(&UnitMaterial{UnitID: u.ID, MaterialID: mid, Position: k + 1}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// organiseInput gives each material a key ("M12") and each of its concepts
// a key ("M12-C3"), and remembers where every key points.
// onlyNew leaves out concepts already organised (keys stay the same).
// taken is how many concepts of each material the input covers.
func organiseInput(c *coursemodels.Course, mats []Material, onlyNew bool) (OrganiseInput, map[string]ConceptSource, map[int]int) {
	in := OrganiseInput{CourseTitle: c.CourseTitle, CourseDescription: c.CourseDescription, Language: c.CourseLanguage}
	refs := map[string]ConceptSource{}
	taken := map[int]int{}
	for _, m := range mats {
		taken[m.ID] = len(m.Digest.Concepts)
		mref := fmt.Sprintf("M%d", m.ID)
		refs[mref] = ConceptSource{MaterialID: m.ID}
		om := OrganiseMaterial{Ref: mref, Kind: m.Kind, Title: m.Title, Summary: m.Digest.Summary}
		for i, sc := range m.Digest.Concepts {
			cref := fmt.Sprintf("%s-C%d", mref, i+1)
			refs[cref] = ConceptSource{MaterialID: m.ID, Pages: sc.Pages, Slides: sc.Slides, StartSeconds: sc.StartSeconds}
			if onlyNew && sc.Organised {
				continue
			}
			om.Concepts = append(om.Concepts, OrganiseConcept{Ref: cref, Name: sc.Name, Summary: sc.Summary, Details: sc.Details})
		}
		if onlyNew && len(om.Concepts) == 0 {
			continue // nothing new in this material
		}
		in.Materials = append(in.Materials, om)
	}
	return in, refs, taken
}

// resolveSources turns the AI's reference keys into locations. Unknown keys
// (made up by the AI) are dropped; several keys into one material merge.
func resolveSources(keys []string, refs map[string]ConceptSource) []ConceptSource {
	byMaterial := map[int]*ConceptSource{}
	var order []int
	for _, k := range keys {
		src, ok := refs[strings.ToUpper(strings.TrimSpace(k))]
		if !ok {
			continue
		}
		cur, seen := byMaterial[src.MaterialID]
		if !seen {
			cp := ConceptSource{MaterialID: src.MaterialID}
			cur = &cp
			byMaterial[src.MaterialID] = cur
			order = append(order, src.MaterialID)
		}
		cur.Pages = mergeInts(cur.Pages, src.Pages)
		cur.Slides = mergeInts(cur.Slides, src.Slides)
		if src.StartSeconds != nil && (cur.StartSeconds == nil || *src.StartSeconds < *cur.StartSeconds) {
			v := *src.StartSeconds // earliest moment wins
			cur.StartSeconds = &v
		}
	}
	out := make([]ConceptSource, 0, len(order))
	for _, id := range order {
		out = append(out, *byMaterial[id])
	}
	return out
}

func mergeInts(a, b []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, v := range append(append([]int{}, a...), b...) {
		if v > 0 && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

func (s *Service) deleteUnits(tx *gorm.DB, courseID int) error {
	// Quizzes stay (their questions are the teacher's work); they move after
	// the new lessons, which the creator can reorder.
	if err := tx.Model(&Unit{}).Where("course_id = ? AND kind = ?", courseID, "quiz").
		Update("position", gorm.Expr("position + 10000")).Error; err != nil {
		return err
	}
	var ids []int
	tx.Model(&Unit{}).Where("course_id = ? AND kind <> ?", courseID, "quiz").Pluck("id", &ids)
	if len(ids) == 0 {
		return nil
	}
	if err := tx.Where("unit_id IN ?", ids).Delete(&UnitMaterial{}).Error; err != nil {
		return err
	}
	if err := deleteTests(tx, ids); err != nil {
		return err
	}
	if err := tx.Where("unit_id IN ?", ids).Delete(&Concept{}).Error; err != nil {
		return err
	}
	if err := tx.Where("unit_id IN ?", ids).Delete(&UnitProgress{}).Error; err != nil {
		return err
	}
	return tx.Where("id IN ?", ids).Delete(&Unit{}).Error
}

// ── units and progress (F1.5, F1.7) ──────────────────────────────────────

func (s *Service) units(courseID int) []Unit {
	var us []Unit
	s.db.Where("course_id = ?", courseID).Order("position, id").Find(&us)
	return us
}

// unitMaterials returns each unit's materials, in order.
func (s *Service) unitMaterials(unitIDs []int) map[int][]Material {
	out := map[int][]Material{}
	if len(unitIDs) == 0 {
		return out
	}
	var links []UnitMaterial
	s.db.Where("unit_id IN ?", unitIDs).Order("unit_id, position").Find(&links)
	var mids []int
	for _, l := range links {
		mids = append(mids, l.MaterialID)
	}
	var mats []Material
	if len(mids) > 0 {
		s.db.Where("id IN ?", mids).Find(&mats)
	}
	byID := map[int]Material{}
	for _, m := range mats {
		byID[m.ID] = m
	}
	for _, l := range links {
		if m, ok := byID[l.MaterialID]; ok {
			out[l.UnitID] = append(out[l.UnitID], m)
		}
	}
	return out
}

// completedUnits is the set of unit IDs the user finished in a course.
func (s *Service) completedUnits(userID, courseID int) map[int]bool {
	var ids []int
	s.db.Model(&UnitProgress{}).Where("user_id = ? AND course_id = ?", userID, courseID).Pluck("unit_id", &ids)
	done := map[int]bool{}
	for _, id := range ids {
		done[id] = true
	}
	return done
}

// unlocked: unit 1 is open; each next unit opens when every earlier one is done.
func unlocked(units []Unit, done map[int]bool) map[int]bool {
	open := map[int]bool{}
	sorted := append([]Unit(nil), units...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Position < sorted[j].Position })
	allBefore := true
	for _, u := range sorted {
		if allBefore {
			open[u.ID] = true
		}
		if !done[u.ID] && !u.Optional { // practice or empty quizzes never block
			allBefore = false
		}
	}
	return open
}

func (s *Service) isEnrolled(userID, courseID int) bool {
	var n int64
	s.db.Model(&coursemodels.Enrollment{}).Where("user_id = ? AND course_id = ?", userID, courseID).Count(&n)
	return n > 0
}

// Complete marks a unit done if it is open for the learner.
func (s *Service) Complete(userID int, u *Unit) error {
	units := s.units(u.CourseID)
	if !unlocked(units, s.completedUnits(userID, u.CourseID))[u.ID] {
		return errors.New("Finish the previous unit first.")
	}
	if s.activeTests([]int{u.ID})[u.ID] {
		return errors.New("Pass the unit test to finish this unit.")
	}
	if err := s.db.Where(UnitProgress{UserID: userID, UnitID: u.ID}).
		Attrs(UnitProgress{CourseID: u.CourseID, CompletedAt: time.Now()}).
		FirstOrCreate(&UnitProgress{}).Error; err != nil {
		return err
	}
	s.maybeCertify(userID, u.CourseID)
	return nil
}

// covers returns a card image per course: the first YouTube video's thumbnail
// (courses with an uploaded cover use course_thumb instead; the frontend picks).
func (s *Service) covers(courseIDs []int) map[int]string {
	out := map[int]string{}
	if len(courseIDs) == 0 {
		return out
	}
	var rows []Material
	s.db.Select("course_id, you_tube_id").
		Where("course_id IN ? AND kind = ? AND you_tube_id <> ''", courseIDs, "video").
		Order("course_id, position, id").Find(&rows)
	for _, m := range rows {
		if _, ok := out[m.CourseID]; !ok {
			out[m.CourseID] = "https://i.ytimg.com/vi/" + m.YouTubeID + "/hqdefault.jpg"
		}
	}
	return out
}
