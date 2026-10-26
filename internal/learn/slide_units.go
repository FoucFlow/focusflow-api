package learn

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"focusflow-api/internal/auth"
	coursemodels "focusflow-api/internal/course/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Build modes (F1.10). In a slides course the teacher's slides are the
// course: nothing is read or organised by the AI on its own, and units are
// ranges of slides the teacher cuts (F3.3).
const (
	BuildAI     = "ai"
	BuildSlides = "slides"
)

const maxSlideUnits = 200

func slidesMode(c *coursemodels.Course) bool { return c != nil && c.BuildMode == BuildSlides }

// courseInSlidesMode looks the course up; used by background jobs.
func (s *Service) courseInSlidesMode(courseID int) bool {
	c, err := s.course(courseID)
	return err == nil && slidesMode(c)
}

// markReadyWithoutAI: in a slides course a material is usable as soon as it
// is stored; it gets no digest.
func (s *Service) markReadyWithoutAI(m *Material) {
	m.Status, m.Error = StatusReady, ""
	m.Digest = Digest{Topics: []string{}, KeyPoints: []string{}}
	s.db.Model(m).Select("status", "error", "digest").Updates(m)
}

// SlideRange is the part of a slides material a unit reads.
type SlideRange struct {
	MaterialID int `json:"material_id"`
	FirstPage  int `json:"first_page"` // 0 = from the first slide
	LastPage   int `json:"last_page"`  // 0 = to the last slide
	Count      int `json:"count"`      // slides learners see (hidden ones left out)
}

// unitRanges returns, per unit, the slide ranges it reads.
func (s *Service) unitRanges(unitIDs []int) map[int][]SlideRange {
	out := map[int][]SlideRange{}
	if len(unitIDs) == 0 {
		return out
	}
	var links []UnitMaterial
	s.db.Table("unit_materials").Joins("JOIN course_materials_v2 m ON m.id = unit_materials.material_id").
		Where("unit_materials.unit_id IN ? AND m.kind = ?", unitIDs, KindSlides).
		Order("unit_materials.unit_id, unit_materials.position").Find(&links)
	for _, l := range links {
		q := s.db.Model(&SlidePage{}).Where("material_id = ? AND NOT hidden", l.MaterialID)
		if l.FirstPage > 0 {
			q = q.Where("number >= ?", l.FirstPage)
		}
		if l.LastPage > 0 {
			q = q.Where("number <= ?", l.LastPage)
		}
		var n int64
		q.Count(&n)
		out[l.UnitID] = append(out[l.UnitID], SlideRange{MaterialID: l.MaterialID, FirstPage: l.FirstPage, LastPage: l.LastPage, Count: int(n)})
	}
	return out
}

// PUT /api/learn/courses/:id/build-mode {build_mode: "slides" | "ai"}
// Allowed until the course has units.
func (h *Handler) SetBuildMode(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	var in struct {
		BuildMode string `json:"build_mode"`
	}
	_ = c.ShouldBindJSON(&in)
	if in.BuildMode != BuildAI && in.BuildMode != BuildSlides {
		fail(c, http.StatusUnprocessableEntity, `build_mode must be "slides" or "ai".`)
		return
	}
	if in.BuildMode == course.BuildMode {
		c.JSON(http.StatusOK, gin.H{"data": course})
		return
	}
	if len(h.s.units(*course.CourseId)) > 0 {
		fail(c, http.StatusConflict, "The way the course is built can only change before it has units.")
		return
	}
	if course.OrganiseStatus == StatusProcessing {
		fail(c, http.StatusConflict, "Wait until the AI has finished organising.")
		return
	}
	if err := h.s.db.Model(course).Update("build_mode", in.BuildMode).Error; err != nil {
		fail(c, http.StatusInternalServerError, "Could not save the change.")
		return
	}
	course.BuildMode = in.BuildMode
	if in.BuildMode == BuildAI {
		// Materials added in slides mode were never read: read them now.
		var mats []Material
		h.s.db.Where("course_id = ? AND kind IN ? AND status = ?", *course.CourseId,
			[]string{KindPDF, KindSlides, KindVideo}, StatusReady).Find(&mats)
		for i := range mats {
			if mats[i].Digest.Summary == "" && len(mats[i].Digest.Concepts) == 0 {
				h.s.Retry(&mats[i])
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": course})
}

type slideUnitIn struct {
	ID         int    `json:"id"` // existing unit to keep (its learners' progress stays)
	Title      string `json:"title"`
	MaterialID int    `json:"material_id"`
	FirstPage  int    `json:"first_page"`
	LastPage   int    `json:"last_page"`
}

// PUT /api/learn/courses/:id/slide-units {units: [{id?, title, material_id, first_page, last_page}]}
// Replaces the course's units with ranges of its slides, in this order
// (F3.3). Units left out are deleted.
func (h *Handler) SaveSlideUnits(c *gin.Context) {
	course, ok := h.ownedCourse(c)
	if !ok {
		return
	}
	if !slidesMode(course) {
		fail(c, http.StatusConflict, "This course is built by the AI. Switch it to “use my slides as written” first.")
		return
	}
	var in struct {
		Units []slideUnitIn `json:"units"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "The request body must be valid JSON.")
		return
	}
	if len(in.Units) == 0 || len(in.Units) > maxSlideUnits {
		fail(c, http.StatusUnprocessableEntity, fmt.Sprintf("Send between 1 and %d units.", maxSlideUnits))
		return
	}
	courseID := *course.CourseId

	// Materials and their slide counts
	var mats []Material
	h.s.db.Where("course_id = ? AND kind = ?", courseID, KindSlides).Find(&mats)
	matByID := map[int]*Material{}
	for i := range mats {
		matByID[mats[i].ID] = &mats[i]
	}
	counts := map[int]int{}
	for _, u := range in.Units {
		m := matByID[u.MaterialID]
		if m == nil {
			fail(c, http.StatusUnprocessableEntity, "Each unit must use a PowerPoint file of this course.")
			return
		}
		if _, done := counts[m.ID]; !done {
			pages, err := h.s.SlidePages(c.Request.Context(), m)
			if err != nil {
				fail(c, http.StatusUnprocessableEntity, "Could not read the slides of “"+m.Title+"”.")
				return
			}
			counts[m.ID] = len(pages)
		}
	}

	existing := map[int]bool{}
	for _, u := range h.s.units(courseID) {
		existing[u.ID] = true
	}
	seen := map[int]bool{}
	for i := range in.Units {
		u := &in.Units[i]
		n := counts[u.MaterialID]
		if u.FirstPage < 1 || u.LastPage < u.FirstPage || u.LastPage > n {
			fail(c, http.StatusUnprocessableEntity, fmt.Sprintf("Unit %d: slides must be between 1 and %d, first before last.", i+1, n))
			return
		}
		u.Title = strings.TrimSpace(u.Title)
		if u.Title == "" {
			u.Title = fmt.Sprintf("Slides %d–%d", u.FirstPage, u.LastPage)
		}
		if len([]rune(u.Title)) > 255 {
			fail(c, http.StatusUnprocessableEntity, fmt.Sprintf("Unit %d: the title is too long.", i+1))
			return
		}
		if u.ID != 0 {
			if !existing[u.ID] || seen[u.ID] {
				fail(c, http.StatusUnprocessableEntity, "Unknown or repeated unit id.")
				return
			}
			seen[u.ID] = true
		}
	}

	slideIDs := make([]int, 0, len(mats))
	for _, m := range mats {
		slideIDs = append(slideIDs, m.ID)
	}
	err := h.s.db.Transaction(func(tx *gorm.DB) error {
		var drop []int
		for id := range existing {
			if !seen[id] {
				drop = append(drop, id)
			}
		}
		if err := deleteUnitIDs(tx, drop); err != nil {
			return err
		}
		for i, in := range in.Units {
			u := Unit{ID: in.ID, CourseID: courseID, Position: i + 1, Title: in.Title, Source: "manual"}
			if in.ID == 0 {
				if err := tx.Create(&u).Error; err != nil {
					return err
				}
			} else if err := tx.Model(&Unit{}).Where("id = ?", in.ID).
				Updates(map[string]any{"position": i + 1, "title": in.Title}).Error; err != nil {
				return err
			}
			// Replace the unit's slide range; other materials (videos, links) stay.
			if err := tx.Where("unit_id = ? AND material_id IN ?", u.ID, slideIDs).Delete(&UnitMaterial{}).Error; err != nil {
				return err
			}
			if err := tx.Create(&UnitMaterial{UnitID: u.ID, MaterialID: in.MaterialID, Position: 0,
				FirstPage: in.FirstPage, LastPage: in.LastPage}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		slog.Error("save slide units", "course", courseID, "error", err)
		fail(c, http.StatusInternalServerError, "Could not save the units.")
		return
	}
	units := h.s.units(courseID)
	ids := make([]int, len(units))
	for i, u := range units {
		ids[i] = u.ID
	}
	ranges := h.s.unitRanges(ids)
	views := make([]gin.H, len(units))
	for i, u := range units {
		views[i] = gin.H{"id": u.ID, "position": u.Position, "title": u.Title, "slides": ranges[u.ID]}
	}
	c.JSON(http.StatusOK, gin.H{"data": views})
}

// deleteUnitIDs removes units with everything hanging off them.
func deleteUnitIDs(tx *gorm.DB, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	if err := tx.Where("unit_id IN ?", ids).Delete(&UnitMaterial{}).Error; err != nil {
		return err
	}
	if err := deleteTests(tx, ids); err != nil {
		return err
	}
	if err := tx.Where("unit_id IN ?", ids).Delete(&Concept{}).Error; err != nil {
		return err
	}
	if err := tx.Where("unit_id IN ?", ids).Delete(&UnitProgress{}).Error; err != nil {
		return err
	}
	return tx.Where("id IN ?", ids).Delete(&Unit{}).Error
}

// unitDeck is one slides material of a unit, with the pages the unit reads.
type unitDeck struct {
	MaterialID int         `json:"material_id"`
	Title      string      `json:"title"`
	URL        string      `json:"url"` // the original file (diagram card)
	FirstPage  int         `json:"first_page"`
	LastPage   int         `json:"last_page"`
	Pages      []SlidePage `json:"pages"`
}

// GET /api/learn/units/:uid/pages: the slides a unit reads (F3.7). Same
// access as the unit: its creator, or an enrolled learner once it is open.
// Hidden slides are left out.
func (h *Handler) UnitPages(c *gin.Context) {
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
	if course.UserId != me && !isAdmin(c) {
		if course.CourseStatus != "published" || !h.s.isEnrolled(me, u.CourseID) {
			fail(c, http.StatusNotFound, "Unit not found.")
			return
		}
		if !unlocked(h.s.units(u.CourseID), h.s.completedUnits(me, u.CourseID))[u.ID] {
			fail(c, http.StatusForbidden, "Finish the previous unit first.")
			return
		}
	}
	decks := []unitDeck{}
	for _, r := range h.s.unitRanges([]int{u.ID})[u.ID] {
		var m Material
		if err := h.s.db.First(&m, r.MaterialID).Error; err != nil {
			continue
		}
		pages, err := h.s.SlidePages(c.Request.Context(), &m)
		if err != nil {
			slog.Warn("unit pages", "unit", u.ID, "material", m.ID, "error", err)
			continue
		}
		d := unitDeck{MaterialID: m.ID, Title: m.Title, URL: m.URL, FirstPage: r.FirstPage, LastPage: r.LastPage, Pages: []SlidePage{}}
		for _, p := range pages {
			if p.Hidden || (r.FirstPage > 0 && p.Number < r.FirstPage) || (r.LastPage > 0 && p.Number > r.LastPage) {
				continue
			}
			p.Notes = "" // speaker notes are the teacher's
			d.Pages = append(d.Pages, p)
		}
		decks = append(decks, d)
	}
	c.JSON(http.StatusOK, gin.H{"data": decks})
}

var errSlidesMode = errors.New("This course uses your slides as written: cut your slides into units instead of organising with AI.")
