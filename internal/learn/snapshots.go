package learn

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"focusflow-api/internal/platform/storage"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Camera (F2.7): when a test has the camera on, the learner's browser sends a
// still picture every minute or so (never video). Pictures are shown only to
// the course creator, through the API, with the other evidence.

const (
	maxSnapshotBytes = 400 << 10
	maxSnapshots     = 180
	minSnapshotGap   = 20 * time.Second
)

// POST /api/learn/attempts/:aid/snapshots (multipart "image": JPEG or PNG)
func (h *Handler) AddSnapshot(c *gin.Context) {
	a, ok := h.mine(c)
	if !ok {
		return
	}
	if !a.Settings.Camera || a.Status != AttemptInProgress {
		fail(c, http.StatusConflict, "This test does not take camera pictures now.")
		return
	}
	fh, err := c.FormFile("image")
	if err != nil || fh.Size > maxSnapshotBytes {
		fail(c, http.StatusUnprocessableEntity, "Send one JPEG or PNG picture under 400 KB.")
		return
	}
	f, err := fh.Open()
	if err != nil {
		fail(c, http.StatusUnprocessableEntity, "Could not read the picture.")
		return
	}
	data, _ := io.ReadAll(io.LimitReader(f, maxSnapshotBytes+1))
	f.Close()
	mime := http.DetectContentType(data)
	ext := map[string]string{"image/jpeg": ".jpg", "image/png": ".png"}[mime]
	if ext == "" || len(data) > maxSnapshotBytes {
		fail(c, http.StatusUnprocessableEntity, "Send one JPEG or PNG picture under 400 KB.")
		return
	}
	if n := len(a.Snapshots); n >= maxSnapshots || (n > 0 && time.Since(a.Snapshots[n-1].At) < minSnapshotGap) {
		c.Status(http.StatusNoContent) // quietly ignored: too many or too soon
		return
	}
	rnd := make([]byte, 16)
	_, _ = rand.Read(rnd)
	key := fmt.Sprintf("attempts/%d/%s%s", a.ID, hex.EncodeToString(rnd), ext)
	if _, err := storage.UploadKey(c.Request.Context(), key, data, mime); err != nil {
		fail(c, http.StatusInternalServerError, "Could not store the picture.")
		return
	}
	err = h.s.db.Transaction(func(tx *gorm.DB) error {
		var cur Attempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&cur, a.ID).Error; err != nil {
			return err
		}
		cur.Snapshots = append(cur.Snapshots, Snapshot{Key: key, At: time.Now()})
		return tx.Model(&cur).Select("snapshots").Updates(&cur).Error
	})
	if err != nil {
		storage.DeleteKey(context.Background(), key)
		fail(c, http.StatusInternalServerError, "Could not store the picture.")
		return
	}
	c.Status(http.StatusNoContent)
}

// GET /api/learn/attempts/:aid/snapshots/:n: one picture, for the course creator.
func (h *Handler) GetSnapshot(c *gin.Context) {
	a, creator, ok := h.attemptFor(c)
	if !ok {
		return
	}
	if !creator {
		fail(c, http.StatusForbidden, "Only the course creator can see camera pictures.")
		return
	}
	n, err := strconv.Atoi(c.Param("n"))
	if err != nil || n < 0 || n >= len(a.Snapshots) {
		fail(c, http.StatusNotFound, "Picture not found.")
		return
	}
	data, err := storage.Get(c.Request.Context(), a.Snapshots[n].Key, maxSnapshotBytes)
	if err != nil {
		fail(c, http.StatusNotFound, "Picture not found.")
		return
	}
	c.Header("Cache-Control", "private, max-age=300")
	c.Data(http.StatusOK, http.DetectContentType(data), data)
}

// snapshotKeys lists the pictures of the attempts of these units, to delete
// them from storage with the attempts.
func snapshotKeys(tx *gorm.DB, unitIDs []int) []string {
	var attempts []Attempt
	tx.Select("snapshots").Where("unit_id IN ?", unitIDs).Find(&attempts)
	var keys []string
	for _, a := range attempts {
		for _, s := range a.Snapshots {
			keys = append(keys, s.Key)
		}
	}
	return keys
}
