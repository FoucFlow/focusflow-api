package models

import "time"

// CourseOverview holds aggregate statistics about all courses
type CourseOverview struct {
	TotalCourses     int64   `json:"total_courses"`
	PublicCourses    int64   `json:"public_courses"`
	PrivateCourses   int64   `json:"private_courses"`
	TotalEnrollments int64   `json:"total_enrollments"`
	AvgEnrollments   float64 `json:"avg_enrollments"`
}

// PopularCourse represents a course ranked by enrollment count
type PopularCourse struct {
	CourseID         int    `json:"course_id"`
	CourseTitle      string `json:"course_title"`
	CourseVisibility string `json:"course_visibility"`
	UserId           int    `json:"user_id"`
	CategoryName     string `json:"category_name"`
	EnrollmentCount  int    `json:"enrollment_count"`
}

// TimeSeriesPoint represents a data point in a time-series
type TimeSeriesPoint struct {
	Period string `json:"period"`
	Count  int    `json:"count"`
}

// CategoryEnrollment represents enrollment distribution by category
type CategoryEnrollment struct {
	CategoryID      uint   `json:"category_id"`
	CategoryName    string `json:"category_name"`
	EnrollmentCount int    `json:"enrollment_count"`
	CourseCount     int    `json:"course_count"`
}

// TeacherPerformance represents a teacher ranked by total enrollments
type TeacherPerformance struct {
	UserID           int     `json:"user_id"`
	TotalCourses     int     `json:"total_courses"`
	TotalEnrollments int     `json:"total_enrollments"`
	AvgEnrollments   float64 `json:"avg_enrollments"`
}

// DateRange is a helper for parsing time range query params
type DateRange struct {
	From time.Time
	To   time.Time
}
