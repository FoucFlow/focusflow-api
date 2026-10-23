package services

import (
	"errors"
	"focusflow-api/internal/material/model"
	"strings"
	"testing"
)

// ---------- Fakes ----------

type fakeTakeawayRepo struct {
	stored map[string]model.Takeaway
}

func newFakeTakeawayRepo() *fakeTakeawayRepo {
	return &fakeTakeawayRepo{stored: map[string]model.Takeaway{}}
}

func key(u, v int) string {
	return string(rune(u)) + "-" + string(rune(v))
}

func (f *fakeTakeawayRepo) Upsert(t *model.Takeaway) (model.Takeaway, error) {
	t.ID = len(f.stored) + 1
	f.stored[key(t.UserID, t.VideoID)] = *t
	return *t, nil
}

func (f *fakeTakeawayRepo) GetByUserAndVideo(userID, videoID int) (model.Takeaway, error) {
	t, ok := f.stored[key(userID, videoID)]
	if !ok {
		return model.Takeaway{}, errors.New("not found")
	}
	return t, nil
}

func (f *fakeTakeawayRepo) GetByUserAndCourse(userID int, courseID string) ([]model.Takeaway, error) {
	var out []model.Takeaway
	for _, t := range f.stored {
		if t.UserID == userID {
			out = append(out, t)
		}
	}
	return out, nil
}

type fakeMaterialRepo struct {
	mats map[int]model.CourseMaterial
}

func newFakeMaterialRepo() *fakeMaterialRepo {
	return &fakeMaterialRepo{mats: map[int]model.CourseMaterial{}}
}

func (f *fakeMaterialRepo) CreateCourseMaterial(cm *model.CourseMaterial) (model.CourseMaterial, error) {
	f.mats[cm.VideoID] = *cm
	return *cm, nil
}
func (f *fakeMaterialRepo) GetCourseMaterialByID(id int) (model.CourseMaterial, error) {
	cm, ok := f.mats[id]
	if !ok {
		return model.CourseMaterial{}, errors.New("not found")
	}
	return cm, nil
}
func (f *fakeMaterialRepo) GetCourseMaterialsByCourseID(courseID string) ([]model.CourseMaterial, error) {
	var out []model.CourseMaterial
	for _, m := range f.mats {
		if m.CourseID == courseID {
			out = append(out, m)
		}
	}
	return out, nil
}
func (f *fakeMaterialRepo) UpdateCourseMaterial(cm *model.CourseMaterial) (model.CourseMaterial, error) {
	f.mats[cm.VideoID] = *cm
	return *cm, nil
}
func (f *fakeMaterialRepo) DeleteCourseMaterial(_ int) error { return nil }

// ---------- Tests ----------

func TestSubmitTakeaway_RejectsShortBody(t *testing.T) {
	tRepo := newFakeTakeawayRepo()
	mRepo := newFakeMaterialRepo()
	mRepo.mats[1] = model.CourseMaterial{VideoID: 1, CourseID: "c1", Title: "L"}
	svc := NewTakeawayService(tRepo, mRepo)

	_, _, err := svc.SubmitTakeaway(42, 1, "too short")
	if err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("expected min-length error, got %v", err)
	}
}

func TestSubmitTakeaway_HappyPathMarksLessonComplete(t *testing.T) {
	tRepo := newFakeTakeawayRepo()
	mRepo := newFakeMaterialRepo()
	mRepo.mats[1] = model.CourseMaterial{VideoID: 1, CourseID: "c1", Title: "L"}
	svc := NewTakeawayService(tRepo, mRepo)

	body := strings.Repeat("a", TakeawayMinChars)
	cm, ta, err := svc.SubmitTakeaway(42, 1, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cm.Completed {
		t.Fatalf("expected lesson to be marked complete")
	}
	if cm.CompletedAt == nil {
		t.Fatalf("expected CompletedAt to be set")
	}
	if ta.Body != body {
		t.Fatalf("body mismatch")
	}
}

func TestSubmitTakeaway_ResubmitUpdatesRatherThanDuplicates(t *testing.T) {
	tRepo := newFakeTakeawayRepo()
	mRepo := newFakeMaterialRepo()
	mRepo.mats[1] = model.CourseMaterial{VideoID: 1, CourseID: "c1", Title: "L"}
	svc := NewTakeawayService(tRepo, mRepo)

	first := strings.Repeat("a", TakeawayMinChars)
	if _, _, err := svc.SubmitTakeaway(42, 1, first); err != nil {
		t.Fatal(err)
	}
	second := strings.Repeat("b", TakeawayMinChars+5)
	if _, _, err := svc.SubmitTakeaway(42, 1, second); err != nil {
		t.Fatal(err)
	}
	if len(tRepo.stored) != 1 {
		t.Fatalf("expected 1 stored takeaway, got %d", len(tRepo.stored))
	}
}

func TestSubmitTakeaway_LessonNotFound(t *testing.T) {
	svc := NewTakeawayService(newFakeTakeawayRepo(), newFakeMaterialRepo())
	body := strings.Repeat("a", TakeawayMinChars)
	_, _, err := svc.SubmitTakeaway(42, 99, body)
	if err == nil || !strings.Contains(err.Error(), "lesson not found") {
		t.Fatalf("expected lesson not found, got %v", err)
	}
}
