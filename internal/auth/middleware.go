package auth

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// CookieName holds the session token. The Nuxt proxy on Vercel forwards it
// both ways, exactly like it forwarded laravel_session before.
const CookieName = "ff_session"

// Context keys. The course and material modules read these (unchanged from
// when they asked the Laravel service who the user was).
const (
	CtxUserID    = "user_id"
	CtxUserRole  = "user_role"
	CtxUserName  = "user_name"
	CtxUserEmail = "user_email"
	CtxUser      = "auth_user"
	CtxSession   = "auth_session"
)

// defaultService is set once in main via Init, so modules can use the
// package-level middleware without carrying the service around.
var defaultService *Service

func Init(s *Service) { defaultService = s }

// tokenFrom reads the session token from the cookie or "Authorization: Bearer".
func tokenFrom(c *gin.Context) string {
	if t, err := c.Cookie(CookieName); err == nil && t != "" {
		return t
	}
	if h := c.GetHeader("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return ""
}

func setUser(c *gin.Context, u *User, sess *Session) {
	c.Set(CtxUser, u)
	c.Set(CtxSession, sess)
	c.Set(CtxUserID, u.ID)
	c.Set(CtxUserRole, u.Role)
	c.Set(CtxUserName, u.Name)
	c.Set(CtxUserEmail, u.Email)
}

// AuthMiddleware rejects the request with 401 unless a valid session exists.
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		u, sess, err := defaultService.ResolveSession(tokenFrom(c))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"message": "Unauthenticated.", "error": "Unauthorized"})
			return
		}
		setUser(c, u, sess)
		c.Next()
	}
}

// OptionalAuth sets the user when a valid session exists, and never aborts.
func OptionalAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if u, sess, err := defaultService.ResolveSession(tokenFrom(c)); err == nil {
			setUser(c, u, sess)
		}
		c.Next()
	}
}

// RequireRole must run after AuthMiddleware.
func RequireRole(roles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		role := c.GetString(CtxUserRole)
		for _, r := range roles {
			if role == r {
				c.Next()
				return
			}
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"message": "This action is unauthorized.",
			"error":   "Insufficient permissions. Required role: " + strings.Join(roles, " or "),
		})
	}
}

// CurrentUser returns the signed-in user (nil when OptionalAuth found none).
func CurrentUser(c *gin.Context) *User {
	if v, ok := c.Get(CtxUser); ok {
		return v.(*User)
	}
	return nil
}

// ── login rate limit: 5 failures per email+IP per minute (as Laravel) ────

type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

var loginLimiter = &limiter{hits: map[string][]time.Time{}}

const (
	maxLoginAttempts = 5
	loginWindow      = time.Minute
)

// retryAfter returns how long the key must wait, or 0 if it may try now.
func (l *limiter) retryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(key)
	if h := l.hits[key]; len(h) >= maxLoginAttempts {
		return time.Until(h[0].Add(loginWindow))
	}
	return 0
}

func (l *limiter) hit(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(key)
	l.hits[key] = append(l.hits[key], time.Now())
}

func (l *limiter) clear(key string) {
	l.mu.Lock()
	delete(l.hits, key)
	l.mu.Unlock()
}

func (l *limiter) prune(key string) {
	cut := time.Now().Add(-loginWindow)
	h := l.hits[key]
	i := 0
	for i < len(h) && h[i].Before(cut) {
		i++
	}
	if i == len(h) {
		delete(l.hits, key)
	} else {
		l.hits[key] = h[i:]
	}
}
