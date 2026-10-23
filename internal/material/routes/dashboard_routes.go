// Package routes registers HTTP routes and binds them to handlers.
package routes

import (
	"focusflow-api/internal/material/handler"

	"github.com/gin-gonic/gin"
)

// SetupDashboardRoutes mounts the dashboard aggregation endpoint.
//
//	GET /api/dashboard/summary?user_id=<int>
func SetupDashboardRoutes(api *gin.RouterGroup, h *handler.DashboardHandler) {
	dash := api.Group("/dashboard")
	{
		dash.GET("/summary", h.Summary)
	}
}
