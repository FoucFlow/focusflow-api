package controllers

import (
	"focusflow-api/internal/course/models"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type EnrollmentController struct {
	DB *gorm.DB
}

// EnrollInCourse handles a user enrolling in a public course
func (ec *EnrollmentController) EnrollInCourse(c *gin.Context) {
	courseID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid course ID"})
		return
	}

	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found in context"})
		return
	}
	uid := userID.(int)

	// Check if course exists and is public
	var course models.Course
	if err := ec.DB.First(&course, courseID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "Course not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		}
		return
	}

	if course.CourseVisibility != "public" && course.CourseVisibility != "shared" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Cannot enroll in a private course"})
		return
	}

	if course.UserId == uid {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot enroll in your own course"})
		return
	}

	// Check if already enrolled
	var count int64
	ec.DB.Model(&models.Enrollment{}).Where("course_id = ? AND user_id = ?", courseID, uid).Count(&count)
	if count > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Already enrolled in this course"})
		return
	}

	enrollment := models.Enrollment{
		CourseID: courseID,
		UserID:   uid,
	}

	if err := ec.DB.Create(&enrollment).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to enroll: " + err.Error()})
		return
	}

	c.JSON(http.StatusCreated, enrollment)
}

// UnenrollFromCourse handles a user unenrolling from a course
func (ec *EnrollmentController) UnenrollFromCourse(c *gin.Context) {
	courseID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid course ID"})
		return
	}

	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found in context"})
		return
	}
	uid := userID.(int)

	result := ec.DB.Where("course_id = ? AND user_id = ?", courseID, uid).Delete(&models.Enrollment{})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Enrollment not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Successfully unenrolled"})
}

// GetCourseEnrollments gets all enrollments for a course (owner/admin only)
func (ec *EnrollmentController) GetCourseEnrollments(c *gin.Context) {
	courseID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid course ID"})
		return
	}

	var course models.Course
	if err := ec.DB.First(&course, courseID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Course not found"})
		return
	}

	userID, _ := c.Get("user_id")
	userRole, _ := c.Get("user_role")

	if userRole != "admin" && course.UserId != userID.(int) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Only the course owner or admin can view enrollments"})
		return
	}

	var enrollments []models.Enrollment
	if err := ec.DB.Where("course_id = ?", courseID).Find(&enrollments).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}

	c.JSON(http.StatusOK, enrollments)
}

// GetMyEnrollments gets all courses the current user is enrolled in
func (ec *EnrollmentController) GetMyEnrollments(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User ID not found in context"})
		return
	}
	uid := userID.(int)

	var enrollments []models.Enrollment
	if err := ec.DB.Preload("Course").Preload("Course.Category").Where("user_id = ?", uid).Find(&enrollments).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}

	c.JSON(http.StatusOK, enrollments)
}
