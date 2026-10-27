package learn

import (
	"encoding/csv"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"focusflow-api/internal/auth"
	coursemodels "focusflow-api/internal/course/models"
	"focusflow-api/internal/platform/mailer"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Institutions (FEATURES.md M4: F4.3 to F4.5). A university or school has
// admins and lecturers. A lecturer attaches a course to the institution,
// brings in the class roster and invites the class. Learners give consent
// before their results are used; an institution admin approves results
// access per course. Class results show who struggles with which concept,
// and the lecturer sends each learner help they have reviewed.

const (
	OrgAdmin    = "admin"
	OrgLecturer = "lecturer"
	// ConsentVersion changes when the default consent text changes.
	ConsentVersion = "2026-10"
)

const defaultConsent = `Your teacher uses FocusFlow to see how the class is doing. If you agree, your quiz and test results in this course are shown to your teacher and to your institution's FocusFlow admins, so they can see which topics you find hard and send you help. Your name is never sent to an AI model. You can withdraw your consent at any time on the course page; your results then stop being shown.`

type Institution struct {
	ID          int       `json:"id" gorm:"primaryKey"`
	Name        string    `json:"name" gorm:"size:255;not null"`
	ConsentText string    `json:"consent_text" gorm:"type:text"` // empty = the default text
	Plan        string    `json:"plan" gorm:"size:16;not null;default:pilot"`
	CreatedBy   int       `json:"created_by" gorm:"not null"`
	CreatedAt   time.Time `json:"created_at"`
}

// InstitutionMember: an admin or lecturer. Email-only rows are people added
// before they have an account; they link on their first visit.
type InstitutionMember struct {
	ID            int       `json:"id" gorm:"primaryKey"`
	InstitutionID int       `json:"institution_id" gorm:"not null;uniqueIndex:idx_inst_email"`
	Email         string    `json:"email" gorm:"size:255;not null;uniqueIndex:idx_inst_email"`
	UserID        *int      `json:"user_id" gorm:"index"`
	Role          string    `json:"role" gorm:"size:16;not null"`
	CreatedAt     time.Time `json:"created_at"`
}

// CourseClass holds a course's institution settings.
type CourseClass struct {
	CourseID          int        `json:"course_id" gorm:"primaryKey"`
	InstitutionID     int        `json:"institution_id" gorm:"not null;index"`
	RosterOnly        bool       `json:"roster_only"` // only people on the roster can join
	ResultsApprovedBy *int       `json:"results_approved_by"`
	ResultsApprovedAt *time.Time `json:"results_approved_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// RosterEntry is one student of the class list.
type RosterEntry struct {
	ID        int        `json:"id" gorm:"primaryKey"`
	CourseID  int        `json:"course_id" gorm:"not null;uniqueIndex:idx_roster_email"`
	Email     string     `json:"email" gorm:"size:255;not null;uniqueIndex:idx_roster_email"`
	Name      string     `json:"name" gorm:"size:255"`
	StudentID string     `json:"student_id" gorm:"size:64"`
	InvitedAt *time.Time `json:"invited_at"`
	CreatedAt time.Time  `json:"created_at"`
}

// Consent records a learner's agreement to share results (F4.4).
type Consent struct {
	ID          int        `json:"id" gorm:"primaryKey"`
	CourseID    int        `json:"course_id" gorm:"not null;uniqueIndex:idx_consent"`
	UserID      int        `json:"user_id" gorm:"not null;uniqueIndex:idx_consent"`
	Version     string     `json:"version" gorm:"size:32"`
	AcceptedAt  time.Time  `json:"accepted_at"`
	WithdrawnAt *time.Time `json:"withdrawn_at"`
}

// HelpMessage logs targeted help a lecturer sent (F4.5).
type HelpMessage struct {
	ID        int       `json:"id" gorm:"primaryKey"`
	CourseID  int       `json:"course_id" gorm:"not null;index"`
	UserID    int       `json:"user_id" gorm:"not null;index"` // the learner
	SentBy    int       `json:"sent_by" gorm:"not null"`
	Subject   string    `json:"subject" gorm:"size:255"`
	Body      string    `json:"body" gorm:"type:text"`
	Concepts  []string  `json:"concepts" gorm:"type:jsonb;serializer:json"`
	CreatedAt time.Time `json:"created_at"`
}

func institutionModels() []any {
	return []any{&Institution{}, &InstitutionMember{}, &CourseClass{}, &RosterEntry{}, &Consent{}, &HelpMessage{}}
}

// SetMailer turns on email (roster invitations, help messages).
func (s *Service) SetMailer(m mailer.Mailer, frontendURL string) {
	s.mail, s.frontendURL = m, strings.TrimRight(frontendURL, "/")
	s.joinCheck = s.rosterCheck
}

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func normEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

// ── membership ───────────────────────────────────────────────────────────

func (s *Service) userEmail(id int) string {
	var u auth.UserBrief
	s.db.Where("id = ?", id).First(&u)
	return normEmail(u.Email)
}

// linkMemberships attaches email-only rows to the user's account.
func (s *Service) linkMemberships(userID int) {
	if e := s.userEmail(userID); e != "" {
		s.db.Model(&InstitutionMember{}).Where("email = ? AND user_id IS NULL", e).Update("user_id", userID)
	}
}

// orgRole: the user's role in the institution ("" = not a member).
func (s *Service) orgRole(instID, userID int) string {
	s.linkMemberships(userID)
	var m InstitutionMember
	if s.db.Where("institution_id = ? AND user_id = ?", instID, userID).First(&m).Error != nil {
		return ""
	}
	return m.Role
}

func (h *Handler) instFor(c *gin.Context, adminOnly bool) (*Institution, string, bool) {
	iid, ok := paramID(c, "iid")
	if !ok {
		return nil, "", false
	}
	var inst Institution
	if h.s.db.First(&inst, iid).Error != nil {
		fail(c, http.StatusNotFound, "Institution not found.")
		return nil, "", false
	}
	role := h.s.orgRole(inst.ID, c.GetInt(auth.CtxUserID))
	if isAdmin(c) {
		role = OrgAdmin
	}
	if role == "" || (adminOnly && role != OrgAdmin) {
		fail(c, http.StatusForbidden, map[bool]string{true: "Only the institution's admins can do this.", false: "You are not a member of this institution."}[role != ""])
		return nil, "", false
	}
	return &inst, role, true
}

// POST /api/learn/institutions {name}: the creator becomes its first admin.
func (h *Handler) CreateInstitution(c *gin.Context) {
	var in struct {
		Name string `json:"name"`
	}
	_ = c.ShouldBindJSON(&in)
	name := strings.TrimSpace(in.Name)
	if name == "" || len([]rune(name)) > 255 {
		fail(c, http.StatusUnprocessableEntity, "Give the institution a name (up to 255 characters).")
		return
	}
	me := c.GetInt(auth.CtxUserID)
	inst := Institution{Name: name, CreatedBy: me}
	err := h.s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&inst).Error; err != nil {
			return err
		}
		return tx.Create(&InstitutionMember{InstitutionID: inst.ID, Email: h.s.userEmail(me), UserID: &me, Role: OrgAdmin}).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "Could not create the institution.")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": inst})
}

// GET /api/learn/institutions: mine, with my role.
func (h *Handler) MyInstitutions(c *gin.Context) {
	me := c.GetInt(auth.CtxUserID)
	h.s.linkMemberships(me)
	type row struct {
		Institution
		Role string `json:"role"`
	}
	var rows []row
	h.s.db.Table("institutions i").Select("i.*, m.role").
		Joins("JOIN institution_members m ON m.institution_id = i.id").Where("m.user_id = ?", me).Order("i.name").Scan(&rows)
	if rows == nil {
		rows = []row{}
	}
	c.JSON(http.StatusOK, gin.H{"data": rows})
}

// GET /api/learn/institutions/:iid: members, courses and my role.
func (h *Handler) GetInstitution(c *gin.Context) {
	inst, role, ok := h.instFor(c, false)
	if !ok {
		return
	}
	members := []InstitutionMember{}
	h.s.db.Where("institution_id = ?", inst.ID).Order("role, email").Find(&members)
	type memberView struct {
		InstitutionMember
		Name string `json:"name"`
	}
	views := make([]memberView, len(members))
	for i, m := range members {
		views[i] = memberView{InstitutionMember: m}
		if m.UserID != nil {
			var u auth.UserBrief
			h.s.db.Where("id = ?", *m.UserID).First(&u)
			views[i].Name = u.Name
		}
	}
	type courseView struct {
		CourseID          int        `json:"course_id"`
		Title             string     `json:"title"`
		Lecturer          string     `json:"lecturer"`
		Learners          int64      `json:"learners"`
		ResultsApprovedAt *time.Time `json:"results_approved_at"`
	}
	var classes []CourseClass
	h.s.db.Where("institution_id = ?", inst.ID).Find(&classes)
	courses := []courseView{}
	for _, cl := range classes {
		co, err := h.s.course(cl.CourseID)
		if err != nil {
			continue
		}
		var u auth.UserBrief
		h.s.db.Where("id = ?", co.UserId).First(&u)
		v := courseView{CourseID: cl.CourseID, Title: co.CourseTitle, Lecturer: u.Name, ResultsApprovedAt: cl.ResultsApprovedAt}
		h.s.db.Model(&coursemodels.Enrollment{}).Where("course_id = ? AND user_id <> ?", cl.CourseID, co.UserId).Count(&v.Learners)
		courses = append(courses, v)
	}
	text := inst.ConsentText
	if text == "" {
		text = defaultConsent
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"institution": inst, "role": role, "members": views, "courses": courses, "consent_text": text}})
}

// PUT /api/learn/institutions/:iid {name?, consent_text?} (admin)
func (h *Handler) UpdateInstitution(c *gin.Context) {
	inst, _, ok := h.instFor(c, true)
	if !ok {
		return
	}
	var in struct {
		Name        *string `json:"name"`
		ConsentText *string `json:"consent_text"`
	}
	_ = c.ShouldBindJSON(&in)
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len([]rune(n)) > 255 {
			fail(c, http.StatusUnprocessableEntity, "Give the institution a name (up to 255 characters).")
			return
		}
		inst.Name = n
	}
	if in.ConsentText != nil {
		inst.ConsentText = clip(strings.TrimSpace(*in.ConsentText), 5000)
	}
	h.s.db.Model(inst).Select("name", "consent_text").Updates(inst)
	c.JSON(http.StatusOK, gin.H{"data": inst})
}

// POST /api/learn/institutions/:iid/members {email, role} (admin)
func (h *Handler) AddMember(c *gin.Context) {
	inst, _, ok := h.instFor(c, true)
	if !ok {
		return
	}
	var in struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	_ = c.ShouldBindJSON(&in)
	email := normEmail(in.Email)
	if !emailRe.MatchString(email) {
		fail(c, http.StatusUnprocessableEntity, "Enter a valid email address.")
		return
	}
	if in.Role != OrgAdmin && in.Role != OrgLecturer {
		fail(c, http.StatusUnprocessableEntity, "role must be admin or lecturer.")
		return
	}
	m := InstitutionMember{InstitutionID: inst.ID, Email: email, Role: in.Role}
	var u auth.UserBrief
	if h.s.db.Where("LOWER(email) = ?", email).First(&u).Error == nil {
		m.UserID = &u.ID
	}
	if err := h.s.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "institution_id"}, {Name: "email"}},
		DoUpdates: clause.AssignmentColumns([]string{"role"})}).Create(&m).Error; err != nil {
		fail(c, http.StatusInternalServerError, "Could not add the member.")
		return
	}
	if h.s.mail != nil {
		link := h.s.frontendURL + "/institutions/" + itoa(inst.ID)
		body := fmt.Sprintf("<p>You were added to <b>%s</b> on FocusFlow as %s.</p><p><a href=\"%s\">Open FocusFlow</a> and sign in or create an account with this email address.</p>",
			html.EscapeString(inst.Name), map[string]string{OrgAdmin: "an admin", OrgLecturer: "a lecturer"}[in.Role], link)
		go func() {
			if err := h.s.mail.Send(email, "You were added to "+inst.Name+" on FocusFlow", body); err != nil {
				slog.Warn("member email", "error", err)
			}
		}()
	}
	c.JSON(http.StatusCreated, gin.H{"data": m})
}

// DELETE /api/learn/institutions/:iid/members/:mid (admin; not the last admin)
func (h *Handler) RemoveMember(c *gin.Context) {
	inst, _, ok := h.instFor(c, true)
	if !ok {
		return
	}
	mid, ok := paramID(c, "mid")
	if !ok {
		return
	}
	var m InstitutionMember
	if h.s.db.Where("id = ? AND institution_id = ?", mid, inst.ID).First(&m).Error != nil {
		fail(c, http.StatusNotFound, "Member not found.")
		return
	}
	if m.Role == OrgAdmin {
		var admins int64
		h.s.db.Model(&InstitutionMember{}).Where("institution_id = ? AND role = ?", inst.ID, OrgAdmin).Count(&admins)
		if admins <= 1 {
			fail(c, http.StatusConflict, "An institution needs at least one admin.")
			return
		}
	}
	h.s.db.Delete(&m)
	c.JSON(http.StatusOK, gin.H{"message": "Member removed."})
}

// ── courses in an institution ────────────────────────────────────────────

func (s *Service) courseClass(courseID int) *CourseClass {
	var cl CourseClass
	if s.db.First(&cl, "course_id = ?", courseID).Error != nil {
		return nil
	}
	return &cl
}

// GET /api/learn/courses/:id/class (creator): institution, settings, roster,
// invite link and learners in one view.
func (h *Handler) GetClass(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	id := *course.CourseId
	me := c.GetInt(auth.CtxUserID)
	cl := h.s.courseClass(id)
	var inst *Institution
	if cl != nil {
		var i Institution
		if h.s.db.First(&i, cl.InstitutionID).Error == nil {
			inst = &i
		}
	}
	roster := []RosterEntry{}
	h.s.db.Where("course_id = ?", id).Order("name, email").Find(&roster)
	type rosterView struct {
		RosterEntry
		Joined    bool `json:"joined"`
		Consented bool `json:"consented"`
	}
	joined := map[string]int{}
	var rows []struct {
		UserID int
		Email  string
	}
	h.s.db.Table("enrollments e").Select("e.user_id, LOWER(u.email) AS email").Joins("JOIN users u ON u.id = e.user_id").
		Where("e.course_id = ?", id).Scan(&rows)
	for _, r := range rows {
		joined[r.Email] = r.UserID
	}
	consented := h.s.consentedUsers(id)
	views := make([]rosterView, len(roster))
	for i, r := range roster {
		uid, ok := joined[r.Email]
		views[i] = rosterView{RosterEntry: r, Joined: ok, Consented: ok && consented[uid]}
	}
	// Institutions the creator can attach the course to
	type instRow struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	h.s.linkMemberships(me)
	var mine []instRow
	h.s.db.Table("institutions i").Select("i.id, i.name").Joins("JOIN institution_members m ON m.institution_id = i.id").
		Where("m.user_id = ?", me).Order("i.name").Scan(&mine)
	if mine == nil {
		mine = []instRow{}
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"class": cl, "institution": inst, "my_institutions": mine,
		"roster": views, "invite": inviteView(course), "consented": len(consented)}})
}

// PUT /api/learn/courses/:id/class {institution_id (0 = none), roster_only}
func (h *Handler) UpdateClass(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	var in struct {
		InstitutionID *int  `json:"institution_id"`
		RosterOnly    *bool `json:"roster_only"`
	}
	_ = c.ShouldBindJSON(&in)
	id := *course.CourseId
	me := c.GetInt(auth.CtxUserID)
	cl := h.s.courseClass(id)
	if in.InstitutionID != nil {
		if *in.InstitutionID == 0 {
			h.s.db.Delete(&CourseClass{}, "course_id = ?", id)
			c.JSON(http.StatusOK, gin.H{"data": nil})
			return
		}
		if r := h.s.orgRole(*in.InstitutionID, me); r == "" && !isAdmin(c) {
			fail(c, http.StatusForbidden, "You can only add courses to institutions you belong to.")
			return
		}
		if cl == nil || cl.InstitutionID != *in.InstitutionID {
			cl = &CourseClass{CourseID: id, InstitutionID: *in.InstitutionID} // a new institution approves again
		}
	}
	if cl == nil {
		fail(c, http.StatusConflict, "Add the course to an institution first.")
		return
	}
	if in.RosterOnly != nil {
		cl.RosterOnly = *in.RosterOnly
	}
	if err := h.s.db.Save(cl).Error; err != nil {
		fail(c, http.StatusInternalServerError, "Could not save.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": cl})
}

// rosterCheck: in a roster-only class, the learner's email must be listed.
func (s *Service) rosterCheck(c *coursemodels.Course, userID int) string {
	cl := s.courseClass(*c.CourseId)
	if cl == nil || !cl.RosterOnly {
		return ""
	}
	var n int64
	s.db.Model(&RosterEntry{}).Where("course_id = ? AND email = ?", *c.CourseId, s.userEmail(userID)).Count(&n)
	if n == 0 {
		return "This course is for the students on the class list. Sign in with the email address your school uses, or ask your teacher to add you."
	}
	return ""
}

// parseRoster reads "email,name,student_id" rows (header optional, any order
// when there is a header); also accepts one email per line.
func parseRoster(text string) ([]RosterEntry, []ImportError) {
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	var out []RosterEntry
	var errs []ImportError
	col := map[string]int{"email": 0, "name": 1, "student_id": 2}
	line := 0
	seen := map[string]bool{}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			errs = append(errs, ImportError{line, "This line could not be read."})
			continue
		}
		if line == 1 {
			hdr := map[string]int{}
			for i, h := range rec {
				k := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF")))
				k = map[string]string{"e-mail": "email", "mail": "email", "full name": "name", "student id": "student_id", "id": "student_id", "student number": "student_id"}[k] + k
				for _, want := range []string{"email", "name", "student_id"} {
					if strings.HasPrefix(k, want) {
						hdr[want] = i
					}
				}
			}
			if _, ok := hdr["email"]; ok {
				col = hdr
				continue
			}
		}
		get := func(k string) string {
			if i, ok := col[k]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		email := normEmail(get("email"))
		if email == "" && strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		if !emailRe.MatchString(email) {
			errs = append(errs, ImportError{line, fmt.Sprintf("%q is not an email address.", get("email"))})
			continue
		}
		if seen[email] {
			continue
		}
		seen[email] = true
		out = append(out, RosterEntry{Email: email, Name: clip(get("name"), 255), StudentID: clip(get("student_id"), 64)})
	}
	return out, errs
}

// POST /api/learn/courses/:id/roster {text, save}: preview or add students.
func (h *Handler) ImportRoster(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	var in struct {
		Text string `json:"text"`
		Save bool   `json:"save"`
	}
	_ = c.ShouldBindJSON(&in)
	if len(in.Text) > maxImportBytes {
		fail(c, http.StatusUnprocessableEntity, "The list is too large (1 MB at most).")
		return
	}
	entries, errs := parseRoster(in.Text)
	if len(entries) > 2000 {
		errs = append(errs, ImportError{0, "At most 2000 students per course."})
	}
	if errs == nil {
		errs = []ImportError{}
	}
	if entries == nil {
		entries = []RosterEntry{}
	}
	if !in.Save || len(errs) > 0 || len(entries) == 0 {
		st := http.StatusOK
		if in.Save {
			st = http.StatusUnprocessableEntity
		}
		c.JSON(st, gin.H{"data": gin.H{"students": entries, "errors": errs, "saved": 0}})
		return
	}
	for i := range entries {
		entries[i].CourseID = *course.CourseId
	}
	if err := h.s.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "course_id"}, {Name: "email"}},
		DoUpdates: clause.AssignmentColumns([]string{"name", "student_id"})}).Create(&entries).Error; err != nil {
		fail(c, http.StatusInternalServerError, "Could not save the class list.")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": gin.H{"students": entries, "errors": errs, "saved": len(entries)}})
}

// DELETE /api/learn/courses/:id/roster/:rid
func (h *Handler) RemoveRosterEntry(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	rid, ok := paramID(c, "rid")
	if !ok {
		return
	}
	res := h.s.db.Where("id = ? AND course_id = ?", rid, *course.CourseId).Delete(&RosterEntry{})
	if res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "Student not found.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Removed from the class list."})
}

// POST /api/learn/courses/:id/roster/invite {all}: emails the invite link to
// students not invited yet (all = everyone who has not joined).
func (h *Handler) InviteRoster(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	if h.s.mail == nil {
		fail(c, http.StatusServiceUnavailable, "Email is not switched on on this server.")
		return
	}
	if course.CourseStatus != "published" {
		fail(c, http.StatusConflict, "Publish the course before inviting the class.")
		return
	}
	if !course.InviteEnabled || course.InviteCode == "" {
		course.InviteCode, course.InviteEnabled = newCertCode(), true
		h.s.db.Model(course).Select("invite_code", "invite_enabled").Updates(course)
	}
	var in struct {
		All bool `json:"all"`
	}
	_ = c.ShouldBindJSON(&in)
	q := h.s.db.Where("course_id = ?", *course.CourseId).
		Where("email NOT IN (SELECT LOWER(u.email) FROM enrollments e JOIN users u ON u.id = e.user_id WHERE e.course_id = ?)", *course.CourseId)
	if !in.All {
		q = q.Where("invited_at IS NULL")
	}
	var todo []RosterEntry
	q.Find(&todo)
	var teacher auth.UserBrief
	h.s.db.Where("id = ?", course.UserId).First(&teacher)
	link := h.s.frontendURL + "/join/" + course.InviteCode
	subject := teacher.Name + " invited you to " + course.CourseTitle
	now := time.Now()
	for _, r := range todo {
		greet := "Hello"
		if r.Name != "" {
			greet = "Hello " + html.EscapeString(r.Name)
		}
		body := fmt.Sprintf("<p>%s,</p><p>%s invited you to the course <b>%s</b> on FocusFlow.</p><p><a href=\"%s\">Join the course</a></p><p>Sign in or create an account with this email address (%s).</p>",
			greet, html.EscapeString(teacher.Name), html.EscapeString(course.CourseTitle), link, html.EscapeString(r.Email))
		to := r.Email
		go func() {
			if err := h.s.mail.Send(to, subject, body); err != nil {
				slog.Warn("roster invite email", "error", err)
			}
		}()
		h.s.db.Model(&RosterEntry{}).Where("id = ?", r.ID).Update("invited_at", now)
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"sent": len(todo)}, "message": fmt.Sprintf("Invitation sent to %d students.", len(todo))})
}

// ── consent (F4.4) ───────────────────────────────────────────────────────

func (s *Service) consentedUsers(courseID int) map[int]bool {
	var ids []int
	s.db.Model(&Consent{}).Where("course_id = ? AND withdrawn_at IS NULL", courseID).Pluck("user_id", &ids)
	out := map[int]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// consentFor is the learner's consent state for an institution course.
func (s *Service) consentFor(courseID, userID int) gin.H {
	cl := s.courseClass(courseID)
	if cl == nil {
		return nil
	}
	var inst Institution
	s.db.First(&inst, cl.InstitutionID)
	text := inst.ConsentText
	if text == "" {
		text = defaultConsent
	}
	var cons Consent
	given := s.db.Where("course_id = ? AND user_id = ? AND withdrawn_at IS NULL", courseID, userID).First(&cons).Error == nil
	return gin.H{"institution": inst.Name, "text": text, "given": given}
}

// GET /api/learn/courses/:id/consent (learner)
func (h *Handler) GetConsent(c *gin.Context) {
	id, ok := paramID(c, "id")
	if !ok {
		return
	}
	v := h.s.consentFor(id, c.GetInt(auth.CtxUserID))
	if v == nil {
		fail(c, http.StatusNotFound, "This course does not ask for consent.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": v})
}

// POST /api/learn/courses/:id/consent (enrolled learner): agree.
// DELETE: withdraw (results stop being shown at once).
func (h *Handler) SetConsent(c *gin.Context) {
	id, ok := paramID(c, "id")
	if !ok {
		return
	}
	me := c.GetInt(auth.CtxUserID)
	if h.s.courseClass(id) == nil || !h.s.isEnrolled(me, id) {
		fail(c, http.StatusNotFound, "This course does not ask for consent.")
		return
	}
	now := time.Now()
	if c.Request.Method == http.MethodDelete {
		h.s.db.Model(&Consent{}).Where("course_id = ? AND user_id = ?", id, me).Update("withdrawn_at", now)
		c.JSON(http.StatusOK, gin.H{"message": "Consent withdrawn. Your results are no longer shown."})
		return
	}
	cons := Consent{CourseID: id, UserID: me, Version: ConsentVersion, AcceptedAt: now}
	h.s.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "course_id"}, {Name: "user_id"}},
		DoUpdates: clause.Assignments(map[string]any{"version": ConsentVersion, "accepted_at": now, "withdrawn_at": nil})}).Create(&cons)
	c.JSON(http.StatusOK, gin.H{"message": "Thank you. Your teacher can now see your results."})
}

// POST /api/learn/institutions/:iid/courses/:cid/results-access {approved} (admin)
func (h *Handler) ApproveResults(c *gin.Context) {
	inst, _, ok := h.instFor(c, true)
	if !ok {
		return
	}
	cid, ok := paramID(c, "cid")
	if !ok {
		return
	}
	cl := h.s.courseClass(cid)
	if cl == nil || cl.InstitutionID != inst.ID {
		fail(c, http.StatusNotFound, "This course is not in the institution.")
		return
	}
	var in struct {
		Approved bool `json:"approved"`
	}
	_ = c.ShouldBindJSON(&in)
	if in.Approved {
		me, now := c.GetInt(auth.CtxUserID), time.Now()
		cl.ResultsApprovedBy, cl.ResultsApprovedAt = &me, &now
	} else {
		cl.ResultsApprovedBy, cl.ResultsApprovedAt = nil, nil
	}
	h.s.db.Model(cl).Select("results_approved_by", "results_approved_at").Updates(cl)
	c.JSON(http.StatusOK, gin.H{"data": cl})
}

// ── class results (F4.5) ─────────────────────────────────────────────────

type learnerResult struct {
	UserID  int                 `json:"user_id"`
	Name    string              `json:"name"`
	Email   string              `json:"email"`
	Scores  map[string]*float64 `json:"scores"` // concept → percent (nil = not tested)
	Weakest []string            `json:"weakest"`
	Overall *float64            `json:"overall"`
	Helped  *time.Time          `json:"helped_at"` // last help message
}

// classResults builds the struggle map from finished attempts: for each
// learner and unit the latest done attempt counts; items are grouped by the
// concept their question tests.
func (s *Service) classResults(courseID int) ([]string, []learnerResult, map[string]float64) {
	consented := s.consentedUsers(courseID)
	var attempts []Attempt
	s.db.Where("course_id = ? AND status = ?", courseID, AttemptDone).Order("id").Find(&attempts)
	latest := map[[2]int]Attempt{}
	for _, a := range attempts {
		if consented[a.UserID] {
			latest[[2]int{a.UserID, a.UnitID}] = a // later ids win
		}
	}
	type acc struct{ sum, n float64 }
	per := map[int]map[string]*acc{}
	class := map[string]*acc{}
	for _, a := range latest {
		if per[a.UserID] == nil {
			per[a.UserID] = map[string]*acc{}
		}
		for _, it := range a.Items {
			concept := strings.TrimSpace(it.Concept)
			if concept == "" || it.Mark == nil {
				continue
			}
			if per[a.UserID][concept] == nil {
				per[a.UserID][concept] = &acc{}
			}
			per[a.UserID][concept].sum += *it.Mark
			per[a.UserID][concept].n++
			if class[concept] == nil {
				class[concept] = &acc{}
			}
			class[concept].sum += *it.Mark
			class[concept].n++
		}
	}
	concepts := make([]string, 0, len(class))
	avg := map[string]float64{}
	for k, v := range class {
		concepts = append(concepts, k)
		avg[k] = round1(v.sum / v.n * 100)
	}
	sort.Slice(concepts, func(i, j int) bool { return avg[concepts[i]] < avg[concepts[j]] }) // hardest first

	var users []auth.UserBrief
	ids := []int{0}
	for id := range consented {
		ids = append(ids, id)
	}
	s.db.Where("id IN ?", ids).Find(&users)
	var helped []HelpMessage
	s.db.Where("course_id = ?", courseID).Order("id").Find(&helped)
	lastHelp := map[int]time.Time{}
	for _, h := range helped {
		lastHelp[h.UserID] = h.CreatedAt
	}
	out := make([]learnerResult, 0, len(users))
	for _, u := range users {
		r := learnerResult{UserID: u.ID, Name: u.Name, Email: u.Email, Scores: map[string]*float64{}, Weakest: []string{}}
		var tot, n float64
		type cs struct {
			c string
			v float64
		}
		var mine []cs
		for _, k := range concepts {
			if a := per[u.ID][k]; a != nil {
				v := round1(a.sum / a.n * 100)
				r.Scores[k] = &v
				tot += a.sum
				n += a.n
				mine = append(mine, cs{k, v})
			} else {
				r.Scores[k] = nil
			}
		}
		if n > 0 {
			o := round1(tot / n * 100)
			r.Overall = &o
		}
		sort.SliceStable(mine, func(i, j int) bool { return mine[i].v < mine[j].v })
		for _, m := range mine {
			if m.v < 70 && len(r.Weakest) < 3 {
				r.Weakest = append(r.Weakest, m.c)
			}
		}
		if t, ok := lastHelp[u.ID]; ok {
			r.Helped = &t
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Overall, out[j].Overall
		if a == nil || b == nil {
			return b == nil && a != nil
		}
		return *a < *b // most help needed first
	})
	return concepts, out, avg
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

// resultsAllowed: an institution course whose results an admin approved.
func (h *Handler) resultsAllowed(c *gin.Context) (*coursemodels.Course, bool) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return nil, false
	}
	cl := h.s.courseClass(*course.CourseId)
	if cl == nil {
		fail(c, http.StatusConflict, "Class results are for courses in an institution. Add the course to one on the Class page.")
		return nil, false
	}
	if cl.ResultsApprovedAt == nil {
		fail(c, http.StatusForbidden, "An admin of your institution needs to approve results access for this course first.")
		return nil, false
	}
	return course, true
}

// GET /api/learn/courses/:id/results: the struggle map (consenting learners only).
func (h *Handler) ClassResults(c *gin.Context) {
	course, ok := h.resultsAllowed(c)
	if !ok {
		return
	}
	concepts, learners, avg := h.s.classResults(*course.CourseId)
	var enrolled int64
	h.s.db.Model(&coursemodels.Enrollment{}).Where("course_id = ? AND user_id <> ?", *course.CourseId, course.UserId).Count(&enrolled)
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"concepts": concepts, "class_average": avg, "learners": learners,
		"enrolled": enrolled, "consented": len(h.s.consentedUsers(*course.CourseId))}})
}

// helpLinks: for each concept, the units where it is taught or tested.
func (s *Service) helpLinks(courseID int, concepts []string) map[string][]gin.H {
	out := map[string][]gin.H{}
	for _, k := range concepts {
		var unitIDs []int
		s.db.Model(&Question{}).Where("course_id = ? AND concept = ?", courseID, k).Distinct().Pluck("unit_id", &unitIDs)
		var conceptUnits []int
		s.db.Model(&Concept{}).Joins("JOIN units u ON u.id = unit_concepts.unit_id").
			Where("u.course_id = ? AND unit_concepts.title = ?", courseID, k).Distinct().Pluck("unit_concepts.unit_id", &conceptUnits)
		seen := map[int]bool{}
		for _, id := range append(conceptUnits, unitIDs...) {
			var u Unit
			if seen[id] || s.db.First(&u, id).Error != nil {
				continue
			}
			seen[id] = true
			target := u
			if u.Kind == "quiz" { // point to the lessons it covers, not the quiz
				var lesson Unit
				cov := s.coveredUnits(&u)
				if len(cov) == 0 || s.db.First(&lesson, cov[len(cov)-1]).Error != nil || seen[lesson.ID] {
					continue
				}
				seen[lesson.ID] = true
				target = lesson
			}
			out[k] = append(out[k], gin.H{"unit_id": target.ID, "title": target.Title,
				"url": fmt.Sprintf("%s/learn/%d/unit/%d", s.frontendURL, courseID, target.ID)})
			if len(out[k]) >= 3 {
				break
			}
		}
		var refs []Concept
		s.db.Joins("JOIN units u ON u.id = unit_concepts.unit_id").Where("u.course_id = ? AND unit_concepts.title = ?", courseID, k).Find(&refs)
		for _, cn := range refs {
			for i, r := range cn.References {
				if i >= 2 {
					break
				}
				out[k] = append(out[k], gin.H{"title": r.Title, "url": r.URL, "external": true})
			}
		}
	}
	return out
}

// GET /api/learn/courses/:id/results/:uid/help: a draft the lecturer edits.
func (h *Handler) HelpDraft(c *gin.Context) {
	course, ok := h.resultsAllowed(c)
	if !ok {
		return
	}
	uid, ok := paramID(c, "uid")
	if !ok {
		return
	}
	_, learners, _ := h.s.classResults(*course.CourseId)
	var me *learnerResult
	for i := range learners {
		if learners[i].UserID == uid {
			me = &learners[i]
		}
	}
	if me == nil {
		fail(c, http.StatusNotFound, "No results for this learner (or no consent).")
		return
	}
	links := h.s.helpLinks(*course.CourseId, me.Weakest)
	var b strings.Builder
	first := strings.Fields(me.Name)
	greet := "Hello"
	if len(first) > 0 {
		greet = "Hello " + first[0]
	}
	fmt.Fprintf(&b, "%s,\n\n", greet)
	if len(me.Weakest) == 0 {
		fmt.Fprintf(&b, "You are doing well in %s. Keep going!\n", course.CourseTitle)
	} else {
		fmt.Fprintf(&b, "Looking at your quizzes in %s, these topics could use a second look:\n\n", course.CourseTitle)
		for _, k := range me.Weakest {
			score := ""
			if v := me.Scores[k]; v != nil {
				score = fmt.Sprintf(" (%.0f%%)", *v)
			}
			fmt.Fprintf(&b, "- %s%s\n", k, score)
			for _, l := range links[k] {
				fmt.Fprintf(&b, "  - %s: %s\n", l["title"], l["url"])
			}
		}
		b.WriteString("\nGo through these again and try the quiz once more. Ask me if anything is unclear.\n")
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"to": me.Name, "email": me.Email, "concepts": me.Weakest,
		"subject": "Help with " + course.CourseTitle, "body": b.String(), "links": links}})
}

// POST /api/learn/courses/:id/results/:uid/help {subject, body, concepts}:
// sends the reviewed message to the learner by email and logs it.
func (h *Handler) SendHelp(c *gin.Context) {
	course, ok := h.resultsAllowed(c)
	if !ok {
		return
	}
	uid, ok := paramID(c, "uid")
	if !ok {
		return
	}
	if !h.s.consentedUsers(*course.CourseId)[uid] {
		fail(c, http.StatusForbidden, "This learner has not given consent.")
		return
	}
	var in struct {
		Subject  string   `json:"subject"`
		Body     string   `json:"body"`
		Concepts []string `json:"concepts"`
	}
	_ = c.ShouldBindJSON(&in)
	in.Subject, in.Body = clip(strings.TrimSpace(in.Subject), 255), clip(strings.TrimSpace(in.Body), 10000)
	if in.Subject == "" || in.Body == "" {
		fail(c, http.StatusUnprocessableEntity, "Write a subject and a message.")
		return
	}
	if h.s.mail == nil {
		fail(c, http.StatusServiceUnavailable, "Email is not switched on on this server.")
		return
	}
	var learner auth.UserBrief
	h.s.db.Where("id = ?", uid).First(&learner)
	htmlBody := "<div style=\"font-family:sans-serif;white-space:pre-wrap\">" + linkify(html.EscapeString(in.Body)) + "</div>"
	if err := h.s.mail.Send(learner.Email, in.Subject, htmlBody); err != nil {
		slog.Warn("help email", "error", err)
		fail(c, http.StatusBadGateway, "The email could not be sent. Try again in a minute.")
		return
	}
	if in.Concepts == nil {
		in.Concepts = []string{}
	}
	msg := HelpMessage{CourseID: *course.CourseId, UserID: uid, SentBy: c.GetInt(auth.CtxUserID), Subject: in.Subject, Body: in.Body, Concepts: in.Concepts}
	h.s.db.Create(&msg)
	c.JSON(http.StatusOK, gin.H{"data": msg, "message": "Sent to " + learner.Name + "."})
}

var urlRe = regexp.MustCompile(`https?://[^\s<>"]+`)

func linkify(escaped string) string {
	return urlRe.ReplaceAllStringFunc(escaped, func(u string) string { return `<a href="` + u + `">` + u + `</a>` })
}
