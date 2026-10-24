package learn

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"focusflow-api/internal/auth"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// sourceView is a concept source ready for the page: what it is and a link
// that opens at the right place (PDF page, video start time).
type sourceView struct {
	ConceptSource
	Title     string `json:"title"`
	Kind      string `json:"kind"`
	URL       string `json:"url"`  // opens the source at the right place
	Hint      string `json:"hint"` // e.g. "pages 4–6", "slides 7, 9"
	YouTubeID string `json:"youtube_id,omitempty"`
}

type conceptView struct {
	Concept
	Sources []sourceView `json:"sources"`
}

// unitConcepts loads each unit's concepts, in order, with their sources
// resolved against the course's current materials (deleted ones drop out).
func (s *Service) unitConcepts(unitIDs []int) map[int][]conceptView {
	out := map[int][]conceptView{}
	if len(unitIDs) == 0 {
		return out
	}
	var concepts []Concept
	s.db.Where("unit_id IN ?", unitIDs).Order("unit_id, position, id").Find(&concepts)
	ids := map[int]bool{}
	for _, c := range concepts {
		for _, src := range c.Sources {
			ids[src.MaterialID] = true
		}
	}
	mats := map[int]Material{}
	if len(ids) > 0 {
		var list []Material
		keys := make([]int, 0, len(ids))
		for id := range ids {
			keys = append(keys, id)
		}
		s.db.Where("id IN ?", keys).Find(&list)
		for _, m := range list {
			mats[m.ID] = m
		}
	}
	for _, c := range concepts {
		v := conceptView{Concept: c, Sources: []sourceView{}}
		for _, src := range c.Sources {
			m, ok := mats[src.MaterialID]
			if !ok {
				continue
			}
			v.Sources = append(v.Sources, sourceView{ConceptSource: src, Title: m.Title, Kind: m.Kind,
				URL: sourceURL(m, src), Hint: sourceHint(src), YouTubeID: m.YouTubeID})
		}
		out[c.UnitID] = append(out[c.UnitID], v)
	}
	return out
}

func sourceURL(m Material, src ConceptSource) string {
	switch {
	case m.Kind == KindPDF && len(src.Pages) > 0:
		return fmt.Sprintf("%s#page=%d", m.URL, src.Pages[0]) // browsers' PDF viewers jump to the page
	case m.Kind == KindVideo && m.YouTubeID != "":
		u := "https://www.youtube.com/watch?v=" + m.YouTubeID
		if src.StartSeconds != nil && *src.StartSeconds > 0 {
			u += fmt.Sprintf("&t=%ds", *src.StartSeconds)
		}
		return u
	default:
		return m.URL
	}
}

// sourceHint describes the location, e.g. "pages 4–6, 9" or "slide 3".
func sourceHint(src ConceptSource) string {
	switch {
	case len(src.Pages) > 0:
		return plural("page", src.Pages) + " " + ranges(src.Pages)
	case len(src.Slides) > 0:
		return plural("slide", src.Slides) + " " + ranges(src.Slides)
	case src.StartSeconds != nil:
		return fmt.Sprintf("from %d:%02d", *src.StartSeconds/60, *src.StartSeconds%60)
	}
	return ""
}

func plural(word string, n []int) string {
	if len(n) == 1 {
		return word
	}
	return word + "s"
}

// ranges formats sorted numbers: [1 2 3 5] → "1–3, 5".
func ranges(n []int) string {
	var parts []string
	for i := 0; i < len(n); {
		j := i
		for j+1 < len(n) && n[j+1] == n[j]+1 {
			j++
		}
		if j > i {
			parts = append(parts, fmt.Sprintf("%d–%d", n[i], n[j]))
		} else {
			parts = append(parts, fmt.Sprint(n[i]))
		}
		i = j + 1
	}
	return strings.Join(parts, ", ")
}

// ── creator edits ────────────────────────────────────────────────────────

func (h *Handler) ownedConcept(c *gin.Context) (*Concept, bool) {
	cid, ok := paramID(c, "cid")
	if !ok {
		return nil, false
	}
	var con Concept
	if err := h.s.db.First(&con, cid).Error; err != nil {
		fail(c, http.StatusNotFound, "Concept not found.")
		return nil, false
	}
	var u Unit
	if err := h.s.db.First(&u, con.UnitID).Error; err != nil {
		fail(c, http.StatusNotFound, "Concept not found.")
		return nil, false
	}
	course, err := h.s.course(u.CourseID)
	if err != nil || (course.UserId != c.GetInt(auth.CtxUserID) && !isAdmin(c)) {
		fail(c, http.StatusForbidden, "Only the course creator can do this.")
		return nil, false
	}
	return &con, true
}

// POST /api/learn/units/:uid/concepts {title, explanation}
func (h *Handler) AddConcept(c *gin.Context) {
	u, _, ok := h.ownedUnit(c)
	if !ok {
		return
	}
	var in struct{ Title, Explanation string }
	_ = c.ShouldBindJSON(&in)
	if strings.TrimSpace(in.Title) == "" {
		fail(c, http.StatusUnprocessableEntity, "A concept needs a title.")
		return
	}
	var maxPos int
	h.s.db.Model(&Concept{}).Where("unit_id = ?", u.ID).Select("COALESCE(MAX(position), 0)").Scan(&maxPos)
	con := Concept{UnitID: u.ID, Position: maxPos + 1, Title: clip(strings.TrimSpace(in.Title), 255), Explanation: in.Explanation, Sources: []ConceptSource{}}
	if err := h.s.db.Create(&con).Error; err != nil {
		fail(c, http.StatusInternalServerError, "Could not add the concept.")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": con})
}

// PUT /api/learn/concepts/:cid {title?, explanation?}
func (h *Handler) UpdateConcept(c *gin.Context) {
	con, ok := h.ownedConcept(c)
	if !ok {
		return
	}
	var in struct {
		Title       *string `json:"title"`
		Explanation *string `json:"explanation"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "The request body must be valid JSON.")
		return
	}
	updates := map[string]any{}
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" {
			fail(c, http.StatusUnprocessableEntity, "A concept needs a title.")
			return
		}
		updates["title"] = clip(t, 255)
	}
	if in.Explanation != nil {
		updates["explanation"] = *in.Explanation
	}
	if len(updates) > 0 {
		h.s.db.Model(con).Updates(updates)
	}
	h.s.db.First(con, con.ID)
	c.JSON(http.StatusOK, gin.H{"data": h.s.unitConcepts([]int{con.UnitID})[con.UnitID]})
}

// DELETE /api/learn/concepts/:cid
func (h *Handler) DeleteConcept(c *gin.Context) {
	con, ok := h.ownedConcept(c)
	if !ok {
		return
	}
	h.s.db.Delete(con)
	c.JSON(http.StatusOK, gin.H{"message": "Concept removed."})
}

// PUT /api/learn/units/:uid/concepts/order {concept_ids: [...]}
func (h *Handler) ReorderConcepts(c *gin.Context) {
	u, _, ok := h.ownedUnit(c)
	if !ok {
		return
	}
	var in struct {
		ConceptIDs []int `json:"concept_ids"`
	}
	_ = c.ShouldBindJSON(&in)
	var ids []int
	h.s.db.Model(&Concept{}).Where("unit_id = ?", u.ID).Pluck("id", &ids)
	belongs := map[int]bool{}
	for _, id := range ids {
		belongs[id] = true
	}
	if len(in.ConceptIDs) != len(ids) {
		fail(c, http.StatusUnprocessableEntity, "Send every concept id of the unit, in the new order.")
		return
	}
	err := h.s.db.Transaction(func(tx *gorm.DB) error {
		for i, id := range in.ConceptIDs {
			if !belongs[id] {
				return errors.New("Unknown concept id.")
			}
			delete(belongs, id)
			if err := tx.Model(&Concept{}).Where("id = ?", id).Update("position", i+1).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		fail(c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": h.s.unitConcepts([]int{u.ID})[u.ID]})
}
