package learn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"time"

	"focusflow-api/internal/platform/storage"

	"gorm.io/gorm"
)

// SlidePage is one slide of a .pptx material as learners read it (F3.1).
type SlidePage struct {
	ID         int          `json:"id" gorm:"primaryKey"`
	MaterialID int          `json:"material_id" gorm:"not null;uniqueIndex:idx_slide_page"`
	Number     int          `json:"number" gorm:"not null;uniqueIndex:idx_slide_page"` // from 1, presentation order
	Title      string       `json:"title" gorm:"type:text"`
	Section    string       `json:"section,omitempty" gorm:"size:255"`
	Blocks     []SlideBlock `json:"blocks" gorm:"type:jsonb;serializer:json"`
	Notes      string       `json:"notes,omitempty" gorm:"type:text"`
	Hidden     bool         `json:"hidden"`
	HasDiagram bool         `json:"has_diagram"`
	MediaKeys  []string     `json:"-" gorm:"type:jsonb;serializer:json"` // pictures in the media bucket
	Edited     bool         `json:"edited"`                              // the teacher changed it (F3.6)
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
}

// SlidePages returns a slides material's pages, building them from the stored
// file the first time (materials uploaded before pages existed).
func (s *Service) SlidePages(ctx context.Context, m *Material) ([]SlidePage, error) {
	if m.Kind != KindSlides {
		return nil, errors.New("only PowerPoint materials have slide pages")
	}
	pages := []SlidePage{}
	if err := s.db.Where("material_id = ?", m.ID).Order("number").Find(&pages).Error; err != nil {
		return nil, err
	}
	if len(pages) > 0 {
		return pages, nil
	}
	data, err := storage.Get(ctx, m.StorageKey, s.limits.MaxFileBytes)
	if err != nil {
		return nil, errors.New("could not read the stored file")
	}
	return s.buildSlidePages(ctx, m, data)
}

// buildSlidePages parses the .pptx, stores its pictures and replaces the
// material's pages. No AI is involved.
func (s *Service) buildSlidePages(ctx context.Context, m *Material, data []byte) ([]SlidePage, error) {
	deck, err := ParseSlides(data)
	if err != nil {
		return nil, err
	}
	urls := map[string]string{} // media path in the .pptx → public URL
	keyOf := map[string]string{}
	for media, img := range deck.Media {
		key := fmt.Sprintf("courses/%d/materials/%d/slides/%s", m.CourseID, m.ID, path.Base(media))
		url, err := storage.UploadKey(ctx, key, img, imageMime(media))
		if err != nil {
			slog.Warn("slide picture not stored", "material", m.ID, "media", media, "error", err)
			continue
		}
		urls[media], keyOf[media] = url, key
	}
	pages := make([]SlidePage, 0, len(deck.Slides))
	for _, sl := range deck.Slides {
		p := SlidePage{MaterialID: m.ID, Number: sl.Number, Title: sl.Title, Section: sl.Section,
			Notes: sl.Notes, Hidden: sl.Hidden, HasDiagram: sl.HasDiagram, Blocks: []SlideBlock{}, MediaKeys: []string{}}
		for _, b := range sl.Blocks {
			if b.Type == "image" {
				if urls[b.Media] == "" {
					// Picture could not be stored: point to the original slide.
					if !p.HasDiagram {
						p.HasDiagram = true
						p.Blocks = append(p.Blocks, SlideBlock{Type: "diagram"})
					}
					continue
				}
				b.Src = urls[b.Media]
				p.MediaKeys = append(p.MediaKeys, keyOf[b.Media])
			}
			p.Blocks = append(p.Blocks, b)
		}
		pages = append(pages, p)
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("material_id = ?", m.ID).Delete(&SlidePage{}).Error; err != nil {
			return err
		}
		if len(pages) == 0 {
			return nil
		}
		return tx.Create(&pages).Error
	})
	if err != nil {
		return nil, err
	}
	return pages, nil
}

// deleteSlidePages removes a material's pages inside tx and returns the
// picture keys to delete from storage once tx commits.
func deleteSlidePages(tx *gorm.DB, materialID int) ([]string, error) {
	var pages []SlidePage
	if err := tx.Select("media_keys").Where("material_id = ?", materialID).Find(&pages).Error; err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var keys []string
	for _, p := range pages {
		for _, k := range p.MediaKeys {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	return keys, tx.Where("material_id = ?", materialID).Delete(&SlidePage{}).Error
}
