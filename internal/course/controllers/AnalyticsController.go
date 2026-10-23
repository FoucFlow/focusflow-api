package controllers

import (
	"focusflow-api/internal/course/models"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type AnalyticsController struct {
	DB *gorm.DB
}

// GetCourseOverview returns summary statistics for the course platform
func (ac *AnalyticsController) GetCourseOverview(c *gin.Context) {
	var overview models.CourseOverview

	ac.DB.Model(&models.Course{}).Count(&overview.TotalCourses)
	ac.DB.Model(&models.Course{}).Where("course_visibility = ?", "public").Count(&overview.PublicCourses)
	ac.DB.Model(&models.Course{}).Where("course_visibility != ?", "public").Count(&overview.PrivateCourses)
	ac.DB.Model(&models.Enrollment{}).Count(&overview.TotalEnrollments)

	if overview.TotalCourses > 0 {
		overview.AvgEnrollments = float64(overview.TotalEnrollments) / float64(overview.TotalCourses)
	}

	c.JSON(http.StatusOK, overview)
}

// GetPopularCourses returns the courses with the most enrollments
func (ac *AnalyticsController) GetPopularCourses(c *gin.Context) {
	limitStr := c.DefaultQuery("limit", "10")
	limit, _ := strconv.Atoi(limitStr)

	var popularCourses []models.PopularCourse

	ac.DB.Table("courses").
		Select("courses.course_id, courses.course_title, courses.course_visibility, courses.user_id, categories.category_name, COUNT(enrollments.id) as enrollment_count").
		Joins("LEFT JOIN categories ON courses.course_category_id = categories.id").
		Joins("LEFT JOIN enrollments ON courses.course_id = enrollments.course_id").
		Group("courses.course_id, categories.category_name").
		Order("enrollment_count DESC").
		Limit(limit).
		Scan(&popularCourses)

	c.JSON(http.StatusOK, popularCourses)
}

// GetCourseGrowth returns time-series data of course creation
func (ac *AnalyticsController) GetCourseGrowth(c *gin.Context) {
	period := c.DefaultQuery("period", "monthly")

	// Default to last 12 months if not specified
	fromStr := c.Query("from")
	var from time.Time
	if fromStr != "" {
		from, _ = time.Parse(time.RFC3339, fromStr)
	} else {
		from = time.Now().AddDate(-1, 0, 0)
	}

	var dateFormat string
	switch period {
	case "daily":
		dateFormat = "YYYY-MM-DD" // Postgres syntax for TO_CHAR
	case "weekly":
		dateFormat = "IYYY-IW"
	case "monthly":
		dateFormat = "YYYY-MM"
	default:
		dateFormat = "YYYY-MM"
	}

	var growth []models.TimeSeriesPoint
	ac.DB.Table("courses").
		Select("TO_CHAR(created_at, ?) as period, COUNT(*) as count", dateFormat).
		Where("created_at >= ?", from).
		Group("period").
		Order("period").
		Scan(&growth)

	c.JSON(http.StatusOK, growth)
}

// GetEnrollmentTrends returns time-series data of enrollments
func (ac *AnalyticsController) GetEnrollmentTrends(c *gin.Context) {
	period := c.DefaultQuery("period", "monthly")

	fromStr := c.Query("from")
	var from time.Time
	if fromStr != "" {
		from, _ = time.Parse(time.RFC3339, fromStr)
	} else {
		from = time.Now().AddDate(-1, 0, 0)
	}

	var dateFormat string
	switch period {
	case "daily":
		dateFormat = "YYYY-MM-DD"
	case "weekly":
		dateFormat = "IYYY-IW"
	case "monthly":
		dateFormat = "YYYY-MM"
	default:
		dateFormat = "YYYY-MM"
	}

	var trends []models.TimeSeriesPoint
	ac.DB.Table("enrollments").
		Select("TO_CHAR(enrolled_at, ?) as period, COUNT(*) as count", dateFormat).
		Where("enrolled_at >= ?", from).
		Group("period").
		Order("period").
		Scan(&trends)

	c.JSON(http.StatusOK, trends)
}

// GetEnrollmentsByCategory returns enrollment distribution across categories
func (ac *AnalyticsController) GetEnrollmentsByCategory(c *gin.Context) {
	var categoryEnrollments []models.CategoryEnrollment

	ac.DB.Table("categories").
		Select("categories.id as category_id, categories.category_name, COUNT(DISTINCT courses.course_id) as course_count, COUNT(enrollments.id) as enrollment_count").
		Joins("LEFT JOIN courses ON courses.course_category_id = categories.id").
		Joins("LEFT JOIN enrollments ON courses.course_id = enrollments.course_id").
		Group("categories.id, categories.category_name").
		Order("enrollment_count DESC").
		Scan(&categoryEnrollments)

	c.JSON(http.StatusOK, categoryEnrollments)
}

// GetTeacherPerformance returns teachers ranked by their courses' enrollments
func (ac *AnalyticsController) GetTeacherPerformance(c *gin.Context) {
	limitStr := c.DefaultQuery("limit", "10")
	limit, _ := strconv.Atoi(limitStr)

	var teacherPerformance []models.TeacherPerformance

	ac.DB.Table("courses").
		Select("courses.user_id, COUNT(DISTINCT courses.course_id) as total_courses, COUNT(enrollments.id) as total_enrollments").
		Joins("LEFT JOIN enrollments ON courses.course_id = enrollments.course_id").
		Group("courses.user_id").
		Order("total_enrollments DESC").
		Limit(limit).
		Scan(&teacherPerformance)

	for i := range teacherPerformance {
		if teacherPerformance[i].TotalCourses > 0 {
			teacherPerformance[i].AvgEnrollments = float64(teacherPerformance[i].TotalEnrollments) / float64(teacherPerformance[i].TotalCourses)
		}
	}

	c.JSON(http.StatusOK, teacherPerformance)
}
