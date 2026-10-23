package repository

import (
	"focusflow-api/internal/material/model"
	"gorm.io/gorm"
)

type LearningAnalyticsRepository struct {
	DB *gorm.DB
}

func NewLearningAnalyticsRepository(db *gorm.DB) *LearningAnalyticsRepository {
	return &LearningAnalyticsRepository{DB: db}
}

func (r *LearningAnalyticsRepository) GetMaterialStats() (model.MaterialStats, error) {
	var stats model.MaterialStats

	r.DB.Table("course_materials").Count(&stats.TotalMaterials)
	r.DB.Table("lesson_questions").Count(&stats.TotalQuestions)
	r.DB.Table("reflections").Count(&stats.TotalReflections)
	r.DB.Table("quiz_attempts").Select("COUNT(DISTINCT quiz_id)").Count(&stats.TotalQuizzes)

	return stats, nil
}

func (r *LearningAnalyticsRepository) GetQuizPerformance(limit int) ([]model.QuizPerformance, error) {
	var performances []model.QuizPerformance

	// For simplicity, we just aggregate all quiz attempts and group by CourseID or QuizID
	// Here grouping by quiz_id
	err := r.DB.Table("quiz_attempts").
		Select("quiz_id as course_id, COUNT(*) as total_attempts, AVG(score) as average_score").
		Group("quiz_id").
		Order("total_attempts DESC").
		Limit(limit).
		Scan(&performances).Error

	return performances, err
}
