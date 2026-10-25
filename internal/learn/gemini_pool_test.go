package learn

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPoolMovesTheRequestToTheNextKey(t *testing.T) {
	p, err := geminiPoolFor(context.Background(), []string{"k-one", "k-two", "k-one", " ", "k-three"})
	if err != nil || len(p.keys) != 3 {
		t.Fatalf("3 distinct keys: %v %v", err, p)
	}
	now := time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	daily := errors.New("Error 429, Quota exceeded for metric: generate_content_free_tier_requests, limit: 20, model: gemini-2.5-flash, description: Generate content requests per day limit exceeded. Please retry in 15h10m8.13s.")
	perMinute := errors.New("Error 429, RESOURCE_EXHAUSTED: requests per minute. Please retry in 31.5s.")

	var used []string
	err = p.do("flash", func(k *poolKey) error {
		used = append(used, k.name)
		if k.name == "key 1" {
			return daily
		}
		return nil
	})
	if err != nil || len(used) != 2 || used[1] != "key 2" {
		t.Fatalf("key 1 over quota → key 2 answers: %v %v", err, used)
	}
	// key 1 rests until midnight Pacific; other models are not affected
	if k := p.keys[0]; !k.rest["flash"].Equal(time.Date(2026, 10, 26, 7, 0, 0, 0, time.UTC)) || !k.rest["pro"].IsZero() {
		t.Fatalf("rest: %v", k.rest)
	}
	used = nil
	_ = p.do("flash", func(k *poolKey) error { used = append(used, k.name); return perMinute })
	if len(used) != 2 { // keys 3 and 2; key 1 rests
		t.Fatalf("resting key skipped: %v", used)
	}
	if got := p.keys[2].rest["flash"]; !got.Equal(now.Add(31500*time.Millisecond + time.Second)) {
		t.Fatalf("per-minute rest from 'retry in': %v", got)
	}
	called := false
	err = p.do("flash", func(*poolKey) error { called = true; return nil })
	if !errors.Is(err, ErrAllKeysResting) || called {
		t.Fatalf("all resting: no request is sent: %v", err)
	}
	other := errors.New("Error 400: bad request")
	if err := p.do("pro", func(*poolKey) error { return other }); err != other {
		t.Fatalf("other errors are returned as is: %v", err)
	}
	if retryableGemini(errors.New("x: " + daily.Error())) {
		t.Fatal("a daily limit is not retried")
	}
}
