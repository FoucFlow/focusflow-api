// Package routes registers HTTP routes and binds them to handlers.
package routes

import (
	"focusflow-api/internal/material/handler"

	"github.com/gin-gonic/gin"
)

// SetupTakeawayRoutes mounts the active-recall endpoints (US-001) onto the
// given /api router group. Three endpoints are exposed:
//
//	POST /api/course-materials/:id/takeaway              — submit + mark complete
//	GET  /api/course-materials/:id/takeaway?user_id=...  — read prior takeaway
//	GET  /api/course-materials/course/:courseId/takeaways?user_id=...
//	                                                     — list takeaways for a course
func SetupTakeawayRoutes(api *gin.RouterGroup, h *handler.TakeawayHandler) {
	materials := api.Group("/course-materials")
	{
		materials.POST("/:id/takeaway", h.SubmitTakeaway)
		materials.GET("/:id/takeaway", h.GetTakeaway)
		materials.GET("/course/:courseId/takeaways", h.GetTakeawaysForCourse)
	}
}
