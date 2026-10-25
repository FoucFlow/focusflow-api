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

	// Course builder (FEATURES.md step 1)
	CourseLanguage    string     `json:"course_language" gorm:"size:16;not null;default:en"`
	CourseStatus      string     `json:"course_status" gorm:"size:16;not null;default:draft;index"` // draft | published
	PublishedAt       *time.Time `json:"published_at"`
	RightsConfirmedAt *time.Time `json:"rights_confirmed_at"`                                  // creator confirmed they may share the files
	OrganiseStatus    string     `json:"organise_status" gorm:"size:16;not null;default:idle"` // idle | processing | ready | failed
	OrganiseError     string     `json:"organise_error,omitempty" gorm:"type:text"`
	OrganiseRuns      int        `json:"organise_runs" gorm:"not null;default:0"`
	OrganiseMore      bool       `json:"organise_more"`   // the run in progress only adds units for newly read pages
	OrganiseDone      int        `json:"organise_done"`   // units written so far in the run in progress
	OrganiseTotal     int        `json:"organise_total"`  // units planned (0 while planning)
	ReferencesDone    int        `json:"references_done"` // units searched so far
	ReferencesTotal   int        `json:"references_total"`
	ReferencesStatus  string     `json:"references_status" gorm:"size:16;not null;default:idle"` // idle | processing | ready | failed
	PriceRWF          int        `json:"price_rwf" gorm:"not null;default:0"`                    // always 0 during the free pilot

	Enrollments     []Enrollment `json:"enrollments,omitempty" gorm:"-"`
	EnrollmentCount int          `json:"enrollment_count" gorm:"-"`

	// Use pointers or specific types if needed, but time.Time is standard for GORM autoCreateTime/autoUpdateTime
	CreatedAt *time.Time `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt *time.Time `json:"updated_at" gorm:"autoUpdateTime"`
}
