package auth

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"focusflow-api/internal/platform/storage"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const maxAvatarBytes = 2 << 20 // 2 MB, as before

var avatarTypes = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp",
}

func ok(c *gin.Context, status int, data any, message ...string) {
	body := gin.H{"status": "success", "data": data}
	if len(message) > 0 {
		body["message"] = message[0]
	}
	c.JSON(status, body)
}

func fail(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"status": "error", "message": message})
}

// targetUser loads :id and allows it only for that user or an admin.
func (h *Handler) targetUser(c *gin.Context) (*User, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		fail(c, http.StatusNotFound, "User not found")
		return nil, false
	}
	me := CurrentUser(c)
	if me.ID != id && me.Role != RoleAdmin {
		fail(c, http.StatusForbidden, "You can only change your own profile.")
		return nil, false
	}
	u, err := h.svc.UserByID(id)
	if err != nil {
		fail(c, http.StatusNotFound, "User not found")
		return nil, false
	}
	return u, true
}

// GET /api/user
func (h *Handler) Me(c *gin.Context) { ok(c, http.StatusOK, CurrentUser(c)) }

// GET /api/user/lookup?email=
func (h *Handler) Lookup(c *gin.Context) {
	email := c.Query("email")
	if email == "" {
		fail(c, http.StatusBadRequest, "Email parameter is required")
		return
	}
	u, err := h.svc.UserByEmail(email)
	if err != nil {
		fail(c, http.StatusNotFound, "User not found")
		return
	}
	ok(c, http.StatusOK, gin.H{"id": u.ID, "name": u.Name, "email": u.Email})
}

// GET /api/user/:id (now requires login)
func (h *Handler) Show(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	u, err := h.svc.UserByID(id)
	if err != nil {
		fail(c, http.StatusNotFound, "User not found")
		return
	}
	ok(c, http.StatusOK, u)
}

// PUT /api/user/:id: only fields present in the body change.
func (h *Handler) Update(c *gin.Context) {
	u, allowed := h.targetUser(c)
	if !allowed {
		return
	}
	var in struct {
		Name     *string `json:"name"`
		Username *string `json:"username"`
		Bio      *string `json:"bio"`
		Location *string `json:"location"`
		Website  *string `json:"website"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		validationError(c, "name", "The request body must be valid JSON.")
		return
	}
	updates := map[string]any{}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 255 {
			validationError(c, "name", "The name field must be between 1 and 255 characters.")
			return
		}
		updates["name"] = n
	}
	if in.Username != nil {
		un := strings.TrimSpace(*in.Username)
		if un == "" || len(un) > 255 {
			validationError(c, "username", "The username field must be between 1 and 255 characters.")
			return
		}
		var n int64
		h.svc.db.Model(&User{}).Where("username = ? AND id <> ?", un, u.ID).Count(&n)
		if n > 0 {
			validationError(c, "username", "The username has already been taken.")
			return
		}
		updates["username"] = un
	}
	for field, v := range map[string]*string{"bio": in.Bio, "location": in.Location, "website": in.Website} {
		if v == nil {
			continue
		}
		limit := 255
		if field == "bio" {
			limit = 500
		}
		if len(*v) > limit {
			validationError(c, field, "The "+field+" field is too long.")
			return
		}
		if *v == "" {
			updates[field] = nil
		} else {
			updates[field] = *v
		}
	}
	if len(updates) > 0 {
		if err := h.svc.db.Model(u).Updates(updates).Error; err != nil {
			slog.Error("update profile", "error", err)
			fail(c, http.StatusInternalServerError, "Could not update the profile.")
			return
		}
	}
	u, _ = h.svc.UserByID(u.ID)
	ok(c, http.StatusOK, u, "User updated successfully")
}

// POST /api/user/:id/avatar (multipart field "avatar")
func (h *Handler) UploadAvatar(c *gin.Context) {
	u, allowed := h.targetUser(c)
	if !allowed {
		return
	}
	fh, err := c.FormFile("avatar")
	if err != nil {
		validationError(c, "avatar", "The avatar field is required.")
		return
	}
	if fh.Size > maxAvatarBytes {
		validationError(c, "avatar", "The avatar field must not be greater than 2048 kilobytes.")
		return
	}
	f, err := fh.Open()
	if err != nil {
		fail(c, http.StatusBadRequest, "Could not read the upload.")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxAvatarBytes+1))
	if err != nil || len(data) > maxAvatarBytes {
		validationError(c, "avatar", "The avatar field must not be greater than 2048 kilobytes.")
		return
	}
	// Trust the bytes, not the browser's content type
	ctype := http.DetectContentType(data)
	ext, okType := avatarTypes[ctype]
	if !okType {
		validationError(c, "avatar", "The avatar field must be a file of type: jpeg, png, jpg, gif, webp.")
		return
	}
	name := "avatar_" + strconv.Itoa(u.ID) + "_" + uuid.NewString()[:8] + ext
	urlStr, err := storage.Upload(c.Request.Context(), "avatars", name, data, ctype)
	if err != nil {
		slog.Error("avatar upload", "error", err)
		fail(c, http.StatusInternalServerError, "Avatar upload failed.")
		return
	}
	if u.Avatar != nil {
		storage.Delete(c.Request.Context(), *u.Avatar)
	}
	h.svc.db.Model(u).Update("avatar", urlStr)
	u.Avatar = &urlStr
	ok(c, http.StatusOK, u, "Avatar uploaded successfully")
}

// PUT /api/user/:id/password: always the signed-in user, :id is ignored.
func (h *Handler) ChangePassword(c *gin.Context) {
	u := CurrentUser(c)
	var in struct {
		CurrentPassword      string `json:"current_password"`
		Password             string `json:"password"`
		PasswordConfirmation string `json:"password_confirmation"`
	}
	_ = c.ShouldBindJSON(&in)
	switch {
	case len(in.Password) < 8:
		validationError(c, "password", "The password field must be at least 8 characters.")
		return
	case in.Password != in.PasswordConfirmation:
		validationError(c, "password", "The password field confirmation does not match.")
		return
	}
	// Google-only accounts have no password yet and may set one directly
	if u.Password != nil && !CheckPassword(u.Password, in.CurrentPassword) {
		fail(c, http.StatusUnprocessableEntity, "The current password is incorrect.")
		return
	}
	sess, _ := c.Get(CtxSession)
	if err := h.svc.SetPassword(u, in.Password, sess.(*Session).ID); err != nil {
		slog.Error("change password", "error", err)
		fail(c, http.StatusInternalServerError, "Could not change the password.")
		return
	}
	ok(c, http.StatusOK, u, "Password updated successfully")
}

func isNotFound(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }
