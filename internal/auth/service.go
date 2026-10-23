package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInvalidCredentials = errors.New("These credentials do not match our records.")
	ErrEmailTaken         = errors.New("The email has already been taken.")
	ErrInvalidResetToken  = errors.New("This password reset token is invalid.")
	ErrNoSession          = errors.New("unauthenticated")
)

const (
	bcryptCost         = 12
	resetTokenTTL      = 60 * time.Minute
	verifyLinkTTL      = 60 * time.Minute
	activityUpdateStep = 5 * time.Minute // write last_activity at most this often
)

// dummyHash lets unknown-email logins cost the same as wrong-password ones
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("focusflow-timing-guard"), bcryptCost)

type Service struct {
	db         *gorm.DB
	appKey     []byte
	sessionTTL time.Duration
}

func NewService(db *gorm.DB, appKey string, sessionTTL time.Duration) *Service {
	return &Service{db: db, appKey: []byte(appKey), sessionTTL: sessionTTL}
}

// ── passwords ────────────────────────────────────────────────────────────

func HashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	return string(h), err
}

// CheckPassword accepts Go ($2a$) and Laravel ($2y$) bcrypt hashes.
func CheckPassword(hash *string, pw string) bool {
	if hash == nil || *hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(*hash), []byte(pw)) == nil
}

// ── users ────────────────────────────────────────────────────────────────

func normaliseEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func (s *Service) Register(name, email, password string) (*User, error) {
	email = normaliseEmail(email)
	var n int64
	s.db.Model(&User{}).Where("email = ?", email).Count(&n)
	if n > 0 {
		return nil, ErrEmailTaken
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	u := &User{Name: strings.TrimSpace(name), Email: email, Password: &hash, Role: RoleStudent}
	if err := s.db.Create(u).Error; err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return nil, ErrEmailTaken
		}
		return nil, err
	}
	return u, nil
}

func (s *Service) Authenticate(email, password string) (*User, error) {
	var u User
	if err := s.db.Where("email = ?", normaliseEmail(email)).First(&u).Error; err != nil {
		// Spend the same time as a real check so emails can't be probed by timing
		bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, ErrInvalidCredentials
	}
	if !CheckPassword(u.Password, password) {
		return nil, ErrInvalidCredentials
	}
	return &u, nil
}

func (s *Service) UserByID(id int) (*User, error) {
	var u User
	if err := s.db.First(&u, id).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Service) UserByEmail(email string) (*User, error) {
	var u User
	if err := s.db.Where("email = ?", normaliseEmail(email)).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

// SetPassword changes the password and signs out every other session.
func (s *Service) SetPassword(u *User, password string, keepSessionID int) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(u).Update("password", hash).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ? AND id <> ?", u.ID, keepSessionID).Delete(&Session{}).Error
	})
}

// EnsureAdmin creates the seeded admin account when it does not exist yet.
func (s *Service) EnsureAdmin(email, password string) (bool, error) {
	if email == "" || password == "" {
		return false, nil
	}
	if _, err := s.UserByEmail(email); err == nil {
		return false, nil
	}
	hash, err := HashPassword(password)
	if err != nil {
		return false, err
	}
	now := time.Now()
	u := &User{Name: "FocusFlow Admin", Email: normaliseEmail(email), Password: &hash, Role: RoleAdmin, EmailVerifiedAt: &now}
	return true, s.db.Create(u).Error
}

// ── sessions ─────────────────────────────────────────────────────────────

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// CreateSession returns the raw token for the cookie; only its hash is stored.
func (s *Service) CreateSession(userID int, ip, userAgent string) (string, *Session, error) {
	token := randomToken(32)
	now := time.Now()
	sess := &Session{
		UserID: userID, TokenHash: sha256Hex(token), IPAddress: ip, UserAgent: userAgent,
		LastActivity: now, ExpiresAt: now.Add(s.sessionTTL),
	}
	if err := s.db.Create(sess).Error; err != nil {
		return "", nil, err
	}
	return token, sess, nil
}

// ResolveSession finds the user for a token and slides the expiry forward.
func (s *Service) ResolveSession(token string) (*User, *Session, error) {
	if token == "" {
		return nil, nil, ErrNoSession
	}
	var sess Session
	if err := s.db.Where("token_hash = ? AND expires_at > ?", sha256Hex(token), time.Now()).First(&sess).Error; err != nil {
		return nil, nil, ErrNoSession
	}
	u, err := s.UserByID(sess.UserID)
	if err != nil {
		return nil, nil, ErrNoSession
	}
	if now := time.Now(); now.Sub(sess.LastActivity) > activityUpdateStep {
		sess.LastActivity, sess.ExpiresAt = now, now.Add(s.sessionTTL)
		s.db.Model(&sess).Updates(map[string]any{"last_activity": sess.LastActivity, "expires_at": sess.ExpiresAt})
	}
	return u, &sess, nil
}

func (s *Service) DeleteSession(token string) {
	if token != "" {
		s.db.Where("token_hash = ?", sha256Hex(token)).Delete(&Session{})
	}
}

// PurgeExpired removes dead sessions and reset tokens (called hourly).
func (s *Service) PurgeExpired() {
	s.db.Where("expires_at < ?", time.Now()).Delete(&Session{})
	s.db.Where("created_at < ?", time.Now().Add(-resetTokenTTL)).Delete(&PasswordResetToken{})
}

// ── password reset ───────────────────────────────────────────────────────

// CreateResetToken returns a raw token for the email link, or "" when the
// email is unknown (callers answer the same either way).
func (s *Service) CreateResetToken(email string) (string, *User, error) {
	u, err := s.UserByEmail(email)
	if err != nil {
		return "", nil, nil
	}
	token := randomToken(32)
	row := PasswordResetToken{Email: u.Email, TokenHash: sha256Hex(token), CreatedAt: time.Now()}
	err = s.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error
	return token, u, err
}

func (s *Service) ResetPassword(email, token, password string) error {
	email = normaliseEmail(email)
	var row PasswordResetToken
	if err := s.db.Where("email = ?", email).First(&row).Error; err != nil {
		return ErrInvalidResetToken
	}
	if time.Since(row.CreatedAt) > resetTokenTTL ||
		subtle.ConstantTimeCompare([]byte(row.TokenHash), []byte(sha256Hex(token))) != 1 {
		return ErrInvalidResetToken
	}
	u, err := s.UserByEmail(email)
	if err != nil {
		return ErrInvalidResetToken
	}
	if err := s.SetPassword(u, password, 0); err != nil {
		return err
	}
	s.db.Delete(&row)
	return nil
}

// ── email verification (signed links, like Laravel's) ────────────────────

func emailHash(email string) string {
	h := sha1.Sum([]byte(email))
	return hex.EncodeToString(h[:])
}

func (s *Service) sign(parts ...string) string {
	m := hmac.New(sha256.New, s.appKey)
	m.Write([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(m.Sum(nil))
}

// VerificationPath is /verify-email/{id}/{hash}?expires=..&signature=..
func (s *Service) VerificationPath(u *User) string {
	exp := strconv.FormatInt(time.Now().Add(verifyLinkTTL).Unix(), 10)
	id, h := strconv.Itoa(u.ID), emailHash(u.Email)
	return fmt.Sprintf("/verify-email/%s/%s?expires=%s&signature=%s", id, h, exp, s.sign("verify", id, h, exp))
}

func (s *Service) VerifyEmail(id, hash, expires, signature string) error {
	exp, err := strconv.ParseInt(expires, 10, 64)
	if err != nil || time.Now().Unix() > exp ||
		!hmac.Equal([]byte(signature), []byte(s.sign("verify", id, hash, expires))) {
		return errors.New("This verification link is invalid or has expired.")
	}
	uid, _ := strconv.Atoi(id)
	u, err := s.UserByID(uid)
	if err != nil || emailHash(u.Email) != hash {
		return errors.New("This verification link is invalid.")
	}
	if u.EmailVerifiedAt == nil {
		now := time.Now()
		return s.db.Model(u).Update("email_verified_at", now).Error
	}
	return nil
}

// ── OAuth state (stateless, signed, 10 minutes) ──────────────────────────

func (s *Service) NewOAuthState() string {
	nonce, exp := randomToken(12), strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10)
	return nonce + "." + exp + "." + s.sign("oauth", nonce, exp)
}

func (s *Service) CheckOAuthState(state string) bool {
	p := strings.Split(state, ".")
	if len(p) != 3 {
		return false
	}
	exp, err := strconv.ParseInt(p[1], 10, 64)
	return err == nil && time.Now().Unix() <= exp && hmac.Equal([]byte(p[2]), []byte(s.sign("oauth", p[0], p[1])))
}

// UpsertGoogleUser links by Google ID, then by email; new accounts have no
// password (Laravel gave them the password "password").
func (s *Service) UpsertGoogleUser(googleID, email, name, avatar string, emailVerified bool) (*User, error) {
	email = normaliseEmail(email)
	var u User
	err := s.db.Where("google_id = ?", googleID).First(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = s.db.Where("email = ?", email).First(&u).Error
	}
	now := time.Now()
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		u = User{Name: name, Email: email, GoogleID: &googleID, Role: RoleStudent}
		if avatar != "" {
			u.Avatar = &avatar
		}
		if emailVerified {
			u.EmailVerifiedAt = &now
		}
		return &u, s.db.Create(&u).Error
	case err != nil:
		return nil, err
	}
	updates := map[string]any{"google_id": googleID}
	if u.Avatar == nil && avatar != "" {
		updates["avatar"] = avatar
	}
	if u.EmailVerifiedAt == nil && emailVerified {
		updates["email_verified_at"] = now
	}
	return &u, s.db.Model(&u).Updates(updates).Error
}
