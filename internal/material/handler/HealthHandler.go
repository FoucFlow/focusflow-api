// Package handler implements the HTTP transport layer for the Course Material Service.
package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// healthResponse is the standard response envelope for the health check endpoint.
type healthResponse struct {
	Status    string `json:"status" example:"healthy"`
	Service   string `json:"service" example:"focusflow-api"`
	Timestamp string `json:"timestamp" example:"2026-02-24T10:00:00Z"`
}

// HealthCheck godoc
// @Summary      Health check
// @Description  Returns the health status of the Course Material Service
// @Tags         Health
// @Produce      json
// @Success      200  {object}  healthResponse
// @Router       /health [get]
func HealthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, healthResponse{
		Status:    "healthy",
		Service:   "focusflow-api",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}
