package routes

import (
	"focusflow-api/internal/material/handler"

	"github.com/gin-gonic/gin"
)

// SetupCourseMaterialRoutes registers all course material CRUD endpoints under /api/course-materials.
//
// Routes:
//   - POST   /api/course-materials              →  Create one or more course materials
//   - GET    /api/course-materials/:id           →  Get a course material by video ID
//   - GET    /api/course-materials/course/:courseId  →  Get all course materials for a course
//   - PUT    /api/course-materials/:id           →  Update a course material by video ID
//   - DELETE /api/course-materials/:id           →  Delete a course material by video ID
func SetupCourseMaterialRoutes(api *gin.RouterGroup, courseMaterialHandler *handler.CourseMaterialHandler) {
	materials := api.Group("/course-materials")
	{
		materials.POST("", courseMaterialHandler.CreateCourseMaterials)
		materials.GET("/:id", courseMaterialHandler.GetCourseMaterialByID)
		materials.GET("/course/:courseId", courseMaterialHandler.GetCourseMaterialsByCourseID)
		materials.PUT("/:id", courseMaterialHandler.UpdateCourseMaterial)
		materials.DELETE("/:id", courseMaterialHandler.DeleteCourseMaterial)
	}
}
