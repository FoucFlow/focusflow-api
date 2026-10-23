// Package routes registers HTTP routes and binds them to handlers.
package routes

import (
	"focusflow-api/internal/material/handler"

	"github.com/gin-gonic/gin"
)

// SetupQuizRoutes mounts the US-026 endpoints onto the /api router group.
//
// Lesson-scoped quiz endpoints (need a video_id):
//
//	POST /api/course-materials/:id/quiz/prepare    — fire-and-forget background generation
//	GET  /api/course-materials/:id/quiz/status     — cheap poll: are questions ready?
//	POST /api/course-materials/:id/quiz/generate   — synchronous generate-or-fetch (slow path)
//	GET  /api/course-materials/:id/quiz            — questions for the stepper (no answers)
//	POST /api/course-materials/:id/quiz/attempts   — submit; returns 202 with attempt_id
//	GET  /api/course-materials/:id/quiz/latest?user_id=... — latest attempt for a lesson
//
// Attempt-scoped endpoints (need an attempt_id):
//
//	GET  /api/quiz-attempts/:id                    — poll an attempt by id (results page)
func SetupQuizRoutes(api *gin.RouterGroup, h *handler.QuizHandler) {
	q := api.Group("/course-materials/:id/quiz")
	{
		q.POST("/prepare", h.PrepareQuiz)
		q.GET("/status", h.QuizStatus)
		q.POST("/generate", h.GenerateQuiz)
		q.POST("/regenerate", h.RegenerateQuiz)
		q.GET("", h.ListQuiz)
		q.POST("/attempts", h.SubmitAttempt)
		q.GET("/latest", h.LatestAttempt)
	}

	attempts := api.Group("/quiz-attempts")
	{
		attempts.GET("", h.ListAttempts) // history feed
		attempts.GET("/:id", h.GetAttempt)
	}
}
