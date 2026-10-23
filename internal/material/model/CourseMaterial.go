// Package model defines the domain entities and repository contracts
// for the FocusFlow Course Material Service.
package model

import "time"

// CourseMaterial represents a video-based learning resource that belongs to a specific course.
// Each material maps a YouTube video to a lesson within the FocusFlow platform.
type CourseMaterial struct {
	VideoID      int        `json:"video_id" gorm:"primaryKey;autoIncrement" example:"1"`
	CourseID     string     `json:"course_id" gorm:"not null;index" example:"course-101"`
	YouTubeID    string     `json:"youtube_id" example:"dQw4w9WgXcQ"`
	Title        string     `json:"title" example:"Introduction to Go"`
	Description  string     `json:"description" example:"A beginner-friendly lesson on Go programming"`
	Duration     string     `json:"duration" example:"15:30"`
	UploadDate   time.Time  `json:"upload_date" example:"2026-01-15T10:00:00Z"`
	LessonID     string     `json:"lesson_id" example:"lesson-001"`
	ThumbnailURL string     `json:"thumbnail_url" example:"https://img.youtube.com/vi/abc123/0.jpg"`
	Completed    bool       `json:"completed" gorm:"default:false" example:"false"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

// YouTubeSearchResult represents a single video item returned from the YouTube Data API.
// Fields marked with `json:"-"` are internal and excluded from API responses.
type YouTubeSearchResult struct {
	YouTubeID    string    `json:"youtube_id" example:"dQw4w9WgXcQ"`
	Title        string    `json:"title" example:"Learn Go Programming"`
	Description  string    `json:"description" example:"A complete tutorial on Go"`
	ThumbnailURL string    `json:"thumbnail_url" example:"https://img.youtube.com/vi/dQw4w9WgXcQ/mqdefault.jpg"`
	ChannelName  string    `json:"channel_name" example:"Tech Academy"`
	PublishedAt  time.Time `json:"published_at" example:"2026-01-15T10:00:00Z"`
	Thumbnail    string    `json:"-"`
	VideoID      string    `json:"-"`
}

// CourseMaterialRepository defines the persistence contract for course material operations.
// Any concrete implementation (e.g. PostgreSQL, in-memory) must satisfy this interface.
type CourseMaterialRepository interface {
	// CreateCourseMaterial persists a single course material record and returns the saved entity.
	CreateCourseMaterial(courseMaterial *CourseMaterial) (CourseMaterial, error)

	// GetCourseMaterialByID retrieves a single course material by its video ID.
	GetCourseMaterialByID(videoID int) (CourseMaterial, error)

	// GetCourseMaterialsByCourseID retrieves all course materials that belong to a given course.
	GetCourseMaterialsByCourseID(courseID string) ([]CourseMaterial, error)

	// UpdateCourseMaterial updates an existing course material record and returns the updated entity.
	UpdateCourseMaterial(courseMaterial *CourseMaterial) (CourseMaterial, error)

	// DeleteCourseMaterial removes a course material by its video ID.
	DeleteCourseMaterial(videoID int) error
}
