package learn

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	coursemodels "focusflow-api/internal/course/models"
	"focusflow-api/internal/platform/storage"

	"gorm.io/gorm"
)

// Limits are the free-tier settings (FEATURES.md section 7).
type Limits struct {
	CoursesPerUser        int   `json:"courses_per_user"`
	MaterialsPerCourse    int   `json:"materials_per_course"`
	MaxFileBytes          int64 `json:"max_file_bytes"`
	OrganiseRunsPerCourse int   `json:"organise_runs_per_course"`
}

var DefaultLimits = Limits{CoursesPerUser: 2, MaterialsPerCourse: 15, MaxFileBytes: 20 << 20, OrganiseRunsPerCourse: 3}

type Service struct {
	db      *gorm.DB
	ai      AI
	videoAI VideoAI  // nil: videos are described by their title and note only
	refs    Enricher // nil: no "expand your knowledge" references
	limits  Limits
	appKey  []byte // signs upload tokens
	jobs    chan func()
}

// NewService starts `workers` background workers for AI jobs. AI calls never
// run inside a request: the request saves a status and returns.
func NewService(db *gorm.DB, ai AI, limits Limits, appKey string, workers int) *Service {
	s := &Service{db: db, ai: ai, limits: limits, appKey: []byte(appKey), jobs: make(chan func(), 256)}
	for i := 0; i < workers; i++ {
		go func() {
			for job := range s.jobs {
				job()
			}
		}()
	}
	return s
}

func (s *Service) enqueue(job func()) {
	select {
	case s.jobs <- job:
	default:
		// Queue full: run on its own goroutine rather than drop the work
		go job()
	}
}

// SetVideoAI turns on video reading (Gemini).
func (s *Service) SetVideoAI(v VideoAI) { s.videoAI = v }

// SetEnricher turns on reference finding (web search).
func (s *Service) SetEnricher(e Enricher) { s.refs = e }

// StartReferences queues the reference search for a course's concepts.
func (s *Service) StartReferences(c *coursemodels.Course) error {
	if s.refs == nil {
		return errors.New("Finding references is not switched on yet on this server.")
	}
	res := s.db.Model(&coursemodels.Course{}).Where("course_id = ? AND references_status <> ?", *c.CourseId, StatusProcessing).
		Update("references_status", StatusProcessing)
	if res.RowsAffected == 0 {
		return errors.New("References are already being found.")
	}
	id, owner := *c.CourseId, c.UserId
	s.enqueue(func() { s.findReferences(id, owner) })
	return nil
}

// findReferences runs unit by unit, so one failure does not lose the rest.
func (s *Service) findReferences(courseID, ownerID int) {
	c, err := s.course(courseID)
	if err != nil {
		return
	}
	failed := 0
	for _, u := range s.units(courseID) {
		var concepts []Concept
		s.db.Where("unit_id = ?", u.ID).Order("position, id").Find(&concepts)
		if len(concepts) == 0 {
			continue
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
	s.db.Model(&coursemodels.Course{}).Where("course_id = ?", courseID).Update("references_status", status)
}

// ResumePending re-queues work that was interrupted by a restart.
func (s *Service) ResumePending() {
	var mats []Material
	s.db.Where("status = ?", StatusProcessing).Find(&mats)
	for _, m := range mats {
		id := m.ID
		s.enqueue(func() { s.digest(id, nil) })
	}
	var courses []coursemodels.Course
	s.db.Where("organise_status = ?", StatusProcessing).Find(&courses)
	for _, c := range courses {
		id, owner := *c.CourseId, c.UserId
		s.enqueue(func() { s.organise(id, owner) })
	}
	var refCourses []coursemodels.Course
	s.db.Where("references_status = ?", StatusProcessing).Find(&refCourses)
	for _, c := range refCourses {
		id, owner := *c.CourseId, c.UserId
		s.enqueue(func() { s.findReferences(id, owner) })
	}
	if len(mats)+len(courses)+len(refCourses) > 0 {
		slog.Info("resumed AI jobs", "materials", len(mats), "courses", len(courses))
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

// CanCreateCourse enforces the free-tier course count (admins are exempt).
func (s *Service) CanCreateCourse(userID int, isAdmin bool) (bool, string) {
	if isAdmin || s.limits.CoursesPerUser <= 0 {
		return true, ""
	}
	var n int64
	s.db.Model(&coursemodels.Course{}).Where("user_id = ?", userID).Count(&n)
	if int(n) >= s.limits.CoursesPerUser {
		return false, fmt.Sprintf("The free plan includes %d courses. Delete one to create another.", s.limits.CoursesPerUser)
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
		if strings.TrimSpace(text) == "" {
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
	if kind == KindVideo && s.videoAI != nil {
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

func (s *Service) Retry(m *Material) {
	s.db.Model(m).Updates(map[string]any{"status": StatusProcessing, "error": ""})
	id := m.ID
	s.enqueue(func() { s.digest(id, nil) })
}

func (s *Service) DeleteMaterial(ctx context.Context, m *Material) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("material_id = ?", m.ID).Delete(&UnitMaterial{}).Error; err != nil {
			return err
		}
		return tx.Delete(m).Error
	})
	if err == nil {
		storage.DeleteKey(ctx, m.StorageKey)
	}
	return err
}

// digest runs in a worker. data is the PDF when the caller still has it.
func (s *Service) digest(materialID int, data []byte) {
	var m Material
	if err := s.db.First(&m, materialID).Error; err != nil {
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
	}
	started := time.Now()
	var d Digest
	var usage Usage
	var err error
	task := "digest"
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
		msg := "The AI could not read this material. Try again in a minute."
		switch {
		case errors.Is(err, ErrAIUnavailable):
			msg = "AI reading is not switched on yet on this server."
		case m.Kind == KindVideo:
			msg = "The AI could not watch this video (it may be private, age-restricted, too long, or the daily video limit was reached). Try again later."
		}
		s.failMaterial(&m, msg)
		return
	}
	if d.Topics == nil {
		d.Topics = []string{}
	}
	if d.KeyPoints == nil {
		d.KeyPoints = []string{}
	}
	for i := range d.Concepts {
		d.Concepts[i].Pages = mergeInts(nil, d.Concepts[i].Pages)
		d.Concepts[i].Slides = mergeInts(nil, d.Concepts[i].Slides)
	}
	// Struct update (not a map) so the JSON serializer encodes the digest
	m.Status, m.Error, m.Digest = StatusReady, "", d
	if err := s.db.Model(&m).Select("status", "error", "digest").Updates(&m).Error; err != nil {
		slog.Error("save digest", "material", m.ID, "error", err)
		s.failMaterial(&m, "Could not save what the AI read. Please retry.")
	}
}

func (s *Service) failMaterial(m *Material, msg string) {
	s.db.Model(m).Updates(map[string]any{"status": StatusFailed, "error": msg})
}

// ── organise (F1.4) ──────────────────────────────────────────────────────

// StartOrganise checks the rules and queues the AI run.
func (s *Service) StartOrganise(c *coursemodels.Course) error {
	if c.CourseStatus == "published" {
		return errors.New("Unpublish the course before organising it again (learners' progress depends on its units).")
	}
	if c.OrganiseStatus == StatusProcessing {
		return errors.New("The course is already being organised.")
	}
	if s.limits.OrganiseRunsPerCourse > 0 && c.OrganiseRuns >= s.limits.OrganiseRunsPerCourse {
		return fmt.Errorf("The free plan allows %d AI organise runs per course. You can still edit the units by hand.", s.limits.OrganiseRunsPerCourse)
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
		Updates(map[string]any{"organise_status": StatusProcessing, "organise_error": "", "organise_runs": gorm.Expr("organise_runs + 1")})
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
	s.db.Where("course_id = ? AND status = ?", courseID, StatusReady).Order("id").Find(&mats)
	in, refs := organiseInput(c, mats)

	started := time.Now()
	outline, usage, err := s.ai.Organise(context.Background(), in)
	s.logUsage("organise", courseID, ownerID, usage, started, err)
	if err == nil && len(outline.Units) == 0 {
		err = errors.New("the AI proposed no units")
	}
	if err != nil {
		slog.Warn("organise failed", "course", courseID, "error", err)
		msg := "The AI could not organise this course. Try again in a minute; this run was not counted."
		if errors.Is(err, ErrAIUnavailable) {
			msg = "AI organising is not switched on yet on this server."
		}
		// A failed run does not use up the creator's allowance
		s.db.Model(&coursemodels.Course{}).Where("course_id = ?", courseID).Updates(map[string]any{
			"organise_status": StatusFailed, "organise_error": msg, "organise_runs": gorm.Expr("GREATEST(organise_runs - 1, 0)")})
		return
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.deleteUnits(tx, courseID); err != nil {
			return err
		}
		for i, ou := range outline.Units {
			u := Unit{CourseID: courseID, Position: i + 1, Title: strings.TrimSpace(ou.Title), Body: ou.Intro, Source: "ai"}
			if u.Title == "" {
				u.Title = fmt.Sprintf("Unit %d", i+1)
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
		return tx.Model(&coursemodels.Course{}).Where("course_id = ?", courseID).
			Updates(map[string]any{"organise_status": StatusReady, "organise_error": ""}).Error
	})
	if err != nil {
		slog.Error("save organised units", "course", courseID, "error", err)
		s.db.Model(&coursemodels.Course{}).Where("course_id = ?", courseID).
			Updates(map[string]any{"organise_status": StatusFailed, "organise_error": "Could not save the units. Please try again."})
		return
	}
	// New units: look for "expand your knowledge" references in the background
	if s.refs != nil {
		_ = s.StartReferences(c)
	}
}

// organiseInput gives each material a key ("M12") and each of its concepts
// a key ("M12-C3"), and remembers where every key points.
func organiseInput(c *coursemodels.Course, mats []Material) (OrganiseInput, map[string]ConceptSource) {
	in := OrganiseInput{CourseTitle: c.CourseTitle, CourseDescription: c.CourseDescription, Language: c.CourseLanguage}
	refs := map[string]ConceptSource{}
	for _, m := range mats {
		mref := fmt.Sprintf("M%d", m.ID)
		refs[mref] = ConceptSource{MaterialID: m.ID}
		om := OrganiseMaterial{Ref: mref, Kind: m.Kind, Title: m.Title, Summary: m.Digest.Summary}
		for i, sc := range m.Digest.Concepts {
			cref := fmt.Sprintf("%s-C%d", mref, i+1)
			refs[cref] = ConceptSource{MaterialID: m.ID, Pages: sc.Pages, Slides: sc.Slides, StartSeconds: sc.StartSeconds}
			om.Concepts = append(om.Concepts, OrganiseConcept{Ref: cref, Name: sc.Name, Summary: sc.Summary, Details: sc.Details})
		}
		in.Materials = append(in.Materials, om)
	}
	return in, refs
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
	var ids []int
	tx.Model(&Unit{}).Where("course_id = ?", courseID).Pluck("id", &ids)
	if len(ids) == 0 {
		return nil
	}
	if err := tx.Where("unit_id IN ?", ids).Delete(&UnitMaterial{}).Error; err != nil {
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
		if !done[u.ID] {
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
	return s.db.Where(UnitProgress{UserID: userID, UnitID: u.ID}).
		Attrs(UnitProgress{CourseID: u.CourseID, CompletedAt: time.Now()}).
		FirstOrCreate(&UnitProgress{}).Error
}
