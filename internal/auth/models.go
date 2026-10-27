package auth

import "time"

const (
	RoleStudent = "student"
	RoleTeacher = "teacher"
	RoleAdmin   = "admin"
)

var validRoles = map[string]bool{RoleStudent: true, RoleTeacher: true, RoleAdmin: true}

// User replaces the Laravel users table. JSON matches what Laravel returned,
// so the frontend keeps working; password and google_id are never serialised.
type User struct {
	ID       int     `json:"id" gorm:"primaryKey"`
	Name     string  `json:"name" gorm:"size:255;not null"`
	Username *string `json:"username" gorm:"size:255;uniqueIndex"`
	Email    string  `json:"email" gorm:"size:255;not null;uniqueIndex"`
	Role     string  `json:"role" gorm:"size:16;not null;default:student;index"`
	// Plan (F4.2): free or pro. Pro raises course, material and AI limits;
	// PlanUntil ends it (nil = no end). Set by admins until payments exist.
	Plan            string     `json:"plan" gorm:"size:16;not null;default:free"`
	PlanUntil       *time.Time `json:"plan_until"`
	EmailVerifiedAt *time.Time `json:"email_verified_at"`
	Bio             *string    `json:"bio" gorm:"type:text"`
	Avatar          *string    `json:"avatar" gorm:"size:1024"`
	Location        *string    `json:"location" gorm:"size:255"`
	Website         *string    `json:"website" gorm:"size:255"`
	Password        *string    `json:"-" gorm:"size:255"` // nil for Google-only accounts
	GoogleID        *string    `json:"-" gorm:"size:255;uniqueIndex"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// Summary is the small {id,name,email} object Laravel returned on login.
func (u *User) Summary() map[string]any {
	return map[string]any{"id": u.ID, "name": u.Name, "email": u.Email}
}

// Session is one signed-in browser. Only the SHA-256 of the cookie token is
// stored, so a database leak does not leak usable sessions.
type Session struct {
	ID           int       `gorm:"primaryKey"`
	UserID       int       `gorm:"not null;index"`
	TokenHash    string    `gorm:"size:64;not null;uniqueIndex"`
	IPAddress    string    `gorm:"size:45"`
	UserAgent    string    `gorm:"type:text"`
	LastActivity time.Time `gorm:"not null;index"`
	ExpiresAt    time.Time `gorm:"not null;index"`
	CreatedAt    time.Time
}

// PasswordResetToken: one active token per email, stored hashed, 60 minutes.
type PasswordResetToken struct {
	Email     string    `gorm:"primaryKey;size:255"`
	TokenHash string    `gorm:"size:64;not null"`
	CreatedAt time.Time `gorm:"not null"`
}

type RoleChangeLog struct {
	ID        int        `json:"id" gorm:"primaryKey"`
	UserID    int        `json:"user_id" gorm:"not null;index"`
	ChangedBy int        `json:"changed_by" gorm:"not null;index"`
	OldRole   string     `json:"old_role" gorm:"size:16;not null"`
	NewRole   string     `json:"new_role" gorm:"size:16;not null"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	User      *UserBrief `json:"user,omitempty" gorm:"foreignKey:UserID"`
	Changer   *UserBrief `json:"changed_by_user,omitempty" gorm:"foreignKey:ChangedBy"`
}

// UserBrief is the users table seen as {id,name,email} (for preloads).
type UserBrief struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (UserBrief) TableName() string { return "users" }

// Models lists every table this package owns, for AutoMigrate.
func Models() []any {
	return []any{&User{}, &Session{}, &PasswordResetToken{}, &RoleChangeLog{}}
}
