package routes

import (
	middleware "focusflow-api/internal/auth"
	"focusflow-api/internal/material/handler"
	"github.com/gin-gonic/gin"
)

// SetupLearningAnalyticsRoutes registers the analytics routes
func SetupLearningAnalyticsRoutes(api *gin.RouterGroup, analyticsHandler *handler.LearningAnalyticsHandler) {
	// Protected analytics routes
	protected := api.Group("")
	protected.Use(middleware.RequireRole("admin"))
	{
		analytics := protected.Group("/analytics")
		analytics.GET("/materials/stats", analyticsHandler.GetMaterialStats)
		analytics.GET("/materials/performance", analyticsHandler.GetQuizPerformance)
	}
}
