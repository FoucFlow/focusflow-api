package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"math"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"focusflow-api/internal/platform/config"
	"focusflow-api/internal/platform/mailer"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type Handler struct {
	svc    *Service
	cfg    *config.Config
	mail   mailer.Mailer
	google *oauth2.Config // nil when Google login is not configured
}

func NewHandler(svc *Service, cfg *config.Config, m mailer.Mailer) *Handler {
	h := &Handler{svc: svc, cfg: cfg, mail: m}
	if cfg.GoogleClientID != "" && cfg.GoogleClientSecret != "" {
		h.google = &oauth2.Config{
			ClientID: cfg.GoogleClientID, ClientSecret: cfg.GoogleClientSecret,
			RedirectURL: cfg.GoogleRedirectURL, Endpoint: google.Endpoint,
			Scopes: []string{"openid", "email", "profile"},
		}
	}
	return h
}

// ── responses ────────────────────────────────────────────────────────────

// validationError answers like Laravel: 422 {message, errors{field:[msg]}}.
func validationError(c *gin.Context, field, msg string) {
	c.JSON(http.StatusUnprocessableEntity, gin.H{"message": msg, "errors": gin.H{field: []string{msg}}})
}

func validEmail(e string) bool {
	a, err := mail.ParseAddress(e)
	return err == nil && a.Address == e && len(e) <= 255
}

func (h *Handler) setSessionCookie(c *gin.Context, token string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(CookieName, token, int(h.cfg.SessionTTL.Seconds()), "/", "", h.cfg.SecureCookies, true)
}

func (h *Handler) clearSessionCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(CookieName, "", -1, "/", "", h.cfg.SecureCookies, true)
}

func (h *Handler) startSession(c *gin.Context, u *User) bool {
	token, _, err := h.svc.CreateSession(u.ID, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		slog.Error("create session", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Could not sign you in. Please try again."})
		return false
	}
	h.setSessionCookie(c, token)
	return true
}

// sendMailAsync never blocks the request on SMTP.
func (h *Handler) sendMailAsync(to, subject, body string) {
	go func() {
		if err := h.mail.Send(to, subject, body); err != nil {
			slog.Error("send mail", "to", to, "subject", subject, "error", err)
		}
	}()
}

func emailLayout(title, intro, buttonText, link, outro string) string {
	return fmt.Sprintf(`<html><body style="font-family:Arial,sans-serif;color:#1f2937">
<h2>%s</h2><p>%s</p>
<p><a href="%s" style="display:inline-block;padding:10px 20px;background:#4F46E5;color:#fff;text-decoration:none;border-radius:6px">%s</a></p>
<p style="font-size:13px;color:#6b7280">Or copy this link into your browser:<br>%s</p>
<p>%s</p><p>FocusFlow</p></body></html>`,
		html.EscapeString(title), intro, link, html.EscapeString(buttonText), html.EscapeString(link), outro)
}

func (h *Handler) sendVerificationEmail(u *User) {
	link := h.cfg.PublicURL + h.svc.VerificationPath(u)
	h.sendMailAsync(u.Email, "Verify your FocusFlow email address", emailLayout(
		"Verify your email address",
		fmt.Sprintf("Hello %s, please confirm this is your email address.", html.EscapeString(u.Name)),
		"Verify email", link, "The link expires in 60 minutes. If you did not create an account, ignore this email."))
}

// ── register / login / logout ────────────────────────────────────────────

type registerInput struct {
	Name                 string `json:"name" form:"name"`
	Email                string `json:"email" form:"email"`
	Password             string `json:"password" form:"password"`
	PasswordConfirmation string `json:"password_confirmation" form:"password_confirmation"`
}

func (h *Handler) Register(c *gin.Context) {
	var in registerInput
	_ = c.ShouldBind(&in)
	in.Email = normaliseEmail(in.Email)
	switch {
	case strings.TrimSpace(in.Name) == "" || len(in.Name) > 255:
		validationError(c, "name", "The name field is required.")
		return
	case !validEmail(in.Email):
		validationError(c, "email", "The email field must be a valid email address.")
		return
	case len(in.Password) < 8:
		validationError(c, "password", "The password field must be at least 8 characters.")
		return
	case in.Password != in.PasswordConfirmation:
		validationError(c, "password", "The password field confirmation does not match.")
		return
	}
	u, err := h.svc.Register(in.Name, in.Email, in.Password)
	if errors.Is(err, ErrEmailTaken) {
		validationError(c, "email", err.Error())
		return
	} else if err != nil {
		slog.Error("register", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Registration failed. Please try again."})
		return
	}
	if !h.startSession(c, u) {
		return
	}
	h.sendVerificationEmail(u)
	c.JSON(http.StatusCreated, gin.H{"status": "success", "message": "Registration successful", "user": u.Summary(), "user_id": u.ID})
}

type loginInput struct {
	Email    string `json:"email" form:"email"`
	Password string `json:"password" form:"password"`
}

func (h *Handler) Login(c *gin.Context) {
	var in loginInput
	_ = c.ShouldBind(&in)
	in.Email = normaliseEmail(in.Email)
	if !validEmail(in.Email) || in.Password == "" {
		validationError(c, "email", "The email and password fields are required.")
		return
	}
	key := in.Email + "|" + c.ClientIP()
	if wait := loginLimiter.retryAfter(key); wait > 0 {
		secs := int(math.Ceil(wait.Seconds()))
		c.Header("Retry-After", fmt.Sprint(secs))
		c.JSON(http.StatusTooManyRequests, gin.H{
			"message": fmt.Sprintf("Too many login attempts. Please try again in %d seconds.", secs),
			"errors":  gin.H{"email": []string{fmt.Sprintf("Too many login attempts. Please try again in %d seconds.", secs)}},
		})
		return
	}
	u, err := h.svc.Authenticate(in.Email, in.Password)
	if err != nil {
		loginLimiter.hit(key)
		validationError(c, "email", ErrInvalidCredentials.Error())
		return
	}
	loginLimiter.clear(key)
	if !h.startSession(c, u) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Logged in successfully", "user": u.Summary(), "user_id": u.ID})
}

func (h *Handler) Logout(c *gin.Context) {
	h.svc.DeleteSession(tokenFrom(c))
	h.clearSessionCookie(c)
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Logged out successfully"})
}

// ── password reset ───────────────────────────────────────────────────────

func (h *Handler) ForgotPassword(c *gin.Context) {
	var in struct {
		Email string `json:"email" form:"email"`
	}
	_ = c.ShouldBind(&in)
	in.Email = normaliseEmail(in.Email)
	if !validEmail(in.Email) {
		validationError(c, "email", "The email field must be a valid email address.")
		return
	}
	token, u, err := h.svc.CreateResetToken(in.Email)
	if err != nil {
		slog.Error("create reset token", "error", err)
	}
	if token != "" && u != nil {
		link := fmt.Sprintf("%s/password-reset/%s?email=%s", h.cfg.FrontendURL, token, url.QueryEscape(u.Email))
		h.sendMailAsync(u.Email, "Reset your FocusFlow password", emailLayout(
			"Reset your password", "You asked to reset your FocusFlow password.",
			"Reset password", link, "The link expires in 60 minutes. If you did not ask for this, ignore this email."))
	}
	// Same answer whether or not the email exists, so accounts can't be probed
	c.JSON(http.StatusOK, gin.H{"status": "If that email is registered, we have emailed a password reset link."})
}

func (h *Handler) ResetPassword(c *gin.Context) {
	var in struct {
		Token                string `json:"token" form:"token"`
		Email                string `json:"email" form:"email"`
		Password             string `json:"password" form:"password"`
		PasswordConfirmation string `json:"password_confirmation" form:"password_confirmation"`
	}
	_ = c.ShouldBind(&in)
	switch {
	case in.Token == "":
		validationError(c, "token", "The token field is required.")
		return
	case len(in.Password) < 8:
		validationError(c, "password", "The password field must be at least 8 characters.")
		return
	case in.Password != in.PasswordConfirmation:
		validationError(c, "password", "The password field confirmation does not match.")
		return
	}
	if err := h.svc.ResetPassword(in.Email, in.Token, in.Password); err != nil {
		validationError(c, "email", ErrInvalidResetToken.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "Your password has been reset."})
}

// ── email verification ───────────────────────────────────────────────────

func (h *Handler) VerifyEmail(c *gin.Context) {
	err := h.svc.VerifyEmail(c.Param("id"), c.Param("hash"), c.Query("expires"), c.Query("signature"))
	if err != nil {
		c.Redirect(http.StatusFound, h.cfg.FrontendURL+"/dashboard?verified=0")
		return
	}
	c.Redirect(http.StatusFound, h.cfg.FrontendURL+"/dashboard?verified=1")
}

func (h *Handler) ResendVerification(c *gin.Context) {
	u := CurrentUser(c)
	if u.EmailVerifiedAt != nil {
		c.JSON(http.StatusOK, gin.H{"status": "already-verified"})
		return
	}
	h.sendVerificationEmail(u)
	c.JSON(http.StatusOK, gin.H{"status": "verification-link-sent"})
}

// ── Google ───────────────────────────────────────────────────────────────

func (h *Handler) GoogleRedirect(c *gin.Context) {
	if h.google == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Google login is not configured."})
		return
	}
	u := h.google.AuthCodeURL(h.svc.NewOAuthState(), oauth2.SetAuthURLParam("prompt", "select_account"))
	c.Redirect(http.StatusFound, u)
}

func (h *Handler) GoogleCallback(c *gin.Context) {
	if h.google == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "Google login is not configured."})
		return
	}
	if !h.svc.CheckOAuthState(c.Query("state")) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid or expired sign-in attempt. Please try again."})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	tok, err := h.google.Exchange(ctx, c.Query("code"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Google sign-in failed. Please try again."})
		return
	}
	resp, err := h.google.Client(ctx, tok).Get("https://openidconnect.googleapis.com/v1/userinfo")
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": "Could not reach Google."})
		return
	}
	defer resp.Body.Close()
	var info struct {
		Sub, Email, Name, Picture string
		EmailVerified             bool `json:"email_verified"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil || info.Sub == "" || info.Email == "" {
		c.JSON(http.StatusBadGateway, gin.H{"message": "Google returned an incomplete profile."})
		return
	}
	if info.Name == "" {
		info.Name = strings.Split(info.Email, "@")[0]
	}
	u, err := h.svc.UpsertGoogleUser(info.Sub, info.Email, info.Name, info.Picture, info.EmailVerified)
	if err != nil {
		slog.Error("google upsert", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Could not sign you in."})
		return
	}
	if !h.startSession(c, u) {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status": "success", "message": "Google authentication successful",
		"user":    gin.H{"id": u.ID, "name": u.Name, "email": u.Email, "avatar": u.Avatar},
		"user_id": u.ID,
	})
}
