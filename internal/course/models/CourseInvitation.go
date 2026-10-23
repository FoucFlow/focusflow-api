package models

import "time"

// CourseInvitation represents an invitation for a user to join a course
type CourseInvitation struct {
	ID         int       `gorm:"primaryKey;autoIncrement" json:"id"`
	CourseID   int       `gorm:"index;not null" json:"course_id"`
	SenderID   int       `gorm:"index;not null" json:"sender_id"`
	ReceiverID int       `gorm:"index;not null" json:"receiver_id"`
	Status     string    `gorm:"type:varchar(20);default:'pending'" json:"status"` // pending, accepted, declined
	CreatedAt  time.Time `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt  time.Time `gorm:"autoUpdateTime" json:"updated_at"`

	// Relationships
	Course Course `gorm:"foreignKey:CourseID;references:CourseId" json:"course,omitempty"`
}

// TableName overrides the table name used by CourseInvitation to `course_invitations`
func (CourseInvitation) TableName() string {
	return "course_invitations"
}
