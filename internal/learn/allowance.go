package learn

import (
	"errors"
	"fmt"
	"math"
	"time"

	"focusflow-api/internal/auth"
)

// Each creator may spend a share of the AI budget per day and per month
// (AI_USER_DAILY_USD, AI_USER_MONTHLY_USD), so one person cannot use up the
// whole app's AI. Creators see it as "percent left", never as money. The
// spend is the estimated cost logged in ai_usage for that user; admins are
// not limited. A step that starts under the limit may finish a little over it.

// ErrAllowance means a creator's own AI allowance is used up.
var ErrAllowance = errors.New("your AI allowance is used up")

type allowanceError struct{ msg string }

func (e *allowanceError) Error() string        { return e.msg }
func (e *allowanceError) Is(target error) bool { return target == ErrAllowance }

// AllowanceWindow is one period of a creator's allowance.
type AllowanceWindow struct {
	Period      string    `json:"period"` // day | month
	UsedPercent int       `json:"used_percent"`
	ResetsAt    time.Time `json:"resets_at"`
}

// Allowance is what a creator has left for AI work.
type Allowance struct {
	Unlimited bool              `json:"unlimited"`
	CanUseAI  bool              `json:"can_use_ai"`
	Message   string            `json:"message,omitempty"` // why not, and when it refills
	Windows   []AllowanceWindow `json:"windows"`
}

// allowanceWindows: days and months start at midnight UTC.
func allowanceWindows(now time.Time) (day, nextDay, month, nextMonth time.Time) {
	n := now.UTC()
	day = time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	month = time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, time.UTC)
	return day, day.AddDate(0, 0, 1), month, month.AddDate(0, 1, 0)
}

func (s *Service) userSpendMicro(userID int, since time.Time) int64 {
	var micro int64
	s.db.Model(&AIUsage{}).Where("user_id = ? AND created_at >= ?", userID, since).
		Select("COALESCE(SUM(cost_micro_usd), 0)").Scan(&micro)
	return micro
}

func (s *Service) isAdminUser(userID int) bool {
	var role string
	s.db.Table("users").Select("role").Where("id = ?", userID).Scan(&role)
	return role == auth.RoleAdmin
}

func usedPercent(micro int64, capUSD float64) int {
	p := float64(micro) / (capUSD * 1e6) * 100
	if micro > 0 && p < 1 {
		return 1 // a little was used: never show 0%
	}
	return int(math.Min(100, math.Floor(p)))
}

// UserAllowance is a creator's AI allowance now.
func (s *Service) UserAllowance(userID int) Allowance {
	a := Allowance{CanUseAI: true, Windows: []AllowanceWindow{}}
	limits := s.limitsFor(userID)
	if limits.UserDailyUSD <= 0 && limits.UserMonthlyUSD <= 0 || s.isAdminUser(userID) {
		a.Unlimited = true
		return a
	}
	now := time.Now()
	day, nextDay, month, nextMonth := allowanceWindows(now)
	if lim := limits.UserDailyUSD; lim > 0 {
		used := s.userSpendMicro(userID, day)
		a.Windows = append(a.Windows, AllowanceWindow{Period: "day", UsedPercent: usedPercent(used, lim), ResetsAt: nextDay})
		if float64(used) >= lim*1e6 {
			a.CanUseAI = false
			a.Message = "You have used today's AI allowance. It refills in " + untilText(nextDay.Sub(now)) +
				". Your course is saved; carry on then."
		}
	}
	if lim := limits.UserMonthlyUSD; lim > 0 {
		used := s.userSpendMicro(userID, month)
		a.Windows = append(a.Windows, AllowanceWindow{Period: "month", UsedPercent: usedPercent(used, lim), ResetsAt: nextMonth})
		if float64(used) >= lim*1e6 {
			a.CanUseAI = false // the month outlasts the day: say when it really refills
			a.Message = "You have used this month's AI allowance. It refills on " + nextMonth.Format("2 January") +
				". Your course is saved, and you can still edit it by hand."
		}
	}
	return a
}

// untilText: "3 h 20 min", "45 min".
func untilText(d time.Duration) string {
	min := int(math.Ceil(d.Minutes()))
	if min < 1 {
		min = 1
	}
	if min >= 60 {
		return fmt.Sprintf("%d h %d min", min/60, min%60)
	}
	return fmt.Sprintf("%d min", min)
}

// checkAI returns ErrBudget when the app's monthly cap is reached, or an
// ErrAllowance error when this user's own allowance is (userID 0: app only).
func (s *Service) checkAI(userID int) error {
	if err := s.checkBudget(); err != nil {
		return err
	}
	if userID == 0 {
		return nil
	}
	if a := s.UserAllowance(userID); !a.CanUseAI {
		return &allowanceError{a.Message}
	}
	return nil
}

// limitMessage is what to tell the creator for a checkAI error.
func limitMessage(err error) string {
	var a *allowanceError
	if errors.As(err, &a) {
		return a.msg
	}
	return budgetMessage
}

// UserAIUse is one creator's AI spend, for the admin page.
type UserAIUse struct {
	UserID       int     `json:"user_id"`
	Name         string  `json:"name"`
	TodayUSD     float64 `json:"today_usd"`
	MonthUSD     float64 `json:"month_usd"`
	DayPercent   int     `json:"day_percent"`
	MonthPercent int     `json:"month_percent"`
}

// topUsers: the creators who used the most AI this month.
func (s *Service) topUsers(limit int) []UserAIUse {
	day, _, month, _ := allowanceWindows(time.Now())
	var rows []struct {
		UserID     int
		Name       string
		TodayMicro int64
		MonthMicro int64
	}
	s.db.Table("ai_usage").
		Select("ai_usage.user_id, COALESCE(users.name, '') AS name, "+
			"COALESCE(SUM(CASE WHEN ai_usage.created_at >= ? THEN cost_micro_usd ELSE 0 END), 0) AS today_micro, "+
			"COALESCE(SUM(cost_micro_usd), 0) AS month_micro", day).
		Joins("LEFT JOIN users ON users.id = ai_usage.user_id").
		Where("ai_usage.user_id > 0 AND ai_usage.created_at >= ?", month).
		Group("ai_usage.user_id, users.name").Order("month_micro DESC").Limit(limit).Scan(&rows)
	out := make([]UserAIUse, 0, len(rows))
	for _, r := range rows {
		u := UserAIUse{UserID: r.UserID, Name: r.Name, TodayUSD: float64(r.TodayMicro) / 1e6, MonthUSD: float64(r.MonthMicro) / 1e6}
		if s.limits.UserDailyUSD > 0 {
			u.DayPercent = usedPercent(r.TodayMicro, s.limits.UserDailyUSD)
		}
		if s.limits.UserMonthlyUSD > 0 {
			u.MonthPercent = usedPercent(r.MonthMicro, s.limits.UserMonthlyUSD)
		}
		out = append(out, u)
	}
	return out
}
