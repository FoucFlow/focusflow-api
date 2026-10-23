// Package routes registers HTTP routes and binds them to handlers.
package routes

import (
	"focusflow-api/internal/material/handler"

	"github.com/gin-gonic/gin"
)

// SetupReflectionRoutes mounts the Feynman-technique reflection endpoints onto
// the given /api router group. Three endpoints are exposed:
//
//	POST /api/course-materials/:id/reflection                    — submit a reflection
//	GET  /api/course-materials/:id/reflections?user_id=...       — full history (oldest first)
//	GET  /api/course-materials/:id/reflection/latest?user_id=... — most-recent reflection
func SetupReflectionRoutes(api *gin.RouterGroup, h *handler.ReflectionHandler) {
	materials := api.Group("/course-materials")
	{
		materials.POST("/:id/reflection", h.SubmitReflection)
		materials.GET("/:id/reflections", h.GetReflectionHistory)
		materials.GET("/:id/reflection/latest", h.GetLatestReflection)
	}
}
