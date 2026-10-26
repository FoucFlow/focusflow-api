package learn

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	coursemodels "focusflow-api/internal/course/models"
)

func TestDrawItemsShufflesButKeepsTheRightAnswer(t *testing.T) {
	var pool []Question
	for i := 1; i <= 10; i++ {
		pool = append(pool, Question{ID: i, Kind: QuestionMCQ, Prompt: "q", Options: []string{"right", "a", "b", "c"}, Correct: 0})
	}
	pool = append(pool, Question{ID: 11, Kind: QuestionShort, Prompt: "say", AnswerGuide: "g"})
	moved := false
	for round := 0; round < 20; round++ {
		items := drawItems(pool, 6)
		if len(items) != 6 {
			t.Fatalf("want 6 items, got %d", len(items))
		}
		seen := map[int]bool{}
		for _, it := range items {
			if seen[it.QuestionID] {
				t.Fatalf("question %d drawn twice", it.QuestionID)
			}
			seen[it.QuestionID] = true
			if it.Kind == QuestionMCQ {
				if it.Options[it.Correct] != "right" {
					t.Fatalf("correct index points at %q", it.Options[it.Correct])
				}
				if it.Correct != 0 {
					moved = true
				}
			}
		}
	}
	if !moved {
		t.Fatal("options were never shuffled")
	}
	if n := len(drawItems(pool, 50)); n != len(pool) {
		t.Fatalf("asking for more than the pool gives the whole pool, got %d", n)
	}
}

func TestScoreAndFinalise(t *testing.T) {
	one, half, zero := 1.0, 0.5, 0.0
	a := Attempt{Items: []AttemptItem{{Mark: &one}, {Mark: &half}, {Mark: &zero}}, Settings: TestSettings{PassMark: 50}}
	finalise(&a)
	if a.ScorePercent != 50 || !a.Passed || a.Status != AttemptDone {
		t.Fatalf("1.5 of 3 = 50%% passes a 50%% mark: %+v", a)
	}
	a.Settings.PassMark = 51
	finalise(&a)
	if a.Passed {
		t.Fatal("50% fails a 51% pass mark")
	}
	a.Settings.TeacherReview = true
	a.Settings.PassMark = 10
	finalise(&a)
	if a.Status != AttemptReview || a.Passed {
		t.Fatalf("with teacher review the result waits: %+v", a)
	}
}

func TestCleanMarksRoundsAndRequiresEveryItem(t *testing.T) {
	items := []MarkItem{{Index: 1}, {Index: 2}, {Index: 3}}
	raw := rawMarks{}
	raw.Marks = append(raw.Marks,
		struct {
			Index    int     `json:"index"`
			Mark     float64 `json:"mark"`
			Feedback string  `json:"feedback"`
		}{1, 0.9, "good"}, struct {
			Index    int     `json:"index"`
			Mark     float64 `json:"mark"`
			Feedback string  `json:"feedback"`
		}{2, 0.4, "part"}, struct {
			Index    int     `json:"index"`
			Mark     float64 `json:"mark"`
			Feedback string  `json:"feedback"`
		}{9, 1, "unknown item"})
	if _, err := cleanMarks(raw, items); err == nil {
		t.Fatal("a missing mark must fail")
	}
	raw.Marks[2].Index = 3
	raw.Marks[2].Mark = 7 // out of range
	m, err := cleanMarks(raw, items)
	if err != nil {
		t.Fatal(err)
	}
	if m[1].Mark != 1 || m[2].Mark != 0.5 || m[3].Mark != 1 {
		t.Fatalf("marks round to 0/0.5/1: %+v", m)
	}
}

func TestCleanQuestionsDropsBrokenOnes(t *testing.T) {
	type q = struct {
		Concept     int      `json:"concept"`
		Kind        string   `json:"kind"`
		Prompt      string   `json:"prompt"`
		Options     []string `json:"options"`
		Correct     int      `json:"correct"`
		Corrects    []int    `json:"corrects"`
		AnswerGuide string   `json:"answer_guide"`
	}
	raw := rawQuestions{Questions: []q{
		{1, "mcq", "ok?", []string{"a", "b", "c"}, 1, nil, "b"},
		{1, "mcq", "bad index", []string{"a", "b"}, 5, nil, ""},
		{1, "mcq", "duplicate options", []string{"a", "A"}, 0, nil, ""},
		{2, "short", "no guide", nil, 0, nil, ""},
		{2, "explain", "why?", nil, 0, nil, "- because"},
		{1, "essay", "unknown kind", nil, 0, nil, "x"},
	}}
	qs, err := cleanQuestions(raw, []ExamConcept{{Title: "First"}, {Title: "Second"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 2 || qs[0].Concept != "First" || qs[1].Concept != "Second" || qs[1].Options == nil {
		t.Fatalf("keeps only the two valid questions: %+v", qs)
	}
}

// creditAI fails every call as if its credit were used up.
type creditAI struct{ FakeAI }

func (creditAI) Digest(context.Context, DigestInput) (Digest, Usage, error) {
	return Digest{}, Usage{}, fmt.Errorf("%w (claude 400): low", ErrAICredit)
}

func (creditAI) MarkAnswers(context.Context, MarkInput) (map[int]AIMark, Usage, error) {
	return nil, Usage{}, errors.New("claude 500: overloaded")
}

func TestFallbackOnlyOnCreditErrors(t *testing.T) {
	f := FallbackAI{Primary: creditAI{}, Backup: FakeAI{}}
	d, _, err := f.Digest(context.Background(), DigestInput{Title: "Notes"})
	if err != nil || d.Summary != "Digest of Notes" {
		t.Fatalf("out of credit → backup reads it: %v %+v", err, d)
	}
	if _, _, err := f.MarkAnswers(context.Background(), MarkInput{}); err == nil || errors.Is(err, ErrAICredit) {
		t.Fatalf("other errors are not handed to the backup: %v", err)
	}
}

func TestAllowanceHelpers(t *testing.T) {
	if p := usedPercent(1, 0.5); p != 1 {
		t.Fatalf("any use shows at least 1%%, got %d", p)
	}
	if p := usedPercent(250000, 0.5); p != 50 {
		t.Fatalf("$0.25 of $0.50 is 50%%, got %d", p)
	}
	if p := usedPercent(900000, 0.5); p != 100 {
		t.Fatalf("going over shows 100%%, got %d", p)
	}
	if s := untilText(3*time.Hour + 19*time.Minute + 10*time.Second); s != "3 h 20 min" {
		t.Fatalf("got %q", s)
	}
	now := time.Date(2026, 10, 31, 23, 30, 0, 0, time.UTC)
	day, nextDay, month, nextMonth := allowanceWindows(now)
	if day.Day() != 31 || nextDay.Month() != 11 || month.Day() != 1 || nextMonth.Month() != 11 || nextMonth.Day() != 1 {
		t.Fatalf("windows: %v %v %v %v", day, nextDay, month, nextMonth)
	}
	err := error(&allowanceError{"used up"})
	if !errors.Is(err, ErrAllowance) || limitMessage(err) != "used up" || limitMessage(ErrBudget) != budgetMessage {
		t.Fatal("allowance errors carry their own message")
	}
}

func TestBuildProgressStages(t *testing.T) {
	id := 1
	c := &coursemodels.Course{CourseId: &id, OrganiseStatus: StatusIdle, ReferencesStatus: StatusIdle}
	mats := []Material{{Status: StatusReady}, {Status: StatusProcessing}}
	if p := buildProgress(c, mats, 0); !p.Active || p.Stage != "reading" || p.Percent != 22 {
		t.Fatalf("1 of 2 read: %+v", p)
	}
	mats[1].Status, mats[1].Reading = StatusReady, true
	c.OrganiseStatus = StatusProcessing
	if p := buildProgress(c, mats, 0); p.Stage != "organising" || p.Percent != 45 {
		t.Fatalf("planning: %+v", p)
	}
	c.OrganiseDone, c.OrganiseTotal = 2, 4
	if p := buildProgress(c, mats, 0); p.Percent != 70 {
		t.Fatalf("2 of 4 units written: %+v", p)
	}
	c.OrganiseStatus, c.ReferencesStatus, c.ReferencesDone, c.ReferencesTotal = StatusReady, StatusProcessing, 1, 2
	if p := buildProgress(c, mats, 4); p.Stage != "references" || p.Percent != 95 {
		t.Fatalf("references: %+v", p)
	}
	c.ReferencesStatus = StatusReady
	if p := buildProgress(c, mats, 4); p.Active || p.Percent != 100 || !p.More {
		t.Fatalf("ready, still reading the rest: %+v", p)
	}
	if buildProgress(c, nil, 0) != nil {
		t.Fatal("no materials: no bar")
	}
}

func TestFirstPartsAreShort(t *testing.T) {
	l := Limits{PartPages: 25, FirstPartPages: 8, VideoPartSeconds: 600, FirstVideoSeconds: 180}
	if l.partPages(true) != 8 || l.partPages(false) != 25 || l.videoPart(true) != 180 || l.videoPart(false) != 600 {
		t.Fatal("first parts use the short size")
	}
	l.FirstPartPages, l.FirstVideoSeconds = 0, 0
	if l.partPages(true) != 25 || l.videoPart(true) != 600 {
		t.Fatal("no first size: the normal size")
	}
}
