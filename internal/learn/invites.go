package learn

import (
	"net/http"
	"strings"
	"time"

	"focusflow-api/internal/auth"
	coursemodels "focusflow-api/internal/course/models"

	"github.com/gin-gonic/gin"
)

// Invite link (F4.1): the creator shares a link; anyone signed in who opens
// it can join the course, public or private. The creator can turn the link
// off, make a new one (the old one stops working) and see who joined.

func (s *Service) courseByInvite(code string) (*coursemodels.Course, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) < 6 || len(code) > 16 {
		return nil, false
	}
	var c coursemodels.Course
	if s.db.Where("invite_code = ? AND invite_enabled", code).First(&c).Error != nil {
		return nil, false
	}
	return &c, true
}

// GET /api/learn/courses/:id/invite (creator)
func (h *Handler) GetInvite(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": inviteView(course)})
}

func inviteView(c *coursemodels.Course) gin.H {
	return gin.H{"code": c.InviteCode, "enabled": c.InviteEnabled && c.InviteCode != ""}
}

// PUT /api/learn/courses/:id/invite {enabled, new_code}
func (h *Handler) UpdateInvite(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
		NewCode bool  `json:"new_code"`
	}
	_ = c.ShouldBindJSON(&in)
	if in.NewCode || (course.InviteCode == "" && in.Enabled != nil && *in.Enabled) {
		course.InviteCode = newCertCode() // 10 characters, no look-alikes
	}
	if in.Enabled != nil {
		course.InviteEnabled = *in.Enabled
	}
	if err := h.s.db.Model(course).Select("invite_code", "invite_enabled").Updates(course).Error; err != nil {
		fail(c, http.StatusInternalServerError, "Could not save the invite link.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": inviteView(course)})
}

// GET /api/learn/invites/:code: what the link leads to, before joining.
func (h *Handler) InviteInfo(c *gin.Context) {
	course, ok := h.s.courseByInvite(c.Param("code"))
	if !ok {
		fail(c, http.StatusNotFound, "This invite link does not work any more. Ask for a new one.")
		return
	}
	var creator auth.UserBrief
	h.s.db.Where("id = ?", course.UserId).First(&creator)
	me := c.GetInt(auth.CtxUserID)
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"course_id": *course.CourseId, "course_title": course.CourseTitle, "course_description": course.CourseDescription,
		"creator_name": creator.Name, "units": len(h.s.learnerUnits(*course.CourseId)), "published": course.CourseStatus == "published",
		"enrolled": h.s.isEnrolled(me, *course.CourseId), "is_creator": course.UserId == me,
	}})
}

// POST /api/learn/invites/:code/join
func (h *Handler) JoinByInvite(c *gin.Context) {
	course, ok := h.s.courseByInvite(c.Param("code"))
	if !ok {
		fail(c, http.StatusNotFound, "This invite link does not work any more. Ask for a new one.")
		return
	}
	if course.CourseStatus != "published" {
		fail(c, http.StatusConflict, "This course is not open yet. Try again when your teacher publishes it.")
		return
	}
	me := c.GetInt(auth.CtxUserID)
	if course.UserId == me {
		fail(c, http.StatusUnprocessableEntity, "You created this course.")
		return
	}
	if msg := h.s.joinAllowed(course, me); msg != "" {
		fail(c, http.StatusForbidden, msg)
		return
	}
	e := coursemodels.Enrollment{CourseID: *course.CourseId, UserID: me}
	h.s.db.Where("course_id = ? AND user_id = ?", e.CourseID, me).FirstOrCreate(&e)
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"course_id": e.CourseID}, "message": "You joined the course."})
}

// joinAllowed: courses of an institution can require learners to be on the
// class roster (see institutions.go); others are open to the link.
func (s *Service) joinAllowed(c *coursemodels.Course, userID int) string {
	if s.joinCheck != nil {
		return s.joinCheck(c, userID)
	}
	return ""
}

// learnerRow is one learner of a course, for its creator.
type learnerRow struct {
	UserID         int       `json:"user_id"`
	Name           string    `json:"name"`
	Email          string    `json:"email"`
	EnrolledAt     time.Time `json:"enrolled_at"`
	CompletedUnits int       `json:"completed_units"`
	TotalUnits     int       `json:"total_units"`
}

// GET /api/learn/courses/:id/learners (creator)
func (h *Handler) CourseLearners(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	id := *course.CourseId
	var rows []learnerRow
	h.s.db.Table("enrollments e").Select("e.user_id, u.name, u.email, e.enrolled_at").
		Joins("JOIN users u ON u.id = e.user_id").Where("e.course_id = ? AND e.user_id <> ?", id, course.UserId).Order("e.enrolled_at").Scan(&rows)
	for i := range rows {
		rows[i].CompletedUnits, rows[i].TotalUnits, _ = h.s.courseProgressFor(rows[i].UserID, id)
	}
	if rows == nil {
		rows = []learnerRow{}
	}
	c.JSON(http.StatusOK, gin.H{"data": rows})
}

// DELETE /api/learn/courses/:id/learners/:uid (creator): removes a learner.
// Their progress and attempts stay, so joining again restores them.
func (h *Handler) RemoveLearner(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	uid, ok := paramID(c, "uid")
	if !ok {
		return
	}
	res := h.s.db.Where("course_id = ? AND user_id = ?", *course.CourseId, uid).Delete(&coursemodels.Enrollment{})
	if res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "This person is not a learner of the course.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Learner removed."})
}
