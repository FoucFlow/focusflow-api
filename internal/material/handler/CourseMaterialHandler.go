// Package handler implements the HTTP transport layer for the Course Material Service.
// Handlers parse incoming requests, delegate to the service layer, and format responses.
package handler

import (
	"focusflow-api/internal/material/model"
	"focusflow-api/internal/material/services"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// CourseMaterialHandler handles HTTP requests for course material CRUD operations.
type CourseMaterialHandler struct {
	service *services.CourseMaterialService
}

// NewCourseMaterialHandler creates a new handler with the given course material service.
func NewCourseMaterialHandler(service *services.CourseMaterialService) *CourseMaterialHandler {
	return &CourseMaterialHandler{
		service: service,
	}
}

// CreateCourseMaterials godoc
// @Summary      Create course materials
// @Description  Accepts a list of course materials and saves them to the database
// @Tags         CourseMaterial
// @Accept       json
// @Produce      json
// @Param        materials  body      []model.CourseMaterial  true  "List of course materials"
// @Success      201        {object}  map[string]interface{}
// @Failure      400        {object}  map[string]interface{}
// @Failure      500        {object}  map[string]interface{}
// @Router       /course-materials [post]
func (h *CourseMaterialHandler) CreateCourseMaterials(c *gin.Context) {
	var cms []model.CourseMaterial

	// Bind and validate the JSON request body into a slice of course materials.
	if err := c.ShouldBindJSON(&cms); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid request body",
			"details": err.Error(),
		})
		return
	}

	for _, cm := range cms {
		if !CanEditCourse(c, cm.CourseID) {
			forbidCourse(c)
			return
		}
	}

	// Delegate validation and persistence to the service layer.
	saved, err := h.service.CreateCourseMaterials(cms)
	if err != nil {
		slog.Error("failed to create course materials",
			"error", err,
			"request_id", c.GetString("request_id"),
		)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "failed to create course materials",
			"details": err.Error(),
		})
		return
	}

	// Return the created resources with a 201 status.
	c.JSON(http.StatusCreated, gin.H{
		"message": "course materials created successfully",
		"count":   len(saved),
		"data":    saved,
	})
}

// GetCourseMaterialByID godoc
// @Summary      Get a course material by video ID
// @Description  Retrieves a single course material by its unique video ID
// @Tags         CourseMaterial
// @Produce      json
// @Param        id   path      int  true  "Video ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  map[string]interface{}
// @Failure      404  {object}  map[string]interface{}
// @Router       /course-materials/{id} [get]
func (h *CourseMaterialHandler) GetCourseMaterialByID(c *gin.Context) {
	// Parse the video ID from the URL path parameter.
	videoID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid video ID",
			"details": "video_id must be a valid integer",
		})
		return
	}

	// Delegate retrieval to the service layer.
	cm, err := h.service.GetCourseMaterialByID(videoID)
	if err != nil {
		slog.Warn("course material not found",
			"video_id", videoID,
			"error", err,
			"request_id", c.GetString("request_id"),
		)
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "course material not found",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "course material retrieved successfully",
		"data":    cm,
	})
}

// GetCourseMaterialsByCourseID godoc
// @Summary      Get course materials by course ID
// @Description  Retrieves all course materials that belong to a specific course
// @Tags         CourseMaterial
// @Produce      json
// @Param        courseId  path      string  true  "Course ID"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  map[string]interface{}
// @Failure      500      {object}  map[string]interface{}
// @Router       /course-materials/course/{courseId} [get]
func (h *CourseMaterialHandler) GetCourseMaterialsByCourseID(c *gin.Context) {
	// Extract the course ID from the URL path parameter.
	courseID := c.Param("courseId")
	if courseID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid course ID",
			"details": "course_id is required",
		})
		return
	}

	// Delegate retrieval to the service layer.
	materials, err := h.service.GetCourseMaterialsByCourseID(courseID)
	if err != nil {
		slog.Error("failed to retrieve course materials",
			"course_id", courseID,
			"error", err,
			"request_id", c.GetString("request_id"),
		)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "failed to retrieve course materials",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "course materials retrieved successfully",
		"count":   len(materials),
		"data":    materials,
	})
}

// UpdateCourseMaterial godoc
// @Summary      Update a course material
// @Description  Updates an existing course material identified by its video ID
// @Tags         CourseMaterial
// @Accept       json
// @Produce      json
// @Param        id        path      int                    true  "Video ID"
// @Param        material  body      model.CourseMaterial   true  "Updated course material"
// @Success      200       {object}  map[string]interface{}
// @Failure      400       {object}  map[string]interface{}
// @Failure      404       {object}  map[string]interface{}
// @Failure      500       {object}  map[string]interface{}
// @Router       /course-materials/{id} [put]
func (h *CourseMaterialHandler) UpdateCourseMaterial(c *gin.Context) {
	// Parse the video ID from the URL path parameter.
	videoID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid video ID",
			"details": "video_id must be a valid integer",
		})
		return
	}

	var cm model.CourseMaterial

	// Bind and validate the JSON request body.
	if err := c.ShouldBindJSON(&cm); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid request body",
			"details": err.Error(),
		})
		return
	}

	// Ensure the path parameter matches the body's video ID.
	cm.VideoID = videoID

	// Must own the lesson's current course, and the course it moves to
	existing, err := h.service.GetCourseMaterialByID(videoID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "course material not found"})
		return
	}
	if !CanEditCourse(c, existing.CourseID) || (cm.CourseID != "" && !CanEditCourse(c, cm.CourseID)) {
		forbidCourse(c)
		return
	}

	// Delegate validation and update to the service layer.
	updated, err := h.service.UpdateCourseMaterial(cm)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "failed to update course material",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "course material updated successfully",
		"data":    updated,
	})
}

// DeleteCourseMaterial godoc
// @Summary      Delete a course material
// @Description  Deletes a course material by its video ID
// @Tags         CourseMaterial
// @Produce      json
// @Param        id   path      int  true  "Video ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  map[string]interface{}
// @Failure      404  {object}  map[string]interface{}
// @Router       /course-materials/{id} [delete]

func (h *CourseMaterialHandler) DeleteCourseMaterial(c *gin.Context) {
	// Parse the video ID from the URL path parameter.
	videoID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid video ID",
			"details": "video_id must be a valid integer",
		})
		return
	}

	// Delegate deletion to the service layer.
	if existing, err := h.service.GetCourseMaterialByID(videoID); err == nil && !CanEditCourse(c, existing.CourseID) {
		forbidCourse(c)
		return
	}

	if err := h.service.DeleteCourseMaterial(videoID); err != nil {
		slog.Warn("failed to delete course material",
			"video_id", videoID,
			"error", err,
			"request_id", c.GetString("request_id"),
		)
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "failed to delete course material",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "course material deleted successfully",
	})
}
