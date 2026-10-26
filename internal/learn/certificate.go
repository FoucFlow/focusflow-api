package learn

import (
	"crypto/rand"
	"math/big"
	"net/http"
	"strings"
	"time"

	"focusflow-api/internal/auth"

	"github.com/gin-gonic/gin"
)

// Completion certificate (F2.6): free, issued by the app when a learner has
// finished every required unit of a course (the final exam included when the
// course has one). Anyone can check it by its code.

type Certificate struct {
	ID          int       `json:"-" gorm:"primaryKey"`
	Code        string    `json:"code" gorm:"size:16;not null;uniqueIndex"`
	UserID      int       `json:"-" gorm:"not null;uniqueIndex:idx_cert_user_course"`
	CourseID    int       `json:"course_id" gorm:"not null;uniqueIndex:idx_cert_user_course"`
	LearnerName string    `json:"learner_name" gorm:"size:255"`
	CourseTitle string    `json:"course_title" gorm:"size:255"`
	CreatorName string    `json:"creator_name" gorm:"size:255"`
	FinalScore  *float64  `json:"final_score,omitempty"` // the final exam's score, when there is one
	IssuedAt    time.Time `json:"issued_at"`
}

func newCertCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 10)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b[i] = alphabet[n.Int64()]
	}
	return string(b)
}

// maybeCertify issues the certificate once the course is finished.
func (s *Service) maybeCertify(userID, courseID int) {
	if _, _, finished := s.courseProgressFor(userID, courseID); !finished {
		return
	}
	var n int64
	s.db.Model(&Certificate{}).Where("user_id = ? AND course_id = ?", userID, courseID).Count(&n)
	if n > 0 {
		return
	}
	c, err := s.course(courseID)
	if err != nil {
		return
	}
	var learner, creator auth.UserBrief
	s.db.Where("id = ?", userID).First(&learner)
	s.db.Where("id = ?", c.UserId).First(&creator)
	cert := Certificate{Code: newCertCode(), UserID: userID, CourseID: courseID, LearnerName: learner.Name,
		CourseTitle: c.CourseTitle, CreatorName: creator.Name, IssuedAt: time.Now()}
	var final Unit
	if s.db.Where("course_id = ? AND final", courseID).First(&final).Error == nil {
		var best Attempt
		if s.db.Where("unit_id = ? AND user_id = ? AND passed", final.ID, userID).Order("score_percent DESC").First(&best).Error == nil {
			sc := best.ScorePercent
			cert.FinalScore = &sc
		}
	}
	s.db.Create(&cert) // the unique index makes a second issue a no-op
}

// certificateFor returns the learner's certificate for a course, if any.
func (s *Service) certificateFor(userID, courseID int) *Certificate {
	var c Certificate
	if s.db.Where("user_id = ? AND course_id = ?", userID, courseID).First(&c).Error != nil {
		return nil
	}
	return &c
}

// GET /api/certificates/:code (public): checks a certificate.
func (h *Handler) VerifyCertificate(c *gin.Context) {
	code := strings.ToUpper(strings.TrimSpace(c.Param("code")))
	var cert Certificate
	if len(code) > 16 || h.s.db.Where("code = ?", code).First(&cert).Error != nil {
		fail(c, http.StatusNotFound, "No certificate has this code.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": cert})
}
