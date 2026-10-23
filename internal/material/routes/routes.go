package routes

import (
	middleware "focusflow-api/internal/auth"
	"focusflow-api/internal/material/handler"

	"github.com/gin-gonic/gin"
)

// SetupRoutes registers the course-material module under /api. Every route
// needs a signed-in user: handlers take the user from the session.
func SetupRoutes(
	r *gin.Engine,
	searchCourseMaterialHandler *handler.SearchCourseMaterialHandler,
	courseMaterialHandler *handler.CourseMaterialHandler,
	takeawayHandler *handler.TakeawayHandler,
	quizHandler *handler.QuizHandler,
	dashboardHandler *handler.DashboardHandler,
	reflectionHandler *handler.ReflectionHandler,
	analyticsHandler *handler.LearningAnalyticsHandler,
) {
	api := r.Group("/api", middleware.AuthMiddleware())

	SetupSearchRoutes(api, searchCourseMaterialHandler)
	SetupCourseMaterialRoutes(api, courseMaterialHandler)
	SetupTakeawayRoutes(api, takeawayHandler)
	SetupQuizRoutes(api, quizHandler)
	SetupDashboardRoutes(api, dashboardHandler)
	SetupReflectionRoutes(api, reflectionHandler)
	SetupLearningAnalyticsRoutes(api, analyticsHandler)
}
