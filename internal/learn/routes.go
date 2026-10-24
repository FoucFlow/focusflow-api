package learn

import (
	"focusflow-api/internal/auth"

	"github.com/gin-gonic/gin"
)

// Routes registers the course builder and learner flow under /api/learn.
// Every route needs a signed-in user; creator-only checks happen per handler.
func (h *Handler) Routes(r *gin.Engine) {
	// Direct file upload from the browser: signed token instead of a session
	up := r.Group("/api/learn/upload", h.uploadCORS())
	up.OPTIONS("/:id", func(*gin.Context) {})
	up.POST("/:id", h.DirectUpload)

	g := r.Group("/api/learn", auth.AuthMiddleware())

	g.GET("/me", h.Me)
	g.GET("/browse", h.Browse)
	g.GET("/categories", h.Categories)

	g.GET("/courses/:id", h.GetCourse)
	g.POST("/courses/:id/join", h.Join)
	g.GET("/courses/:id/materials", h.ListMaterials)
	g.POST("/courses/:id/materials", h.AddMaterial)
	g.POST("/courses/:id/upload-url", h.UploadURL)
	g.POST("/courses/:id/organise", h.Organise)
	g.POST("/courses/:id/units", h.AddUnit)
	g.PUT("/courses/:id/units/order", h.ReorderUnits)
	g.POST("/courses/:id/publish", h.Publish)
	g.POST("/courses/:id/unpublish", h.Unpublish)

	g.DELETE("/materials/:mid", h.DeleteMaterial)
	g.POST("/materials/:mid/retry", h.RetryMaterial)

	g.GET("/units/:uid", h.GetUnit)
	g.PUT("/units/:uid", h.UpdateUnit)
	g.DELETE("/units/:uid", h.DeleteUnit)
	g.POST("/units/:uid/complete", h.CompleteUnit)
	g.POST("/units/:uid/concepts", h.AddConcept)
	g.PUT("/units/:uid/concepts/order", h.ReorderConcepts)
	g.PUT("/concepts/:cid", h.UpdateConcept)
	g.DELETE("/concepts/:cid", h.DeleteConcept)
}
