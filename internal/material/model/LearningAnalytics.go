package model

// MaterialStats holds general stats about materials
type MaterialStats struct {
	TotalMaterials   int64 `json:"total_materials"`
	TotalQuestions   int64 `json:"total_questions"`
	TotalReflections int64 `json:"total_reflections"`
	TotalQuizzes     int64 `json:"total_quizzes"`
}

// QuizPerformance shows average scores over time or overall
type QuizPerformance struct {
	CourseID      string  `json:"course_id"`
	TotalAttempts int64   `json:"total_attempts"`
	AverageScore  float64 `json:"average_score"`
}
