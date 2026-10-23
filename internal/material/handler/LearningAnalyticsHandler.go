package handler

import (
	"focusflow-api/internal/material/services"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

type LearningAnalyticsHandler struct {
	service *services.LearningAnalyticsService
}

func NewLearningAnalyticsHandler(service *services.LearningAnalyticsService) *LearningAnalyticsHandler {
	return &LearningAnalyticsHandler{service: service}
}

func (h *LearningAnalyticsHandler) GetMaterialStats(c *gin.Context) {
	stats, err := h.service.GetMaterialStats()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve material stats"})
		return
	}
	c.JSON(http.StatusOK, stats)
}

func (h *LearningAnalyticsHandler) GetQuizPerformance(c *gin.Context) {
	limitStr := c.DefaultQuery("limit", "10")
	limit, _ := strconv.Atoi(limitStr)

	perf, err := h.service.GetQuizPerformance(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve quiz performance"})
		return
	}
	c.JSON(http.StatusOK, perf)
}
