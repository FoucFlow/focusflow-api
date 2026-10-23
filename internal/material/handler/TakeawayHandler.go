// Package handler implements the HTTP transport layer for the Course Material Service.
package handler

import (
	"focusflow-api/internal/auth"
	"focusflow-api/internal/material/services"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// TakeawayHandler exposes the active-recall endpoints described in US-001.
type TakeawayHandler struct {
	service *services.TakeawayService
}

// NewTakeawayHandler creates a new handler bound to a TakeawayService.
func NewTakeawayHandler(service *services.TakeawayService) *TakeawayHandler {
	return &TakeawayHandler{service: service}
}

// submitTakeawayRequest is the JSON body accepted by SubmitTakeaway.
type submitTakeawayRequest struct {
	UserID int    `json:"-"` // set from the session, never from the body
	Body   string `json:"body" binding:"required" example:"Goroutines are lightweight threads managed by the Go runtime."`
}

// SubmitTakeaway godoc
// @Summary      Submit a lesson takeaway and mark the lesson complete
// @Description  Validates the takeaway body (30..1000 chars), upserts the takeaway for (user, lesson), and atomically flips the CourseMaterial to completed. Operationalises the US-001 active-recall gate.
// @Tags         Takeaway
// @Accept       json
// @Produce      json
// @Param        id    path      int                    true  "Video ID (lesson)"
// @Param        body  body      submitTakeawayRequest  true  "Takeaway payload"
// @Success      201   {object}  map[string]interface{}
// @Failure      400   {object}  map[string]interface{}
// @Failure      404   {object}  map[string]interface{}
// @Failure      422   {object}  map[string]interface{}
// @Router       /course-materials/{id}/takeaway [post]
func (h *TakeawayHandler) SubmitTakeaway(c *gin.Context) {
	videoID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid video ID",
			"details": "video_id must be a valid integer",
		})
		return
	}

	var req submitTakeawayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid request body",
			"details": err.Error(),
		})
		return
	}
	req.UserID = c.GetInt(auth.CtxUserID)

	material, takeaway, err := h.service.SubmitTakeaway(req.UserID, videoID, req.Body)
	if err != nil {
		// Validation-level errors are 422 so the frontend can keep the modal open.
		// Not-found errors return 404.
		status := http.StatusUnprocessableEntity
		msg := err.Error()
		switch {
		case contains(msg, "lesson not found"):
			status = http.StatusNotFound
		case contains(msg, "must be a positive integer"):
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"error": msg})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"message":  "takeaway submitted; lesson marked complete",
		"takeaway": takeaway,
		"material": material,
	})
}

// GetTakeaway godoc
// @Summary      Get the user's prior takeaway for a lesson
// @Description  Returns the takeaway the given user previously wrote for the given lesson, or null if none exists yet.
// @Tags         Takeaway
// @Produce      json
// @Param        id       path      int  true  "Video ID (lesson)"
// @Param        user_id  query     int  true  "User ID"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  map[string]interface{}
// @Router       /course-materials/{id}/takeaway [get]
func (h *TakeawayHandler) GetTakeaway(c *gin.Context) {
	videoID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid video ID",
			"details": "video_id must be a valid integer",
		})
		return
	}
	userID, err := sessionUserID(c)
	if err != nil || userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid user_id",
			"details": "user_id query parameter must be a positive integer",
		})
		return
	}

	t, err := h.service.GetTakeaway(userID, videoID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// An empty takeaway (zero-valued struct) is a valid "no entry yet" response.
	if t.ID == 0 {
		c.JSON(http.StatusOK, gin.H{"data": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": t})
}

// GetTakeawaysForCourse godoc
// @Summary      List the user's takeaways for a course
// @Description  Returns every takeaway the given user has submitted for any lesson in the given course.
// @Tags         Takeaway
// @Produce      json
// @Param        courseId  path      string  true  "Course ID"
// @Param        user_id   query     int     true  "User ID"
// @Success      200       {object}  map[string]interface{}
// @Failure      400       {object}  map[string]interface{}
// @Router       /course-materials/course/{courseId}/takeaways [get]
func (h *TakeawayHandler) GetTakeawaysForCourse(c *gin.Context) {
	courseID := c.Param("courseId")
	if courseID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "course_id is required"})
		return
	}
	userID, err := sessionUserID(c)
	if err != nil || userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid user_id",
			"details": "user_id query parameter must be a positive integer",
		})
		return
	}

	results, err := h.service.GetTakeawaysForCourse(userID, courseID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"count": len(results),
		"data":  results,
	})
}

// contains is a tiny case-sensitive substring helper kept local to avoid
// importing "strings" only for this one site.
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	if len(haystack) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
