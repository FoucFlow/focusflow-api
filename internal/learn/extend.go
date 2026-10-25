package learn

import (
	"context"
	"errors"
	"log/slog"
	"time"

	coursemodels "focusflow-api/internal/course/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Long materials are read in parts. Once a course is organised, every newly
// read part gets units of its own, added after the existing ones: the
// creator's units, edits and learners' progress stay as they are.

// hasUnorganised: some ready material has concepts not yet in units.
func (s *Service) hasUnorganised(courseID int) bool {
	var mats []Material
	s.db.Where("course_id = ? AND status = ?", courseID, StatusReady).Find(&mats)
	for _, m := range mats {
		for _, c := range m.Digest.Concepts {
			if !c.Organised {
				return true
			}
		}
	}
	return false
}

// maybeExtend queues extend when the course was organised and new concepts
// were read since. A full organise in progress picks them up when it ends.
func (s *Service) maybeExtend(courseID int) {
	c, err := s.course(courseID)
	if err != nil || c.OrganiseStatus != StatusReady || len(s.units(courseID)) == 0 || !s.hasUnorganised(courseID) {
		return
	}
	res := s.db.Model(&coursemodels.Course{}).Where("course_id = ? AND organise_status = ?", courseID, StatusReady).
		Updates(map[string]any{"organise_status": StatusProcessing, "organise_more": true, "organise_error": ""})
	if res.RowsAffected == 0 {
		return
	}
	owner := c.UserId
	s.enqueue(func() { s.extend(courseID, owner) })
}

// extend plans and writes new units for the concepts not organised yet.
func (s *Service) extend(courseID, ownerID int) {
	done := func(msg string) {
		s.db.Model(&coursemodels.Course{}).Where("course_id = ?", courseID).
			Updates(map[string]any{"organise_status": StatusReady, "organise_more": false, "organise_error": msg})
	}
	c, err := s.course(courseID)
	if err != nil {
		return
	}
	var mats []Material
	s.db.Where("course_id = ? AND status = ?", courseID, StatusReady).Order("id").Find(&mats)
	in, refs, taken := organiseInput(c, mats, true)
	if len(in.Materials) == 0 {
		done("")
		return
	}
	units := s.units(courseID)
	ids := make([]int, len(units))
	for i, u := range units {
		ids[i] = u.ID
	}
	concepts := s.unitConcepts(ids)
	for _, u := range units {
		eu := ExistingUnit{Title: u.Title, Concepts: []string{}}
		for _, cv := range concepts[u.ID] {
			eu.Concepts = append(eu.Concepts, cv.Title)
		}
		in.ExistingUnits = append(in.ExistingUnits, eu)
	}
	if err := s.checkAI(ownerID); err != nil {
		done(limitMessage(err))
		return
	}

	started := time.Now()
	outline, usage, err := s.ai.Organise(context.Background(), in)
	s.logUsage("organise_more", courseID, ownerID, usage, started, err)
	if err == nil && len(outline.Units) == 0 {
		err = errors.New("the AI proposed no units")
	}
	if err != nil {
		slog.Warn("adding units failed", "course", courseID, "error", err)
		msg := "The AI could not add units for the pages read later. It tries again when more pages are read; or press Organise to redo the whole course."
		if errors.Is(err, ErrAICredit) {
			msg = creditMessage
		}
		done(msg)
		return
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var maxPos int
		tx.Model(&Unit{}).Where("course_id = ?", courseID).Select("COALESCE(MAX(position), 0)").Scan(&maxPos)
		return saveUnits(tx, courseID, maxPos, outline, refs)
	})
	if err != nil {
		slog.Error("save added units", "course", courseID, "error", err)
		done("Could not save the new units. They will be tried again when more pages are read.")
		return
	}
	s.markOrganised(taken)
	done("")
	if s.refs != nil {
		if c, err := s.course(courseID); err == nil {
			_ = s.startReferences(c, true) // only the new concepts
		}
	}
	s.maybeExtend(courseID) // pages read meanwhile
}

// markOrganised flags the first n concepts of each material as organised.
func (s *Service) markOrganised(taken map[int]int) {
	for mid, n := range taken {
		_ = s.db.Transaction(func(tx *gorm.DB) error {
			var m Material
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&m, mid).Error; err != nil {
				return err
			}
			for i := 0; i < n && i < len(m.Digest.Concepts); i++ {
				m.Digest.Concepts[i].Organised = true
			}
			return tx.Model(&m).Select("digest").Updates(&m).Error
		})
	}
}
