package learn

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	coursemodels "focusflow-api/internal/course/models"

	"gorm.io/gorm"
)

// The course builds itself in the background: each material's first short
// part is read, the course is organised as soon as nothing is still being
// read, and the rest of long materials is read afterwards (new units are
// added for it), as far as the creator's AI allowance allows.

// readReservePercent of the allowance is kept for organising: later parts of
// long materials are not read automatically below it.
const readReservePercent = 25

// afterRead runs when a material, or a part of it, has been read (or failed).
func (s *Service) afterRead(courseID int) {
	c, err := s.course(courseID)
	if err != nil {
		return
	}
	if len(s.units(courseID)) > 0 {
		s.maybeExtend(courseID) // units for the newly read concepts
		return
	}
	// Never organised: do it now. StartOrganise waits for materials still
	// being read and checks the allowance.
	if c.OrganiseRuns > 0 || c.OrganiseStatus == StatusProcessing || c.CourseStatus == "published" {
		return
	}
	if err := s.StartOrganise(c); err != nil {
		slog.Info("auto organise not started", "course", courseID, "reason", err.Error())
	}
}

// organiseProgress saves how many units are written, for the progress bar.
func (s *Service) organiseProgress(courseID int) func(done, total int) {
	return func(done, total int) {
		s.db.Model(&coursemodels.Course{}).Where("course_id = ?", courseID).
			Updates(map[string]any{"organise_done": done, "organise_total": total})
	}
}

// canReadMore: enough allowance is left to read on in the background.
func (s *Service) canReadMore(userID int) bool {
	a := s.UserAllowance(userID)
	if a.Unlimited {
		return true
	}
	for _, w := range a.Windows {
		if w.UsedPercent >= 100-readReservePercent {
			return false
		}
	}
	return a.CanUseAI
}

const waitMessage = "Waiting for your AI allowance: your course is organised from the materials already read first. This one is read automatically when your allowance refills, or press Read now."

// shouldWait: a material not read yet waits when the allowance is low and
// the course already has something read to organise.
func (s *Service) shouldWait(m *Material) bool {
	if m.ReadPages > 0 || m.ReadSeconds > 0 || s.canReadMore(m.OwnerID) {
		return false
	}
	var ready int64
	s.db.Model(&Material{}).Where("course_id = ? AND id <> ? AND status = ?", m.CourseID, m.ID, StatusReady).Count(&ready)
	return ready > 0
}

// pauseReading keeps what was read; resumeLoop goes on when the allowance refills.
func (s *Service) pauseReading(m *Material) {
	where := ""
	if m.TotalSeconds > 0 {
		where = fmt.Sprintf("%s of %s watched", clock(m.ReadSeconds), clock(m.TotalSeconds))
	} else {
		where = fmt.Sprintf("pages 1–%d of %d read", m.ReadPages, m.TotalPages)
	}
	s.db.Model(m).Updates(map[string]any{"reading": false, "paused": true, "status": StatusReady,
		"error": "Paused (" + where + ") to keep some of your AI allowance for organising. The rest is read automatically when your allowance refills, or press Continue."})
}

const (
	aiBusyMessage  = "The AI has reached its limit for now. This is read automatically as soon as it is available again (at the latest after 07:00 UTC)."
	aiBusyOrganise = "The AI has reached its limit for now. Your course is organised automatically as soon as it is available again (at the latest after 07:00 UTC)."
)

// waitForAI: every AI key is over its quota. The material waits (nothing is
// lost) and resumePaused continues it; false for any other error.
func (s *Service) waitForAI(m *Material, err error) bool {
	if !errors.Is(err, ErrAllKeysResting) {
		return false
	}
	if m.ReadPages == 0 && m.ReadSeconds == 0 {
		s.db.Model(m).Updates(map[string]any{"status": StatusPaused, "reading": false, "error": aiBusyMessage})
	} else {
		s.db.Model(m).Updates(map[string]any{"status": StatusReady, "reading": false, "paused": true, "error": aiBusyMessage})
	}
	return true
}

// resumeLoop continues paused materials whose creators have allowance again.
func (s *Service) resumeLoop() {
	for range time.Tick(10 * time.Minute) {
		s.resumePaused()
	}
}

func (s *Service) resumePaused() {
	// Courses whose organising waited for the AI
	var waiting []coursemodels.Course
	s.db.Where("organise_status = ? AND organise_error = ?", StatusFailed, aiBusyOrganise).Limit(20).Find(&waiting)
	for i := range waiting {
		if err := s.StartOrganise(&waiting[i]); err != nil {
			slog.Info("organise still waiting", "course", *waiting[i].CourseId, "reason", err.Error())
		}
	}

	var mats []Material
	s.db.Where("(paused AND status = ?) OR status = ?", StatusReady, StatusPaused).Order("id").Limit(50).Find(&mats)
	ok := map[int]bool{}
	for _, m := range mats {
		can, seen := ok[m.OwnerID]
		if !seen {
			can = s.canReadMore(m.OwnerID)
			ok[m.OwnerID] = can
		}
		if !can {
			continue
		}
		var res *gorm.DB
		if m.Status == StatusPaused {
			res = s.db.Model(&Material{}).Where("id = ? AND status = ?", m.ID, StatusPaused).Updates(map[string]any{"status": StatusProcessing, "error": ""})
		} else {
			res = s.db.Model(&Material{}).Where("id = ? AND paused", m.ID).Updates(map[string]any{"paused": false, "reading": true, "error": ""})
		}
		if res.RowsAffected == 1 {
			id := m.ID
			s.enqueue(func() { s.digest(id, nil) })
		}
	}
}

// BuildProgress is what the "generating your course" bar shows.
type BuildProgress struct {
	Active  bool   `json:"active"`  // still generating
	Percent int    `json:"percent"` // 0–100
	Stage   string `json:"stage"`   // reading | organising | adding | references | ready | waiting | failed
	Label   string `json:"label"`
	More    bool   `json:"more"`  // the rest of long materials is still being read
	Later   bool   `json:"later"` // some material waits for the creator's AI allowance
}

// buildProgress: reading the first parts 0–40%, organising 40–90%
// (planning, then unit by unit), references 90–100%.
func buildProgress(c *coursemodels.Course, mats []Material, units int) *BuildProgress {
	if len(mats) == 0 {
		return nil
	}
	read, more, later := 0, false, false
	for _, m := range mats {
		if m.Status != StatusProcessing {
			read++
		}
		if m.Reading {
			more = true
		}
		if m.Paused || m.Status == StatusPaused {
			later = true
		}
	}
	p := &BuildProgress{Active: true, More: more, Later: later}
	pct := func(from, span, done, total int) int {
		if total <= 0 {
			return from
		}
		return from + span*done/total
	}
	switch {
	case c.OrganiseStatus == StatusProcessing && c.OrganiseMore:
		p.Stage, p.Percent = "adding", pct(10, 85, c.OrganiseDone, c.OrganiseTotal)
		p.Label = "Adding units for the newly read part"
		if c.OrganiseTotal > 0 {
			p.Label += fmt.Sprintf(" (%d of %d written)", c.OrganiseDone, c.OrganiseTotal)
		}
	case c.OrganiseStatus == StatusProcessing:
		p.Stage = "organising"
		if c.OrganiseTotal == 0 {
			p.Percent, p.Label = 45, "Planning your units"
		} else {
			p.Percent = pct(50, 40, c.OrganiseDone, c.OrganiseTotal)
			p.Label = fmt.Sprintf("Writing your units (%d of %d)", c.OrganiseDone, c.OrganiseTotal)
		}
	case units == 0 && read < len(mats):
		p.Stage, p.Percent = "reading", pct(5, 35, read, len(mats))
		p.Label = fmt.Sprintf("Reading your materials (%d of %d ready)", read, len(mats))
	case units == 0 && c.OrganiseStatus == StatusFailed:
		p.Active, p.Stage, p.Percent, p.Label = false, "failed", 40, c.OrganiseError
	case units == 0:
		p.Active, p.Stage, p.Percent, p.Label = false, "waiting", 40, "Your materials are read. Organise them into units."
	case c.ReferencesStatus == StatusProcessing:
		p.Stage, p.Percent = "references", pct(90, 10, c.ReferencesDone, c.ReferencesTotal)
		p.Label = "Finding free resources to expand each concept"
	default:
		p.Active, p.Stage, p.Percent, p.Label = false, "ready", 100, "Your course is ready"
		if later {
			p.Label = "Your course is ready with what the AI could read so far. The rest of your materials is added automatically when your AI allowance refills"
		} else if more {
			p.Label += ". The rest of your materials is still being read; new units are added as it is"
		}
	}
	return p
}

// moreComing: units are still to be added (for learners).
func (s *Service) moreComing(courseID int) bool {
	var n int64
	s.db.Model(&Material{}).Where("course_id = ? AND (reading OR paused OR status IN ?)", courseID,
		[]string{StatusProcessing, StatusPaused}).Count(&n)
	return n > 0
}

// courseProgress loads what buildProgress needs.
func (s *Service) courseProgress(c *coursemodels.Course) *BuildProgress {
	var mats []Material
	s.db.Select("id, status, reading, paused").Where("course_id = ?", *c.CourseId).Find(&mats)
	var units int64
	s.db.Model(&Unit{}).Where("course_id = ?", *c.CourseId).Count(&units)
	p := buildProgress(c, mats, int(units))
	// Read, and about to be organised by itself (afterRead runs just after
	// the material is saved): keep showing progress
	anyReady := false
	for _, m := range mats {
		anyReady = anyReady || m.Status == StatusReady
	}
	if p != nil && p.Stage == "waiting" && anyReady && c.OrganiseRuns == 0 && c.CourseStatus != "published" && s.UserAllowance(c.UserId).CanUseAI {
		p.Active, p.Stage, p.Label = true, "organising", "Starting to organise your units"
	}
	return p
}
