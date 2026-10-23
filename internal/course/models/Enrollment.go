package models

import "time"

type Enrollment struct {
	ID         uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	CourseID   int       `json:"course_id" gorm:"uniqueIndex:idx_course_user;index;not null"`
	UserID     int       `json:"user_id" gorm:"uniqueIndex:idx_course_user;index;not null"`
	EnrolledAt time.Time `json:"enrolled_at" gorm:"autoCreateTime"`
	Course     Course    `json:"course,omitempty" gorm:"foreignKey:CourseID;references:CourseId"`
}
