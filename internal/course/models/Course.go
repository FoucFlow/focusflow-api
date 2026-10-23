package models

import "time"

type Course struct {
	// 1. Explicitly name the column 'course_id'
	// 2. Add 'autoIncrement' so GORM ignores the 0 and fetches the new ID after creation
	CourseId *int `gorm:"primaryKey;column:course_id;autoIncrement" json:"course_id"`

	CourseTitle      string `json:"course_title"`
	CourseThumb      string `json:"course_thumb"`
	CourseCategoryId int    `gorm:"column:course_category_id" json:"course_category_id"`

	// Ensure this matches the Supabase column 'user_id'
	UserId   int      `gorm:"column:user_id" json:"user_id"`
	Category Category `gorm:"foreignKey:CourseCategoryId;references:ID" json:"category"`

	CourseVisibility  string `json:"course_visibility"`
	CourseDescription string `json:"course_description" gorm:"column:course_description"`
	CourseProgress    int    `json:"course_progress" gorm:"column:course_progress"`

	Enrollments     []Enrollment `json:"enrollments,omitempty" gorm:"-"`
	EnrollmentCount int          `json:"enrollment_count" gorm:"-"`

	// Use pointers or specific types if needed, but time.Time is standard for GORM autoCreateTime/autoUpdateTime
	CreatedAt *time.Time `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt *time.Time `json:"updated_at" gorm:"autoUpdateTime"`
}
