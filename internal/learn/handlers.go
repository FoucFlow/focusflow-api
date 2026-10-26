package learn

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"focusflow-api/internal/auth"
	coursemodels "focusflow-api/internal/course/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Handler struct {
	s              *Service
	publicURL      string          // where browsers reach the API (direct uploads)
	allowedOrigins map[string]bool // frontend sites allowed to upload directly
}

func NewHandler(s *Service, publicURL string, origins []string) *Handler {
	allowed := map[string]bool{}
	for _, o := range origins {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			allowed[o] = true
		}
	}
	return &Handler{s: s, publicURL: strings.TrimRight(publicURL, "/"), allowedOrigins: allowed}
}

func fail(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"message": msg, "error": msg})
}

func paramID(c *gin.Context, name string) (int, bool) {
	id, err := strconv.Atoi(c.Param(name))
	if err != nil || id <= 0 {
		fail(c, http.StatusNotFound, "Not found.")
		return 0, false
	}
	return id, true
}

func isAdmin(c *gin.Context) bool { return c.GetString(auth.CtxUserRole) == auth.RoleAdmin }

// ownedCourse loads :id and allows only its creator (or an admin).
func (h *Handler) ownedCourse(c *gin.Context) (*coursemodels.Course, bool) {
	id, ok := paramID(c, "id")
	if !ok {
		return nil, false
	}
	course, err := h.s.course(id)
	if err != nil {
		fail(c, http.StatusNotFound, "Course not found.")
		return nil, false
	}
	if course.UserId != c.GetInt(auth.CtxUserID) && !isAdmin(c) {
		fail(c, http.StatusForbidden, "Only the course creator can do this.")
		return nil, false
	}
	return course, true
}

// ownedUnit loads :uid and allows only its course creator (or an admin).
func (h *Handler) ownedUnit(c *gin.Context) (*Unit, *coursemodels.Course, bool) {
	uid, ok := paramID(c, "uid")
	if !ok {
		return nil, nil, false
	}
	var u Unit
	if err := h.s.db.First(&u, uid).Error; err != nil {
		fail(c, http.StatusNotFound, "Unit not found.")
		return nil, nil, false
	}
	course, err := h.s.course(u.CourseID)
	if err != nil || (course.UserId != c.GetInt(auth.CtxUserID) && !isAdmin(c)) {
		fail(c, http.StatusForbidden, "Only the course creator can do this.")
		return nil, nil, false
	}
	return &u, course, true
}

// ── materials ────────────────────────────────────────────────────────────

var (
	pdfMagic = []byte("%PDF-")
	zipMagic = []byte("PK\x03\x04")
)

// POST /api/learn/courses/:id/materials
// multipart: file (PDF or .pptx) + optional title, or JSON {kind: video|link, url, title, note}
func (h *Handler) AddMaterial(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	if h.s.limits.MaterialsPerCourse > 0 && h.s.materialCount(*course.CourseId) >= h.s.limits.MaterialsPerCourse {
		fail(c, http.StatusUnprocessableEntity, "The free plan allows "+strconv.Itoa(h.s.limits.MaterialsPerCourse)+" materials per course.")
		return
	}
	me := c.GetInt(auth.CtxUserID)
	if err := h.s.checkAI(me); err != nil {
		fail(c, http.StatusUnprocessableEntity, limitMessage(err))
		return
	}

	if strings.HasPrefix(c.ContentType(), "multipart/") {
		h.uploadFile(c, course, me)
		return
	}

	var in struct {
		Kind  string `json:"kind"`
		URL   string `json:"url"`
		Title string `json:"title"`
		Note  string `json:"note"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "Send a file, or a JSON body with kind, url and title.")
		return
	}
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		fail(c, http.StatusUnprocessableEntity, "Enter a full link starting with https://")
		return
	}
	title := strings.TrimSpace(in.Title)
	var youtubeID string
	switch in.Kind {
	case KindVideo:
		youtubeID = YouTubeID(u)
		if youtubeID == "" {
			fail(c, http.StatusUnprocessableEntity, "Only YouTube video links are supported for videos right now.")
			return
		}
	case KindLink:
	default:
		fail(c, http.StatusUnprocessableEntity, "kind must be video or link.")
		return
	}
	if title == "" {
		title = u.Host + u.Path
	}
	m, err := h.s.AddLink(course, me, in.Kind, clip(title, 255), u.String(), youtubeID, clip(in.Note, 2000))
	if err != nil {
		fail(c, http.StatusInternalServerError, "Could not save the link.")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": m})
}

// uploadFile stores a multipart PDF or .pptx ("file" + optional "title").
func (h *Handler) uploadFile(c *gin.Context, course *coursemodels.Course, me int) {
	if h.s.limits.MaterialsPerCourse > 0 && h.s.materialCount(*course.CourseId) >= h.s.limits.MaterialsPerCourse {
		fail(c, http.StatusUnprocessableEntity, "The free plan allows "+strconv.Itoa(h.s.limits.MaterialsPerCourse)+" materials per course.")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.s.limits.MaxFileBytes+(1<<20))
	fh, err := c.FormFile("file")
	if err != nil {
		fail(c, http.StatusUnprocessableEntity, "Choose a file to upload (PDF or PowerPoint .pptx, up to "+mb(h.s.limits.MaxFileBytes)+").")
		return
	}
	if fh.Size > h.s.limits.MaxFileBytes {
		fail(c, http.StatusUnprocessableEntity, "This file is too large. The limit is "+mb(h.s.limits.MaxFileBytes)+".")
		return
	}
	f, err := fh.Open()
	if err != nil {
		fail(c, http.StatusBadRequest, "Could not read the upload.")
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, h.s.limits.MaxFileBytes+1))
	f.Close()
	if err != nil || int64(len(data)) > h.s.limits.MaxFileBytes {
		fail(c, http.StatusUnprocessableEntity, "This file is too large. The limit is "+mb(h.s.limits.MaxFileBytes)+".")
		return
	}
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	var kind, mime string
	switch {
	case bytes.HasPrefix(data, pdfMagic):
		kind, mime = KindPDF, "application/pdf"
	case bytes.HasPrefix(data, zipMagic) && ext == ".pptx":
		kind, mime = KindSlides, "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ext == ".ppt":
		fail(c, http.StatusUnprocessableEntity, "Old PowerPoint (.ppt) files are not supported. Save it as .pptx and upload again.")
		return
	default:
		fail(c, http.StatusUnprocessableEntity, "Only PDF and PowerPoint (.pptx) files are supported.")
		return
	}
	title := strings.TrimSpace(c.PostForm("title"))
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(fh.Filename), filepath.Ext(fh.Filename))
	}
	m, err := h.s.AddFile(c.Request.Context(), course, me, kind, clip(title, 255), fh.Filename, mime, data)
	if err != nil {
		fail(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": m})
}

// ── direct browser uploads ───────────────────────────────────────────────
// The frontend's proxy (Vercel) caps request bodies at ~4.5 MB, so files go
// straight from the browser to the API with a short-lived signed token.

const uploadTokenTTL = 10 * time.Minute

// POST /api/learn/courses/:id/upload-url → {url, token, expires_at}
func (h *Handler) UploadURL(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	if err := h.s.checkAI(c.GetInt(auth.CtxUserID)); err != nil {
		fail(c, http.StatusUnprocessableEntity, limitMessage(err))
		return
	}
	exp := time.Now().Add(uploadTokenTTL)
	token := h.s.UploadToken(*course.CourseId, c.GetInt(auth.CtxUserID), exp)
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"url":        h.publicURL + "/api/learn/upload/" + strconv.Itoa(*course.CourseId),
		"token":      token,
		"expires_at": exp,
		"max_bytes":  h.s.limits.MaxFileBytes,
	}})
}

// POST /api/learn/upload/:id (header X-Upload-Token): the file itself.
func (h *Handler) DirectUpload(c *gin.Context) {
	id, ok := paramID(c, "id")
	if !ok {
		return
	}
	userID, valid := h.s.CheckUploadToken(c.GetHeader("X-Upload-Token"), id)
	if !valid {
		fail(c, http.StatusUnauthorized, "This upload link has expired. Please try again.")
		return
	}
	course, err := h.s.course(id)
	if err != nil || course.UserId != userID {
		fail(c, http.StatusForbidden, "Only the course creator can do this.")
		return
	}
	h.uploadFile(c, course, userID)
}

// uploadCORS lets the frontend's own sites (and only those) call DirectUpload.
func (h *Handler) uploadCORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && h.allowedOrigins[origin] {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Methods", "POST, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type, X-Upload-Token")
			c.Header("Access-Control-Max-Age", "600")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// GET /api/learn/categories
func (h *Handler) Categories(c *gin.Context) {
	cats := []coursemodels.Category{}
	h.s.db.Order("category_id").Find(&cats)
	c.JSON(http.StatusOK, gin.H{"data": cats})
}

var ytID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// YouTubeID extracts the 11-character id from the usual YouTube URL shapes.
func YouTubeID(u *url.URL) string {
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	host = strings.TrimPrefix(host, "m.")
	var id string
	switch host {
	case "youtu.be":
		id = strings.Trim(u.Path, "/")
	case "youtube.com", "music.youtube.com":
		if v := u.Query().Get("v"); v != "" {
			id = v
		} else {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) == 2 && (parts[0] == "embed" || parts[0] == "shorts" || parts[0] == "live") {
				id = parts[1]
			}
		}
	}
	if ytID.MatchString(id) {
		return id
	}
	return ""
}

// GET /api/learn/courses/:id/materials
func (h *Handler) ListMaterials(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	mats := []Material{}
	h.s.db.Where("course_id = ?", *course.CourseId).Order(materialOrder).Find(&mats)
	c.JSON(http.StatusOK, gin.H{"data": mats})
}

func (h *Handler) ownedMaterial(c *gin.Context) (*Material, bool) {
	mid, ok := paramID(c, "mid")
	if !ok {
		return nil, false
	}
	var m Material
	if err := h.s.db.First(&m, mid).Error; err != nil {
		fail(c, http.StatusNotFound, "Material not found.")
		return nil, false
	}
	course, err := h.s.course(m.CourseID)
	if err != nil || (course.UserId != c.GetInt(auth.CtxUserID) && !isAdmin(c)) {
		fail(c, http.StatusForbidden, "Only the course creator can do this.")
		return nil, false
	}
	return &m, true
}

// PUT /api/learn/materials/:mid {title}
func (h *Handler) RenameMaterial(c *gin.Context) {
	m, ok := h.ownedMaterial(c)
	if !ok {
		return
	}
	var in struct {
		Title string `json:"title"`
	}
	_ = c.ShouldBindJSON(&in)
	title := strings.TrimSpace(in.Title)
	if title == "" {
		fail(c, http.StatusUnprocessableEntity, "A material needs a name.")
		return
	}
	m.Title = clip(title, 255)
	if err := h.s.db.Model(m).Update("title", m.Title).Error; err != nil {
		fail(c, http.StatusInternalServerError, "Could not rename the material.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": m})
}

// PUT /api/learn/courses/:id/materials/order {material_ids: [...]}: every
// material of the course, in the new order.
func (h *Handler) ReorderMaterials(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	var in struct {
		MaterialIDs []int `json:"material_ids"`
	}
	_ = c.ShouldBindJSON(&in)
	var ids []int
	h.s.db.Model(&Material{}).Where("course_id = ?", *course.CourseId).Pluck("id", &ids)
	belongs := map[int]bool{}
	for _, id := range ids {
		belongs[id] = true
	}
	if len(in.MaterialIDs) != len(ids) {
		fail(c, http.StatusUnprocessableEntity, "Send every material id of the course, in the new order.")
		return
	}
	err := h.s.db.Transaction(func(tx *gorm.DB) error {
		for i, id := range in.MaterialIDs {
			if !belongs[id] {
				return errors.New("Unknown material id.")
			}
			delete(belongs, id)
			if err := tx.Model(&Material{}).Where("id = ?", id).Update("position", i+1).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		fail(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	mats := []Material{}
	h.s.db.Where("course_id = ?", *course.CourseId).Order(materialOrder).Find(&mats)
	c.JSON(http.StatusOK, gin.H{"data": mats})
}

// DELETE /api/learn/materials/:mid
func (h *Handler) DeleteMaterial(c *gin.Context) {
	m, ok := h.ownedMaterial(c)
	if !ok {
		return
	}
	if err := h.s.DeleteMaterial(c.Request.Context(), m); err != nil {
		fail(c, http.StatusInternalServerError, "Could not delete the material.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Material deleted."})
}

// GET /api/learn/materials/:mid/pages: the slides of a PowerPoint material,
// one page each (F3.1). Creator only for now; learners get pages through
// their units (F3.7).
func (h *Handler) SlidePages(c *gin.Context) {
	m, ok := h.ownedMaterial(c)
	if !ok {
		return
	}
	if m.Kind != KindSlides {
		fail(c, http.StatusUnprocessableEntity, "Only PowerPoint materials have slide pages.")
		return
	}
	pages, err := h.s.SlidePages(c.Request.Context(), m)
	if err != nil {
		slog.Warn("slide pages", "material", m.ID, "error", err)
		fail(c, http.StatusUnprocessableEntity, "Could not read the slides of this file.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"material_id": m.ID, "pages": pages}})
}

// POST /api/learn/materials/:mid/retry
func (h *Handler) RetryMaterial(c *gin.Context) {
	m, ok := h.ownedMaterial(c)
	if !ok {
		return
	}
	if (m.Status != StatusFailed && m.Status != StatusPaused && !partlyRead(m)) || m.Kind == KindLink || (m.Kind == KindVideo && h.s.videoAI == nil) {
		fail(c, http.StatusUnprocessableEntity, "Only a failed or partly read file, or a failed video, can be retried.")
		return
	}
	h.s.Retry(m)
	c.JSON(http.StatusAccepted, gin.H{"message": "Reading the material again."})
}

// ── organise and units ───────────────────────────────────────────────────

// POST /api/learn/courses/:id/organise
func (h *Handler) Organise(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	if err := h.s.StartOrganise(course); err != nil {
		fail(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message": "Organising the course. This usually takes under a minute.", "organise_status": StatusProcessing})
}

type unitView struct {
	Unit
	Locked       bool          `json:"locked"`
	Completed    bool          `json:"completed"`
	Materials    []Material    `json:"materials,omitempty"`
	Concepts     []conceptView `json:"concepts,omitempty"`
	ConceptNames []string      `json:"concept_names,omitempty"` // learner outline
	Test         *testSummary  `json:"test,omitempty"`
}

// GET /api/learn/courses/:id: the creator's builder view, or the learner's view.
func (h *Handler) GetCourse(c *gin.Context) {
	id, ok := paramID(c, "id")
	if !ok {
		return
	}
	course, err := h.s.course(id)
	if err != nil {
		fail(c, http.StatusNotFound, "Course not found.")
		return
	}
	me := c.GetInt(auth.CtxUserID)
	units := h.s.units(id)
	ids := make([]int, len(units))
	for i, u := range units {
		ids[i] = u.ID
	}

	if course.UserId == me || isAdmin(c) {
		mats := []Material{}
		h.s.db.Where("course_id = ?", id).Order(materialOrder).Find(&mats)
		byUnit := h.s.unitMaterials(ids)
		concepts := h.s.unitConcepts(ids)
		tests := h.s.testSummaries(ids, true, me)
		views := make([]unitView, len(units))
		for i, u := range units {
			views[i] = unitView{Unit: u, Materials: byUnit[u.ID], Concepts: concepts[u.ID], Test: tests[u.ID]}
		}
		var learners int64
		h.s.db.Model(&coursemodels.Enrollment{}).Where("course_id = ?", id).Count(&learners)
		c.JSON(http.StatusOK, gin.H{"data": gin.H{
			"role": "creator", "course": course, "materials": mats, "units": views,
			"learners": learners, "limits": h.s.limits, "ai_allowance": h.s.UserAllowance(course.UserId),
			"progress": h.s.courseProgress(course),
		}})
		return
	}

	if course.CourseStatus != "published" || (course.CourseVisibility != "public" && !h.s.isEnrolled(me, id)) {
		fail(c, http.StatusNotFound, "Course not found.")
		return
	}
	enrolled := h.s.isEnrolled(me, id)
	done := h.s.completedUnits(me, id)
	open := unlocked(units, done)
	concepts := h.s.unitConcepts(ids)
	tests := h.s.testSummaries(ids, false, me)
	views := make([]unitView, len(units))
	for i, u := range units {
		u.Body = "" // body only through GET /units/:uid
		names := []string{}
		for _, cv := range concepts[u.ID] {
			names = append(names, cv.Title)
		}
		views[i] = unitView{Unit: u, Locked: !enrolled || !open[u.ID], Completed: done[u.ID], ConceptNames: names, Test: tests[u.ID]}
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"role": "learner", "course": course, "units": views, "enrolled": enrolled,
		"completed_units": len(done), "total_units": len(units), "more_coming": h.s.moreComing(id),
	}})
}

// POST /api/learn/courses/:id/units {title, body}
func (h *Handler) AddUnit(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	var in struct{ Title, Body string }
	_ = c.ShouldBindJSON(&in)
	if strings.TrimSpace(in.Title) == "" {
		fail(c, http.StatusUnprocessableEntity, "A unit needs a title.")
		return
	}
	var maxPos int
	h.s.db.Model(&Unit{}).Where("course_id = ?", *course.CourseId).Select("COALESCE(MAX(position), 0)").Scan(&maxPos)
	u := Unit{CourseID: *course.CourseId, Position: maxPos + 1, Title: clip(strings.TrimSpace(in.Title), 255), Body: in.Body, Source: "manual"}
	if err := h.s.db.Create(&u).Error; err != nil {
		fail(c, http.StatusInternalServerError, "Could not add the unit.")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": u})
}

// PUT /api/learn/units/:uid {title?, body?, material_ids?}
func (h *Handler) UpdateUnit(c *gin.Context) {
	u, course, ok := h.ownedUnit(c)
	if !ok {
		return
	}
	var in struct {
		Title       *string `json:"title"`
		Body        *string `json:"body"`
		MaterialIDs *[]int  `json:"material_ids"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "The request body must be valid JSON.")
		return
	}
	err := h.s.db.Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{}
		if in.Title != nil {
			t := strings.TrimSpace(*in.Title)
			if t == "" {
				return errors.New("A unit needs a title.")
			}
			updates["title"] = clip(t, 255)
		}
		if in.Body != nil {
			updates["body"] = *in.Body
		}
		if len(updates) > 0 {
			if err := tx.Model(u).Updates(updates).Error; err != nil {
				return err
			}
		}
		if in.MaterialIDs != nil {
			// Only this course's materials, each once, in the given order
			var valid []int
			tx.Model(&Material{}).Where("course_id = ? AND id IN ?", *course.CourseId, append(*in.MaterialIDs, 0)).Pluck("id", &valid)
			ok := map[int]bool{}
			for _, v := range valid {
				ok[v] = true
			}
			if err := tx.Where("unit_id = ?", u.ID).Delete(&UnitMaterial{}).Error; err != nil {
				return err
			}
			pos := 0
			for _, mid := range *in.MaterialIDs {
				if !ok[mid] {
					continue
				}
				delete(ok, mid)
				pos++
				if err := tx.Create(&UnitMaterial{UnitID: u.ID, MaterialID: mid, Position: pos}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		fail(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	h.s.db.First(u, u.ID)
	c.JSON(http.StatusOK, gin.H{"data": unitView{Unit: *u, Materials: h.s.unitMaterials([]int{u.ID})[u.ID], Concepts: h.s.unitConcepts([]int{u.ID})[u.ID]}})
}

// DELETE /api/learn/units/:uid
func (h *Handler) DeleteUnit(c *gin.Context) {
	u, course, ok := h.ownedUnit(c)
	if !ok {
		return
	}
	err := h.s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("unit_id = ?", u.ID).Delete(&UnitMaterial{}).Error; err != nil {
			return err
		}
		if err := tx.Where("unit_id = ?", u.ID).Delete(&UnitProgress{}).Error; err != nil {
			return err
		}
		if err := deleteTests(tx, []int{u.ID}); err != nil {
			return err
		}
		if err := tx.Where("unit_id = ?", u.ID).Delete(&Concept{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(u).Error; err != nil {
			return err
		}
		return renumber(tx, *course.CourseId)
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "Could not delete the unit.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Unit deleted."})
}

// PUT /api/learn/courses/:id/units/order {unit_ids: [...]}
func (h *Handler) ReorderUnits(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	var in struct {
		UnitIDs []int `json:"unit_ids"`
	}
	_ = c.ShouldBindJSON(&in)
	units := h.s.units(*course.CourseId)
	if len(in.UnitIDs) != len(units) {
		fail(c, http.StatusUnprocessableEntity, "Send every unit id of the course, in the new order.")
		return
	}
	belongs := map[int]bool{}
	for _, u := range units {
		belongs[u.ID] = true
	}
	err := h.s.db.Transaction(func(tx *gorm.DB) error {
		for i, id := range in.UnitIDs {
			if !belongs[id] {
				return errors.New("Unknown unit id.")
			}
			delete(belongs, id)
			if err := tx.Model(&Unit{}).Where("id = ?", id).Update("position", i+1).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		fail(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": h.s.units(*course.CourseId)})
}

func renumber(tx *gorm.DB, courseID int) error {
	var us []Unit
	tx.Where("course_id = ?", courseID).Order("position, id").Find(&us)
	for i, u := range us {
		if u.Position != i+1 {
			if err := tx.Model(&Unit{}).Where("id = ?", u.ID).Update("position", i+1).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// ── publish ──────────────────────────────────────────────────────────────

// POST /api/learn/courses/:id/publish {visibility: public|private, confirm_rights: bool}
func (h *Handler) Publish(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	var in struct {
		Visibility    string `json:"visibility"`
		ConfirmRights bool   `json:"confirm_rights"`
	}
	_ = c.ShouldBindJSON(&in)
	if in.Visibility != "public" && in.Visibility != "private" {
		fail(c, http.StatusUnprocessableEntity, "visibility must be public or private.")
		return
	}
	// A course may be published while the AI is still writing its units
	// (learners see them as they appear), but not with nothing in it
	if len(h.s.units(*course.CourseId)) == 0 && h.s.materialCount(*course.CourseId) == 0 {
		fail(c, http.StatusUnprocessableEntity, "Add a material or a unit before publishing.")
		return
	}
	if in.Visibility == "public" && !in.ConfirmRights {
		fail(c, http.StatusUnprocessableEntity, "Confirm that you have the right to share these materials before making the course public.")
		return
	}
	now := time.Now()
	updates := map[string]any{"course_status": "published", "course_visibility": in.Visibility, "published_at": now}
	if in.ConfirmRights {
		updates["rights_confirmed_at"] = now
	}
	h.s.db.Model(&coursemodels.Course{}).Where("course_id = ?", *course.CourseId).Updates(updates)
	course, _ = h.s.course(*course.CourseId)
	c.JSON(http.StatusOK, gin.H{"data": course, "message": "Course published."})
}

// POST /api/learn/courses/:id/unpublish
func (h *Handler) Unpublish(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	h.s.db.Model(&coursemodels.Course{}).Where("course_id = ?", *course.CourseId).Update("course_status", "draft")
	course, _ = h.s.course(*course.CourseId)
	c.JSON(http.StatusOK, gin.H{"data": course, "message": "Course moved back to draft."})
}

// ── learners ─────────────────────────────────────────────────────────────

// GET /api/learn/browse?q=: published public courses
func (h *Handler) Browse(c *gin.Context) {
	type row struct {
		coursemodels.Course
		Units    int    `json:"units"`
		Learners int    `json:"learners"`
		Cover    string `json:"cover,omitempty"`
	}
	var courses []coursemodels.Course
	q := h.s.db.Preload("Category").Where("course_status = ? AND course_visibility = ?", "published", "public")
	if s := strings.TrimSpace(c.Query("q")); s != "" {
		like := "%" + strings.ToLower(s) + "%"
		q = q.Where("LOWER(course_title) LIKE ? OR LOWER(course_description) LIKE ?", like, like)
	}
	q.Order("published_at DESC").Limit(100).Find(&courses)
	ids := make([]int, 0, len(courses))
	for _, course := range courses {
		ids = append(ids, *course.CourseId)
	}
	covers := h.s.covers(ids)
	out := make([]row, 0, len(courses))
	for _, course := range courses {
		var units, learners int64
		h.s.db.Model(&Unit{}).Where("course_id = ?", *course.CourseId).Count(&units)
		h.s.db.Model(&coursemodels.Enrollment{}).Where("course_id = ?", *course.CourseId).Count(&learners)
		out = append(out, row{Course: course, Units: int(units), Learners: int(learners), Cover: covers[*course.CourseId]})
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

// POST /api/learn/courses/:id/join
func (h *Handler) Join(c *gin.Context) {
	id, ok := paramID(c, "id")
	if !ok {
		return
	}
	course, err := h.s.course(id)
	if err != nil || course.CourseStatus != "published" || course.CourseVisibility != "public" {
		fail(c, http.StatusNotFound, "Course not found.")
		return
	}
	me := c.GetInt(auth.CtxUserID)
	if course.UserId == me {
		fail(c, http.StatusUnprocessableEntity, "You created this course.")
		return
	}
	e := coursemodels.Enrollment{CourseID: id, UserID: me}
	h.s.db.Where("course_id = ? AND user_id = ?", id, me).FirstOrCreate(&e)
	c.JSON(http.StatusOK, gin.H{"data": e, "message": "You joined the course."})
}

// GET /api/learn/units/:uid: the unit text and materials, if open for the user.
func (h *Handler) GetUnit(c *gin.Context) {
	uid, ok := paramID(c, "uid")
	if !ok {
		return
	}
	var u Unit
	if err := h.s.db.First(&u, uid).Error; err != nil {
		fail(c, http.StatusNotFound, "Unit not found.")
		return
	}
	course, err := h.s.course(u.CourseID)
	if err != nil {
		fail(c, http.StatusNotFound, "Unit not found.")
		return
	}
	me := c.GetInt(auth.CtxUserID)
	view := unitView{Unit: u, Materials: h.s.unitMaterials([]int{u.ID})[u.ID], Concepts: h.s.unitConcepts([]int{u.ID})[u.ID]}
	creator := course.UserId == me || isAdmin(c)
	view.Test = h.s.testSummaries([]int{u.ID}, creator, me)[u.ID]
	if creator {
		c.JSON(http.StatusOK, gin.H{"data": view})
		return
	}
	if course.CourseStatus != "published" || !h.s.isEnrolled(me, u.CourseID) {
		fail(c, http.StatusNotFound, "Unit not found.")
		return
	}
	done := h.s.completedUnits(me, u.CourseID)
	if !unlocked(h.s.units(u.CourseID), done)[u.ID] {
		fail(c, http.StatusForbidden, "Finish the previous unit first.")
		return
	}
	view.Completed = done[u.ID]
	c.JSON(http.StatusOK, gin.H{"data": view})
}

// POST /api/learn/units/:uid/complete
func (h *Handler) CompleteUnit(c *gin.Context) {
	uid, ok := paramID(c, "uid")
	if !ok {
		return
	}
	var u Unit
	if err := h.s.db.First(&u, uid).Error; err != nil {
		fail(c, http.StatusNotFound, "Unit not found.")
		return
	}
	course, err := h.s.course(u.CourseID)
	me := c.GetInt(auth.CtxUserID)
	if err != nil || course.CourseStatus != "published" || !h.s.isEnrolled(me, u.CourseID) {
		fail(c, http.StatusNotFound, "Unit not found.")
		return
	}
	if err := h.s.Complete(me, &u); err != nil {
		fail(c, http.StatusForbidden, err.Error())
		return
	}
	done := h.s.completedUnits(me, u.CourseID)
	total := len(h.s.units(u.CourseID))
	c.JSON(http.StatusOK, gin.H{"message": "Unit finished.", "completed_units": len(done), "total_units": total,
		"course_completed": total > 0 && len(done) >= total})
}

// GET /api/learn/me: courses I created and courses I joined, with progress.
func (h *Handler) Me(c *gin.Context) {
	me := c.GetInt(auth.CtxUserID)
	created := []coursemodels.Course{}
	h.s.db.Preload("Category").Where("user_id = ?", me).Order("course_id DESC").Find(&created)

	var enrollments []coursemodels.Enrollment
	h.s.db.Preload("Course.Category").Where("user_id = ?", me).Order("enrolled_at DESC").Find(&enrollments)
	type joinedRow struct {
		Course         coursemodels.Course `json:"course"`
		CompletedUnits int                 `json:"completed_units"`
		TotalUnits     int                 `json:"total_units"`
	}
	joined := make([]joinedRow, 0, len(enrollments))
	for _, e := range enrollments {
		// own courses are listed under "created", not as joined
		if e.Course.CourseStatus != "published" || e.Course.UserId == me {
			continue
		}
		joined = append(joined, joinedRow{Course: e.Course,
			CompletedUnits: len(h.s.completedUnits(me, e.CourseID)), TotalUnits: len(h.s.units(e.CourseID))})
	}
	progress := map[int]*BuildProgress{}
	for i := range created {
		if created[i].CourseId != nil {
			progress[*created[i].CourseId] = h.s.courseProgress(&created[i])
		}
	}
	ids := []int{}
	for _, cr := range created {
		if cr.CourseId != nil {
			ids = append(ids, *cr.CourseId)
		}
	}
	for _, j := range joined {
		ids = append(ids, *j.Course.CourseId)
	}
	canCreate, reason := h.s.CanCreateCourse(me, isAdmin(c))
	c.JSON(http.StatusOK, gin.H{"data": gin.H{
		"created": created, "joined": joined, "limits": h.s.limits,
		"can_create_course": canCreate, "create_limit_reason": reason, "ai_allowance": h.s.UserAllowance(me),
		"progress": progress, "covers": h.s.covers(ids),
	}})
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func mb(b int64) string { return strconv.FormatInt(b>>20, 10) + " MB" }
