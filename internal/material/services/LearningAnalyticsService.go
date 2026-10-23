package services

import (
	"focusflow-api/internal/material/model"
	"focusflow-api/internal/material/repository"
)

type LearningAnalyticsService struct {
	repo *repository.LearningAnalyticsRepository
}

func NewLearningAnalyticsService(repo *repository.LearningAnalyticsRepository) *LearningAnalyticsService {
	return &LearningAnalyticsService{repo: repo}
}

func (s *LearningAnalyticsService) GetMaterialStats() (model.MaterialStats, error) {
	return s.repo.GetMaterialStats()
}

func (s *LearningAnalyticsService) GetQuizPerformance(limit int) ([]model.QuizPerformance, error) {
	if limit <= 0 {
		limit = 10
	}
	return s.repo.GetQuizPerformance(limit)
}
