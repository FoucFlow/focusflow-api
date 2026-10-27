package learn

import (
	"time"

	"focusflow-api/internal/auth"
)

// Plans (FEATURES.md F4.2). Learning stays free. Personal accounts are free
// or Pro; owners and lecturers of an institution on the paid Institution
// plan get the same higher creation limits and larger AI allowance.
// Payments come later; FocusFlow admins set plans until then.

const (
	PlanFree        = "free"
	PlanPro         = "pro"
	PlanInstitution = "institution" // Pro limits as owner or lecturer of a paid institution
)

// ProLimits replace the free numbers for Pro users (0 = no limit).
type ProLimits struct {
	CoursesPerUser        int
	MaterialsPerCourse    int
	OrganiseRunsPerCourse int
	QuestionRunsPerUnit   int
	NewCoursesPerDay      int
	AIFactor              float64 // times the free AI allowance
}

var DefaultPro = ProLimits{CoursesPerUser: 50, MaterialsPerCourse: 100, OrganiseRunsPerCourse: 30, QuestionRunsPerUnit: 30, NewCoursesPerDay: 0, AIFactor: 6}

// planOf: pro while the user's plan is pro and not ended; institution when
// they are an owner or lecturer of an institution on the paid plan; else
// free. Students keep their own plan.
func (s *Service) planOf(userID int) string {
	var u struct {
		Plan      string
		PlanUntil *time.Time
	}
	s.db.Model(&auth.UserBrief{}).Select("plan, plan_until").Where("id = ?", userID).Scan(&u)
	if u.Plan == PlanPro && (u.PlanUntil == nil || time.Now().Before(*u.PlanUntil)) {
		return PlanPro
	}
	var n int64
	s.db.Table("institution_members m").Joins("JOIN institutions i ON i.id = m.institution_id").
		Where("m.user_id = ? AND m.role IN ? AND i.plan = ?", userID, []string{OrgOwner, OrgLecturer}, InstPaid).Count(&n)
	if n > 0 {
		return PlanInstitution
	}
	return PlanFree
}

// limitsFor returns the limits that apply to this user.
func (s *Service) limitsFor(userID int) Limits {
	l := s.limits
	l.Plan = s.planOf(userID)
	if l.Plan == PlanFree {
		return l
	}
	p := s.limits.Pro
	if p == (ProLimits{}) {
		p = DefaultPro
	}
	l.CoursesPerUser, l.MaterialsPerCourse = p.CoursesPerUser, p.MaterialsPerCourse
	l.OrganiseRunsPerCourse, l.QuestionRunsPerUnit, l.NewCoursesPerDay = p.OrganiseRunsPerCourse, p.QuestionRunsPerUnit, p.NewCoursesPerDay
	if p.AIFactor > 0 {
		l.UserDailyUSD *= p.AIFactor
		l.UserMonthlyUSD *= p.AIFactor
	}
	return l
}

func planLabel(l Limits) string {
	switch l.Plan {
	case PlanPro:
		return "The Pro plan"
	case PlanInstitution:
		return "Your institution's plan"
	}
	return "The free plan"
}

func upgradeHint(l Limits) string {
	if l.Plan == PlanFree {
		return ", or move to Pro for more"
	}
	return ""
}
