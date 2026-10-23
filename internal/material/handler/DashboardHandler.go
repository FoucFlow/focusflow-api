// Package handler implements the HTTP transport layer for the Course Material Service.
package handler

import (
	"focusflow-api/internal/material/services"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// DashboardHandler exposes the single aggregation endpoint used by the
// /dashboard page. Read-only; never calls Gemini.
type DashboardHandler struct {
	service *services.DashboardService
}

// NewDashboardHandler binds the handler to its service.
func NewDashboardHandler(service *services.DashboardService) *DashboardHandler {
	return &DashboardHandler{service: service}
}

// Summary godoc
// @Summary      Aggregated learner dashboard
// @Description  One round-trip that returns the four stats tiles, the Continue Learning rail, the Lessons-to-Review widget, and the Recent Attempts rail.
// @Tags         Dashboard
// @Produce      json
// @Param        user_id  query  int  true  "User ID"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  map[string]interface{}
// @Router       /dashboard/summary [get]
func (h *DashboardHandler) Summary(c *gin.Context) {
	userID, err := sessionUserID(c)
	if err != nil || userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_id query param required"})
		return
	}
	summary, err := h.service.Summary(userID)
	if err != nil {
		slog.Error("dashboard summary failed",
			"user_id", userID,
			"error", err,
			"request_id", c.GetString("request_id"),
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": summary})
}
