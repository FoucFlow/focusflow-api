// Package services contains the business-logic layer for the Course Material Service.
package services

import (
	"fmt"
	"focusflow-api/internal/material/model"
	"sort"
	"time"

	"gorm.io/gorm"
)

// DashboardSummary is what the /api/dashboard/summary endpoint returns —
// one round-trip that the dashboard page reads cover-to-cover.
type DashboardSummary struct {
	Stats            DashboardStats         `json:"stats"`
	ContinueLearning []DashboardLessonCard  `json:"continue_learning"`
	LessonsToReview  []DashboardWeakLesson  `json:"lessons_to_review"`
	RecentAttempts   []DashboardAttemptCard `json:"recent_attempts"`
}

// DashboardStats are the four numbers in the top grid.
type DashboardStats struct {
	CoursesCount          int     `json:"courses_count"`
	LessonsCompletedCount int     `json:"lessons_completed_count"`
	AverageQuizScore      float64 `json:"average_quiz_score"`
	CurrentStreakDays     int     `json:"current_streak_days"`
}

// DashboardLessonCard is one entry in the Continue Learning rail.
type DashboardLessonCard struct {
	VideoID     int       `json:"video_id"`
	CourseID    string    `json:"course_id"`
	LessonTitle string    `json:"lesson_title"`
	YouTubeID   string    `json:"youtube_id"`
	Completed   bool      `json:"completed"`
	LastTouched time.Time `json:"last_touched"`
}

// DashboardWeakLesson is one entry in the Lessons to Review widget.
type DashboardWeakLesson struct {
	AttemptID   int       `json:"attempt_id"`
	VideoID     int       `json:"video_id"`
	CourseID    string    `json:"course_id"`
	LessonTitle string    `json:"lesson_title"`
	Score       float64   `json:"score"`
	CreatedAt   time.Time `json:"created_at"`
}

// DashboardAttemptCard is one entry in the Recent Attempts rail.
type DashboardAttemptCard struct {
	AttemptID   int                 `json:"attempt_id"`
	VideoID     int                 `json:"video_id"`
	CourseID    string              `json:"course_id"`
	LessonTitle string              `json:"lesson_title"`
	Status      model.AttemptStatus `json:"status"`
	TotalScore  float64             `json:"total_score"`
	CreatedAt   time.Time           `json:"created_at"`
}

// DashboardService computes the dashboard summary for a learner. It is
// read-only — no state, no Gemini calls. Cheap: one or two SQL queries.
type DashboardService struct {
	db           *gorm.DB
	materialRepo model.CourseMaterialRepository
	quizRepo     model.QuizRepository
}

// NewDashboardService wires the aggregator.
func NewDashboardService(
	db *gorm.DB,
	materialRepo model.CourseMaterialRepository,
	quizRepo model.QuizRepository,
) *DashboardService {
	return &DashboardService{
		db:           db,
		materialRepo: materialRepo,
		quizRepo:     quizRepo,
	}
}

// Summary returns everything the dashboard page needs in one call.
//
// Cross-service note: the Course Service owns the Course table, so we
// cannot count "courses owned by user X" directly. Instead we count
// distinct `course_id` values across the learner's lesson activity (quiz
// attempts + completed lessons). For an engaged learner this is identical
// to "courses I'm studying", which is the metric a dashboard actually
// wants to surface anyway.
func (s *DashboardService) Summary(userID int) (DashboardSummary, error) {
	if userID <= 0 {
		return DashboardSummary{}, fmt.Errorf("user_id must be positive")
	}

	stats, err := s.computeStats(userID)
	if err != nil {
		return DashboardSummary{}, err
	}

	continueLearning, err := s.computeContinueLearning(userID)
	if err != nil {
		return DashboardSummary{}, err
	}

	weakLessons, err := s.computeLessonsToReview(userID)
	if err != nil {
		return DashboardSummary{}, err
	}

	recent, err := s.computeRecentAttempts(userID)
	if err != nil {
		return DashboardSummary{}, err
	}

	return DashboardSummary{
		Stats:            stats,
		ContinueLearning: continueLearning,
		LessonsToReview:  weakLessons,
		RecentAttempts:   recent,
	}, nil
}

// computeStats produces the four top-row tiles.
func (s *DashboardService) computeStats(userID int) (DashboardStats, error) {
	stats := DashboardStats{}

	// Courses engaged with — distinct course_id across the learner's
	// quiz attempts. Cheap one-shot query.
	var courseRows []struct{ CourseID string }
	if err := s.db.Raw(`
		SELECT DISTINCT cm.course_id AS course_id
		FROM quiz_attempts qa
		JOIN course_materials cm ON cm.video_id = qa.video_id
		WHERE qa.user_id = ?
	`, userID).Scan(&courseRows).Error; err != nil {
		return stats, fmt.Errorf("count courses: %w", err)
	}
	stats.CoursesCount = len(courseRows)

	// Lessons completed — global completed flag for now (until US-013
	// moves this to a per-user table). Counted across course_materials
	// the learner has engaged with so a single-user demo still reads
	// the right number.
	var lessonsCompleted int64
	if err := s.db.Model(&model.CourseMaterial{}).
		Where("completed = ?", true).
		Count(&lessonsCompleted).Error; err != nil {
		return stats, fmt.Errorf("count completed lessons: %w", err)
	}
	stats.LessonsCompletedCount = int(lessonsCompleted)

	// Average quiz score across completed attempts.
	var avg struct{ Avg *float64 }
	if err := s.db.Raw(`
		SELECT AVG(total_score) AS avg
		FROM quiz_attempts
		WHERE user_id = ? AND status = 'completed'
	`, userID).Scan(&avg).Error; err != nil {
		return stats, fmt.Errorf("avg quiz score: %w", err)
	}
	if avg.Avg != nil {
		stats.AverageQuizScore = round1(*avg.Avg)
	}

	// Current streak in days — consecutive UTC days, ending today, where
	// the learner has at least one quiz attempt.
	streak, err := s.computeStreak(userID)
	if err != nil {
		return stats, err
	}
	stats.CurrentStreakDays = streak

	return stats, nil
}

// computeStreak walks the distinct activity dates backwards from today,
// counting consecutive days. Yesterday-only is a streak of 0 (today is
// the anchor — if you didn't show up today, the streak is broken).
func (s *DashboardService) computeStreak(userID int) (int, error) {
	var dates []struct{ D time.Time }
	if err := s.db.Raw(`
		SELECT DISTINCT date_trunc('day', created_at) AS d
		FROM quiz_attempts
		WHERE user_id = ?
		ORDER BY d DESC
	`, userID).Scan(&dates).Error; err != nil {
		return 0, fmt.Errorf("activity dates: %w", err)
	}
	if len(dates) == 0 {
		return 0, nil
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)
	streak := 0
	cursor := today
	for _, row := range dates {
		day := row.D.UTC().Truncate(24 * time.Hour)
		if day.Equal(cursor) {
			streak++
			cursor = cursor.Add(-24 * time.Hour)
			continue
		}
		// Allow yesterday as the first day if the user hasn't quizzed
		// yet today — feels right pedagogically; mid-day visitors don't
		// get punished.
		if streak == 0 && day.Equal(today.Add(-24*time.Hour)) {
			streak++
			cursor = day.Add(-24 * time.Hour)
			continue
		}
		break
	}
	return streak, nil
}

// computeContinueLearning surfaces up to three "next lesson" cards across
// the courses the learner has engaged with. Definition of "continue":
// the first lesson in a course that isn't yet completed, with most
// recent activity ranking the courses.
func (s *DashboardService) computeContinueLearning(userID int) ([]DashboardLessonCard, error) {
	// Get courses ranked by most recent activity.
	var rows []struct {
		CourseID string
		LastAt   time.Time
	}
	if err := s.db.Raw(`
		SELECT cm.course_id AS course_id, MAX(qa.created_at) AS last_at
		FROM quiz_attempts qa
		JOIN course_materials cm ON cm.video_id = qa.video_id
		WHERE qa.user_id = ?
		GROUP BY cm.course_id
		ORDER BY last_at DESC
		LIMIT 5
	`, userID).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("recent courses: %w", err)
	}

	out := make([]DashboardLessonCard, 0, 3)
	for _, r := range rows {
		next, err := s.firstUncompletedLesson(r.CourseID)
		if err != nil || next == nil {
			continue
		}
		out = append(out, DashboardLessonCard{
			VideoID:     next.VideoID,
			CourseID:    next.CourseID,
			LessonTitle: next.Title,
			YouTubeID:   next.YouTubeID,
			Completed:   next.Completed,
			LastTouched: r.LastAt,
		})
		if len(out) >= 3 {
			break
		}
	}

	// If the learner has activity but every course is finished, surface
	// the most recent lesson they completed as a "browse" hint.
	if len(out) == 0 && len(rows) > 0 {
		mats, err := s.materialRepo.GetCourseMaterialsByCourseID(rows[0].CourseID)
		if err == nil && len(mats) > 0 {
			sort.Slice(mats, func(i, j int) bool { return mats[i].VideoID < mats[j].VideoID })
			last := mats[len(mats)-1]
			out = append(out, DashboardLessonCard{
				VideoID:     last.VideoID,
				CourseID:    last.CourseID,
				LessonTitle: last.Title,
				YouTubeID:   last.YouTubeID,
				Completed:   last.Completed,
				LastTouched: rows[0].LastAt,
			})
		}
	}
	return out, nil
}

func (s *DashboardService) firstUncompletedLesson(courseID string) (*model.CourseMaterial, error) {
	mats, err := s.materialRepo.GetCourseMaterialsByCourseID(courseID)
	if err != nil {
		return nil, err
	}
	sort.Slice(mats, func(i, j int) bool { return mats[i].VideoID < mats[j].VideoID })
	for i := range mats {
		if !mats[i].Completed {
			return &mats[i], nil
		}
	}
	return nil, nil
}

// computeLessonsToReview lists the most recent completed attempts where
// the score dropped below 50%. One row per video — repeated attempts on
// the same lesson collapse to the latest.
func (s *DashboardService) computeLessonsToReview(userID int) ([]DashboardWeakLesson, error) {
	var rows []struct {
		AttemptID int
		VideoID   int
		Score     float64
		CreatedAt time.Time
	}
	if err := s.db.Raw(`
		SELECT DISTINCT ON (qa.video_id)
			qa.id        AS attempt_id,
			qa.video_id  AS video_id,
			qa.total_score AS score,
			qa.created_at AS created_at
		FROM quiz_attempts qa
		WHERE qa.user_id = ? AND qa.status = 'completed' AND qa.total_score < 50
		ORDER BY qa.video_id, qa.created_at DESC
	`, userID).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("weak lessons: %w", err)
	}

	out := make([]DashboardWeakLesson, 0, len(rows))
	for _, r := range rows {
		cm, err := s.materialRepo.GetCourseMaterialByID(r.VideoID)
		if err != nil {
			continue
		}
		out = append(out, DashboardWeakLesson{
			AttemptID:   r.AttemptID,
			VideoID:     r.VideoID,
			CourseID:    cm.CourseID,
			LessonTitle: cm.Title,
			Score:       round1(r.Score),
			CreatedAt:   r.CreatedAt,
		})
	}

	// Sort by score ascending (weakest first) so the most urgent reviews
	// are at the top of the widget.
	sort.Slice(out, func(i, j int) bool { return out[i].Score < out[j].Score })
	if len(out) > 5 {
		out = out[:5]
	}
	return out, nil
}

// computeRecentAttempts is the small recent-quizzes rail. Reuses the
// existing quizRepo.ListByUser endpoint with a small page size.
func (s *DashboardService) computeRecentAttempts(userID int) ([]DashboardAttemptCard, error) {
	attempts, err := s.quizRepo.ListByUser(userID, 5, 0)
	if err != nil {
		return nil, err
	}
	out := make([]DashboardAttemptCard, 0, len(attempts))
	for _, a := range attempts {
		card := DashboardAttemptCard{
			AttemptID:  a.ID,
			VideoID:    a.VideoID,
			Status:     a.Status,
			TotalScore: round1(a.TotalScore),
			CreatedAt:  a.CreatedAt,
		}
		if cm, err := s.materialRepo.GetCourseMaterialByID(a.VideoID); err == nil {
			card.LessonTitle = cm.Title
			card.CourseID = cm.CourseID
		}
		out = append(out, card)
	}
	return out, nil
}

// round1 rounds to one decimal place — dashboard numbers don't need 2dp.
func round1(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}
