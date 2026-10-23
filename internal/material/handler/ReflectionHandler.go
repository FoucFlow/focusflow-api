// Package handler implements the HTTP transport layer for the Course Material Service.
package handler

import (
	"focusflow-api/internal/auth"
	"focusflow-api/internal/material/services"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// ReflectionHandler exposes the Feynman-technique reflection endpoints.
//
// When service is nil (Gemini disabled at boot), SubmitReflection replies
// HTTP 503. GetReflectionHistory and GetLatestReflection remain fully
// functional without Gemini because they only read from the database.
type ReflectionHandler struct {
	service *services.ReflectionService
}

// NewReflectionHandler creates a new handler bound to a ReflectionService.
// Passing a nil service puts the submit endpoint into fallback mode (503).
func NewReflectionHandler(service *services.ReflectionService) *ReflectionHandler {
	return &ReflectionHandler{service: service}
}

// disabled returns true when the handler was constructed without a service.
func (h *ReflectionHandler) disabled() bool { return h.service == nil }

func (h *ReflectionHandler) reply503(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"error": "reflection feature is disabled on this deployment (GEMINI_API_KEY not configured)",
	})
}

// submitReflectionRequest is the JSON body accepted by SubmitReflection.
type submitReflectionRequest struct {
	UserID int    `json:"-"` // set from the session, never from the body
	Body   string `json:"body"    binding:"required" example:"A goroutine is a lightweight thread managed by the Go runtime…"`
}

// SubmitReflection godoc
// @Summary      Submit a Feynman-technique reflection for a lesson
// @Description  Validates the explanation body (30..3000 chars), sends {lesson context, video URL, learner explanation} to Gemini, persists the scored result, and returns the full reflection row. Multiple submissions per (user, lesson) are allowed — each creates a new row so the learner can track improvement over time.
// @Tags         Reflection
// @Accept       json
// @Produce      json
// @Param        id    path      int                        true  "Video ID (lesson)"
// @Param        body  body      submitReflectionRequest    true  "Reflection payload"
// @Success      201   {object}  map[string]interface{}
// @Failure      400   {object}  map[string]interface{}
// @Failure      404   {object}  map[string]interface{}
// @Failure      422   {object}  map[string]interface{}
// @Failure      503   {object}  map[string]interface{}
// @Router       /course-materials/{id}/reflection [post]
func (h *ReflectionHandler) SubmitReflection(c *gin.Context) {
	if h.disabled() {
		h.reply503(c)
		return
	}

	videoID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid video ID",
			"details": "video_id must be a valid integer",
		})
		return
	}

	var req submitReflectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid request body",
			"details": err.Error(),
		})
		return
	}
	req.UserID = c.GetInt(auth.CtxUserID)

	reflection, err := h.service.Submit(c.Request.Context(), req.UserID, videoID, req.Body)
	if err != nil {
		status := http.StatusUnprocessableEntity
		msg := err.Error()
		lower := strings.ToLower(msg)
		switch {
		case contains(msg, "lesson not found"):
			status = http.StatusNotFound
		case contains(msg, "must be a positive integer"):
			status = http.StatusBadRequest
		case strings.Contains(lower, "service unavailable") ||
			strings.Contains(lower, "gemini service unavailable"):
			reason, message := classifyGenerateError(err)
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error":   "reflection analysis unavailable",
				"reason":  reason,
				"message": message,
				"details": msg,
			})
			return
		}
		c.JSON(status, gin.H{"error": msg})
		return
	}

	// 202 Accepted: row is persisted with status="evaluating"; a background
	// goroutine will flip it to "completed" or "failed" once Gemini returns.
	// The frontend polls the history/latest endpoints to observe the change.
	c.JSON(http.StatusAccepted, gin.H{
		"message": "reflection accepted; analysis in progress",
		"data":    reflection,
	})
}

// GetReflectionHistory godoc
// @Summary      Get the user's full reflection history for a lesson
// @Description  Returns every reflection the given user has submitted for the lesson, ordered oldest-first. An empty array is a normal response when no reflections have been submitted yet.
// @Tags         Reflection
// @Produce      json
// @Param        id       path   int  true  "Video ID (lesson)"
// @Param        user_id  query  int  true  "User ID"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  map[string]interface{}
// @Router       /course-materials/{id}/reflections [get]
func (h *ReflectionHandler) GetReflectionHistory(c *gin.Context) {
	if h.disabled() {
		h.reply503(c)
		return
	}

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

	results, err := h.service.GetHistory(userID, videoID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"count": len(results),
		"data":  results,
	})
}

// GetLatestReflection godoc
// @Summary      Get the user's most-recent reflection for a lesson
// @Description  Returns the latest reflection the given user submitted for the lesson, or {data: null} if none exists. A null response is not an error — it means the learner hasn't submitted a reflection yet.
// @Tags         Reflection
// @Produce      json
// @Param        id       path   int  true  "Video ID (lesson)"
// @Param        user_id  query  int  true  "User ID"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  map[string]interface{}
// @Router       /course-materials/{id}/reflection/latest [get]
func (h *ReflectionHandler) GetLatestReflection(c *gin.Context) {
	if h.disabled() {
		h.reply503(c)
		return
	}

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

	ref, err := h.service.GetLatest(userID, videoID)
	if err != nil {
		// Not-found is a normal case — the learner simply hasn't submitted yet.
		c.JSON(http.StatusOK, gin.H{"data": nil})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": ref})
}
