package routes

import (
	middleware "focusflow-api/internal/auth"
	"focusflow-api/internal/course/controllers"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// CourseRoutes takes the router and the DB to set up all course-related endpoints
func CourseRoutes(r *gin.Engine, db *gorm.DB) {

	// Initialize the controllers with the DB
	courseController := &controllers.CourseService{
		DB: db,
	}
	enrollmentController := &controllers.EnrollmentController{
		DB: db,
	}
	analyticsController := &controllers.AnalyticsController{
		DB: db,
	}
	invitationController := &controllers.InvitationController{
		DB: db,
	}

	// Group the routes for better organization (optional but recommended)
	courseGroup := r.Group("/api")
	{
		// Public routes (or routes with optional auth)
		courseGroup.GET("/course", courseController.GetCourses)
		courseGroup.GET("/course/:id", courseController.GetCourseByID)
	}

	// Protected routes (require auth)
	protectedGroup := r.Group("/api")
	protectedGroup.Use(middleware.AuthMiddleware())
	{
		// Course CRUD — any authenticated user can create a course. Update and
		// delete are also open to any authenticated user because the controllers
		// enforce ownership (only the course owner can modify or remove it).
		protectedGroup.POST("/course", courseController.CreateCourse)
		protectedGroup.PUT("/course/:id", courseController.UpdateCourse)
		protectedGroup.DELETE("/course/:id", courseController.DeleteCourse)

		// Teacher & Admin routes for courses (viewing enrollments)
		teacherAdminGroup := protectedGroup.Group("")
		teacherAdminGroup.Use(middleware.RequireRole("teacher", "admin"))
		{
			teacherAdminGroup.GET("/course/:id/enrollments", enrollmentController.GetCourseEnrollments)
		}

		// Student routes (any authenticated user can enroll)
		protectedGroup.POST("/course/:id/enroll", enrollmentController.EnrollInCourse)
		protectedGroup.DELETE("/course/:id/enroll", enrollmentController.UnenrollFromCourse)
		protectedGroup.GET("/enrollments", enrollmentController.GetMyEnrollments)

		// Invitations and Notifications
		protectedGroup.POST("/course/:id/invite-email", invitationController.InviteUserByEmail)
		protectedGroup.GET("/notifications", invitationController.GetMyNotifications)
		protectedGroup.POST("/invitations/:id/respond", invitationController.RespondToInvite)

		// Admin only routes for analytics
		adminGroup := protectedGroup.Group("/admin")
		adminGroup.Use(middleware.RequireRole("admin"))
		{
			adminGroup.GET("/analytics/courses/overview", analyticsController.GetCourseOverview)
			adminGroup.GET("/analytics/courses/popular", analyticsController.GetPopularCourses)
			adminGroup.GET("/analytics/courses/growth", analyticsController.GetCourseGrowth)
			adminGroup.GET("/analytics/enrollments/trends", analyticsController.GetEnrollmentTrends)
			adminGroup.GET("/analytics/enrollments/categories", analyticsController.GetEnrollmentsByCategory)
			adminGroup.GET("/analytics/teachers/performance", analyticsController.GetTeacherPerformance)
		}
	}
}
