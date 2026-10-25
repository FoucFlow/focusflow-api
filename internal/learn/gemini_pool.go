package learn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"
)

// Several Gemini keys (GEMINI_API_KEYS) share the work: requests take turns,
// and a key that hits a quota rests until Google says it is free again
// (midnight Pacific for daily limits, about a minute for per-minute ones)
// while the next key takes the same request. Every request carries its whole
// context (the material part, what earlier parts found, the plan), so moving
// it to another key loses nothing. Quotas belong to a Google Cloud project:
// keys of the same project share one quota.

// ErrAllKeysResting: every key is over its quota for this model right now.
var ErrAllKeysResting = errors.New("every Gemini key is over its quota for now")

type poolKey struct {
	name   string // "key 3", for logs and the admin page (never the key itself)
	client *genai.Client
	rest   map[string]time.Time // model → resting until
}

type geminiPool struct {
	mu   sync.Mutex
	keys []*poolKey
	next int
	now  func() time.Time
}

var (
	poolsMu sync.Mutex
	pools   = map[string]*geminiPool{}
)

// geminiPoolFor returns the one shared pool for these keys (nil without keys),
// so the reader, the video watcher and the backup AI see the same resting keys.
func geminiPoolFor(ctx context.Context, keys []string) (*geminiPool, error) {
	keys = uniqueKeys(keys)
	if len(keys) == 0 {
		return nil, nil
	}
	id := strings.Join(keys, ",")
	poolsMu.Lock()
	defer poolsMu.Unlock()
	if p, ok := pools[id]; ok {
		return p, nil
	}
	p := &geminiPool{now: time.Now}
	for i, k := range keys {
		c, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: k, Backend: genai.BackendGeminiAPI})
		if err != nil {
			return nil, fmt.Errorf("gemini client (key %d): %w", i+1, err)
		}
		p.keys = append(p.keys, &poolKey{name: fmt.Sprintf("key %d", i+1), client: c, rest: map[string]time.Time{}})
	}
	pools[id] = p
	return p, nil
}

func uniqueKeys(keys []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k != "" && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// pick returns the next key not resting for model, in turn.
func (p *geminiPool) pick(model string, tried map[*poolKey]bool) *poolKey {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for i := 0; i < len(p.keys); i++ {
		k := p.keys[(p.next+i)%len(p.keys)]
		if tried[k] || now.Before(k.rest[model]) {
			continue
		}
		p.next = (p.next + i + 1) % len(p.keys)
		return k
	}
	return nil
}

func (p *geminiPool) restKey(k *poolKey, model string, until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if until.After(k.rest[model]) {
		k.rest[model] = until
	}
}

func (p *geminiPool) restAll(model string, until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, k := range p.keys {
		if until.After(k.rest[model]) {
			k.rest[model] = until
		}
	}
}

// do runs fn with one key after another until one is not over its quota.
// fn's error is returned as is when it is not a quota error.
func (p *geminiPool) do(model string, fn func(k *poolKey) error) error {
	tried := map[*poolKey]bool{}
	var last error
	for {
		k := p.pick(model, tried)
		if k == nil {
			if last == nil {
				return fmt.Errorf("%w (%s)", ErrAllKeysResting, model)
			}
			return fmt.Errorf("%w (%s): %v", ErrAllKeysResting, model, last)
		}
		tried[k] = true
		err := fn(k)
		if err != nil && overloaded(err) {
			// Busy for everyone: skip this model for a few minutes on every key
			p.restAll(model, p.now().Add(3*time.Minute))
			return fmt.Errorf("%w (%s overloaded): %v", ErrAllKeysResting, model, err)
		}
		if err == nil || !(quotaError(err) || modelUnavailable(err)) {
			return err
		}
		until := restUntil(err, p.now())
		if !noQuota(err) && modelUnavailable(err) {
			until = p.now().AddDate(0, 0, 7) // closed to this key's project: check again in a week
		}
		p.restKey(k, model, until)
		slog.Warn("gemini key over quota, trying the next one", "key", k.name, "model", model, "rest_until", until.Format(time.RFC3339))
		last = err
	}
}

// Status lists, per key, the models it is resting for (for the admin page).
func (p *geminiPool) Status() []map[string]any {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	out := make([]map[string]any, 0, len(p.keys))
	for _, k := range p.keys {
		resting := map[string]time.Time{}
		for m, t := range k.rest {
			if now.Before(t) {
				resting[m] = t
			}
		}
		out = append(out, map[string]any{"key": k.name, "resting": resting})
	}
	return out
}

func quotaError(err error) bool {
	s := err.Error()
	return strings.Contains(s, "429") || strings.Contains(s, "RESOURCE_EXHAUSTED")
}

var retryIn = regexp.MustCompile(`retry in ([0-9hms.]+?s)`)

// restUntil reads when a key is free again from Google's 429 message.
func restUntil(err error, now time.Time) time.Time {
	s := err.Error()
	_, midnight := quotaDayStart(now)
	if strings.Contains(s, "limit: 0") || strings.Contains(strings.ToLower(s), "per day") || strings.Contains(s, "PerDay") {
		return midnight // daily quotas (and models not in the plan) reset at midnight Pacific
	}
	if m := retryIn.FindStringSubmatch(s); m != nil {
		if d, err := time.ParseDuration(m[1]); err == nil && d > 0 {
			if d > 24*time.Hour {
				d = 24 * time.Hour
			}
			return now.Add(d + time.Second)
		}
	}
	return now.Add(time.Minute) // per-minute limits
}

// geminiKeyStatus is the resting state of every pool's keys.
func geminiKeyStatus() []map[string]any {
	poolsMu.Lock()
	ps := make([]*geminiPool, 0, len(pools))
	for _, p := range pools {
		ps = append(ps, p)
	}
	poolsMu.Unlock()
	var out []map[string]any
	for _, p := range ps {
		out = append(out, p.Status()...)
	}
	return out
}
