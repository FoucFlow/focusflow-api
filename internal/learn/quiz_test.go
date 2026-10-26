package learn

import (
	"testing"
	"time"
)

func TestCSVTemplateImportsEveryKind(t *testing.T) {
	qs, errs := parseCSVQuestions(CSVTemplate)
	if len(errs) != 0 {
		t.Fatalf("template errors: %+v", errs)
	}
	kinds := []string{QuestionMCQ, QuestionMulti, QuestionTrueFalse, QuestionShort, QuestionExplain}
	if len(qs) != len(kinds) {
		t.Fatalf("got %d questions", len(qs))
	}
	for i, k := range kinds {
		if qs[i].Kind != k {
			t.Errorf("row %d kind %q, want %q", i+2, qs[i].Kind, k)
		}
	}
	if qs[0].Correct != 0 || len(qs[1].Corrects) != 2 || qs[1].Corrects[1] != 2 || qs[2].Correct != 0 {
		t.Errorf("answers: %+v %+v %+v", qs[0], qs[1], qs[2])
	}
}

func TestCSVImportReportsLines(t *testing.T) {
	csv := "type,question,option_a,option_b,correct,answer_guide\n" +
		"mcq,Fine?,yes,no,A,\n" +
		"mcq,Bad letter?,yes,no,C,\n" +
		"short,No guide?,,,,\n" +
		"essay,Unknown?,,,,x\n"
	qs, errs := parseCSVQuestions(csv)
	if len(qs) != 1 || len(errs) != 3 || errs[0].Line != 3 || errs[1].Line != 4 || errs[2].Line != 5 {
		t.Fatalf("qs=%d errs=%+v", len(qs), errs)
	}
	if _, errs := parseCSVQuestions("question\nx\n"); len(errs) != 1 || errs[0].Line != 1 {
		t.Errorf("missing type column: %+v", errs)
	}
}

func TestAikenImport(t *testing.T) {
	text := "What is 2+2?\nA. 3\nB. 4\nC. 5\nANSWER: B\n\nNo answer here\nA. x\nB. y\n\nCapital of France?\nA) Paris\nB) Rome\nANSWER: A\n"
	qs, errs := parseAiken(text)
	if len(qs) != 2 || qs[0].Correct != 1 || qs[1].Options[0] != "Paris" {
		t.Fatalf("qs=%+v", qs)
	}
	if len(errs) != 1 || errs[0].Line != 7 {
		t.Errorf("errs=%+v", errs)
	}
}

func TestGIFTImport(t *testing.T) {
	text := `// a comment
::Q1:: What stops recursion? {=A base case ~A loop ~A print#no}

::Q2:: Recursion needs a base case. {T}

::Q3:: Which are recursive? {~%50%fact ~%50%fib ~%-100%loop}

::Q4:: Name the stack used for calls. {=call stack =stack}

::Q5:: Explain recursion. {####- calls itself on a smaller problem}

::Q6:: Broken {~a ~b}`
	qs, errs := parseGIFT(text)
	if len(qs) != 5 {
		t.Fatalf("got %d questions: %+v errs=%+v", len(qs), qs, errs)
	}
	want := []string{QuestionMCQ, QuestionTrueFalse, QuestionMulti, QuestionShort, QuestionExplain}
	for i, k := range want {
		if qs[i].Kind != k {
			t.Errorf("Q%d kind %q, want %q", i+1, qs[i].Kind, k)
		}
	}
	if qs[0].Options[2] != "A print" || qs[0].Correct != 0 {
		t.Errorf("mcq: %+v", qs[0])
	}
	if len(qs[2].Corrects) != 2 || qs[2].Corrects[0] != 0 || qs[2].Corrects[1] != 1 {
		t.Errorf("multi: %+v", qs[2])
	}
	if len(errs) != 1 || errs[0].Line != 12 {
		t.Errorf("errs=%+v", errs)
	}
}

func TestMarkChoice(t *testing.T) {
	one, two := 1, 2
	mcq := AttemptItem{Kind: QuestionMCQ, Correct: 1, Choice: &one}
	if markChoice(&mcq) != 1 {
		t.Error("right mcq")
	}
	mcq.Choice = &two
	if markChoice(&mcq) != 0 {
		t.Error("wrong mcq")
	}
	multi := func(choices ...int) float64 {
		it := AttemptItem{Kind: QuestionMulti, Corrects: []int{0, 2}, Choices: choices}
		return markChoice(&it)
	}
	if multi(0, 2) != 1 || multi(2) != 0.5 || multi(0, 1) != 0 || multi() != 0 {
		t.Errorf("multi marks: %v %v %v %v", multi(0, 2), multi(2), multi(0, 1), multi())
	}
}

func TestLevelsRoundTrip(t *testing.T) {
	for _, lv := range []string{LevelPractice, LevelStandard, LevelStrict, LevelSupervised} {
		st := defaultTestSettings(1, 1)
		if err := applyLevel(&st, lv); err != nil {
			t.Fatal(err)
		}
		if got := levelOf(st); got != lv {
			t.Errorf("%s reads back as %s", lv, got)
		}
	}
	st := defaultTestSettings(1, 1)
	_ = applyLevel(&st, LevelStrict)
	st.MaxAttempts = 5
	if levelOf(st) != LevelCustom {
		t.Error("a changed preset is custom")
	}
	_ = applyLevel(&st, LevelSupervised)
	if len(st.AccessCode) != 6 || !st.ExplainAnswers || st.TimeLimitMinutes == 0 {
		t.Errorf("supervised: %+v", st)
	}
	if applyLevel(&st, "exam") == nil {
		t.Error("unknown level accepted")
	}
}

func TestWindowAndAnswersVisible(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	if ok, _ := windowOpen(TestSettings{OpensAt: &future}, now); ok {
		t.Error("not open yet")
	}
	if ok, _ := windowOpen(TestSettings{ClosesAt: &past}, now); ok {
		t.Error("closed")
	}
	if ok, _ := windowOpen(TestSettings{OpensAt: &past, ClosesAt: &future}, now); !ok {
		t.Error("open")
	}
	if answersVisible(TestSettings{ShowAnswers: "after_close", ClosesAt: &future}, now, true) ||
		!answersVisible(TestSettings{ShowAnswers: "after_close", ClosesAt: &past}, now, false) ||
		answersVisible(TestSettings{ShowAnswers: "never"}, now, true) || !answersVisible(TestSettings{ShowAnswers: "after_submit"}, now, false) ||
		answersVisible(TestSettings{ShowAnswers: "when_finished"}, now, false) || !answersVisible(TestSettings{ShowAnswers: "when_finished"}, now, true) {
		t.Error("answer visibility")
	}
}
