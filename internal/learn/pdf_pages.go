package learn

import (
	"context"
	"net/http"
	"strconv"

	"focusflow-api/internal/auth"
	"focusflow-api/internal/platform/storage"

	"github.com/gin-gonic/gin"
)

// PDF pages (F3.2): in a slides course a PDF is cut into page ranges like a
// deck and drawn page by page in the browser. The file is streamed through
// the API so only the creator and enrolled learners can read it.

// pdfPages returns the page count, reading the file once if it is unknown.
func (s *Service) pdfPages(ctx context.Context, m *Material) (int, error) {
	if m.TotalPages > 0 {
		return m.TotalPages, nil
	}
	data, err := storage.Get(ctx, m.StorageKey, s.limits.MaxFileBytes)
	if err != nil {
		return 0, err
	}
	n := pdfPageCount(data)
	if n > 0 {
		m.TotalPages = n
		s.db.Model(m).Update("total_pages", n)
	}
	return n, nil
}

// GET /api/learn/materials/:mid/file: the PDF itself, for its creator or a
// learner enrolled in the published course.
func (h *Handler) MaterialFile(c *gin.Context) {
	mid, ok := paramID(c, "mid")
	if !ok {
		return
	}
	var m Material
	if err := h.s.db.First(&m, mid).Error; err != nil || m.Kind != KindPDF {
		fail(c, http.StatusNotFound, "File not found.")
		return
	}
	course, err := h.s.course(m.CourseID)
	me := c.GetInt(auth.CtxUserID)
	if err != nil || (course.UserId != me && !isAdmin(c) && (course.CourseStatus != "published" || !h.s.isEnrolled(me, m.CourseID))) {
		fail(c, http.StatusNotFound, "File not found.")
		return
	}
	data, err := storage.Get(c.Request.Context(), m.StorageKey, h.s.limits.MaxFileBytes)
	if err != nil {
		fail(c, http.StatusNotFound, "File not found.")
		return
	}
	c.Header("Cache-Control", "private, max-age=3600")
	c.Header("Content-Length", strconv.Itoa(len(data)))
	c.Data(http.StatusOK, "application/pdf", data)
}

// GET /api/learn/materials/:mid/page-count
func (h *Handler) PDFPageCount(c *gin.Context) {
	m, ok := h.ownedMaterial(c)
	if !ok {
		return
	}
	if m.Kind != KindPDF {
		fail(c, http.StatusUnprocessableEntity, "Only PDF files have pages.")
		return
	}
	n, err := h.s.pdfPages(c.Request.Context(), m)
	if err != nil || n == 0 {
		fail(c, http.StatusUnprocessableEntity, "Could not read the pages of this file.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"material_id": m.ID, "pages": n}})
}
