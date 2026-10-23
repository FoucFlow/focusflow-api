package handler

import (
	"errors"

	"focusflow-api/internal/auth"

	"github.com/gin-gonic/gin"
)

// sessionUserID is the signed-in user's ID. It replaces the old user_id
// query/body parameters, which let anyone act as any user.
func sessionUserID(c *gin.Context) (int, error) {
	if id := c.GetInt(auth.CtxUserID); id > 0 {
		return id, nil
	}
	return 0, errors.New("unauthenticated")
}

// CanEditCourse reports whether the signed-in user may change the lessons of
// courseID (the course owner or an admin). main wires the real check.
var CanEditCourse = func(c *gin.Context, courseID string) bool { return false }

func forbidCourse(c *gin.Context) {
	c.JSON(403, gin.H{"error": "You can only change lessons of your own courses."})
}
