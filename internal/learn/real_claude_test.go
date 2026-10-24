package learn

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
)

// Manual check against the real API: REAL_CLAUDE_KEY=... go test -run TestRealOrganise -v ./internal/learn
func TestRealOrganise(t *testing.T) {
	key := os.Getenv("REAL_CLAUDE_KEY")
	if key == "" {
		t.Skip("set REAL_CLAUDE_KEY to run")
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	ai := NewClaudeAI(key, "", "", "").(*claudeAI)
	in := OrganiseInput{CourseTitle: "Recursion", Language: "en", Materials: []OrganiseMaterial{
		{Ref: "M1", Kind: "pdf", Title: "Week 5 notes", Summary: "Recursion, factorial, call stack, fibonacci, memoization",
			Concepts: []OrganiseConcept{
				{Ref: "M1-C1", Name: "Recursion and its two parts", Summary: "Base case and recursive case", Details: "A function is recursive when it calls itself on a smaller input. It needs a base case answered directly and a recursive case. Without a reachable base case: stack overflow."},
				{Ref: "M1-C2", Name: "Memoization", Summary: "Store computed results", Details: "Memoization stores fib(k) in a dict so each value is computed once: n steps instead of 1.6^n."},
			}},
		{Ref: "M2", Kind: "video", Title: "Recursion in 100 Seconds", Summary: "Short video on recursion",
			Concepts: []OrganiseConcept{{Ref: "M2-C1", Name: "Base Case", Summary: "Stops recursion", Details: "The base case returns without calling itself."}}},
	}}
	out, usage, err := ai.Organise(context.Background(), in)
	b, _ := json.MarshalIndent(out, "", "  ")
	t.Logf("err=%v usage=%+v", err, usage)
	if f := os.Getenv("REAL_OUT"); f != "" {
		_ = os.WriteFile(f, b, 0o600)
	}
}
