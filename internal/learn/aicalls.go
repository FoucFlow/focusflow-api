package learn

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // America/Los_Angeles in minimal containers

	"gorm.io/gorm"
)

// AICall is one request to an AI provider. A course-builder task (one
// ai_usage row) can make many calls; provider quotas count calls, so this is
// what measures them (requests per minute/day, tokens per minute).
type AICall struct {
	ID           int       `json:"id" gorm:"primaryKey"`
	Provider     string    `json:"provider" gorm:"size:16;not null"`
	Model        string    `json:"model" gorm:"size:64;not null;index:idx_ai_calls_model_time"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	OK           bool      `json:"ok"`
	QuotaError   bool      `json:"quota_error"`
	Error        string    `json:"error,omitempty" gorm:"type:text"`
	CreatedAt    time.Time `json:"created_at" gorm:"index:idx_ai_calls_model_time"`
}

func (AICall) TableName() string { return "ai_calls" }

// CallRecorder is told about every provider request.
type CallRecorder func(provider, model string, in, out int64, err error)

// recorder stores calls; it never blocks the AI work for long.
func recorder(db *gorm.DB) CallRecorder {
	return func(provider, model string, in, out int64, err error) {
		row := AICall{Provider: provider, Model: model, InputTokens: in, OutputTokens: out, OK: err == nil}
		if err != nil {
			row.Error = clip(err.Error(), 4000)
			row.QuotaError = strings.Contains(row.Error, "429") || strings.Contains(row.Error, "RESOURCE_EXHAUSTED")
		}
		db.Create(&row)
	}
}

// quotaDayStart: Gemini's daily quotas reset at midnight Pacific time.
func quotaDayStart(now time.Time) (start, next time.Time) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		loc = time.UTC
	}
	n := now.In(loc)
	start = time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	return start, start.AddDate(0, 0, 1)
}

// GoogleLimit is a quota Google reported in a 429 error.
type GoogleLimit struct {
	Metric string `json:"metric"` // e.g. "requests per day"
	Limit  int64  `json:"limit"`
}

var quotaLine = regexp.MustCompile(`Quota exceeded for metric: [^,]*?(input_token_count|requests)[^,]*, limit: (\d+), model: ([\w.\-]+), description: ([^.\n]*)`)

// parseGoogleLimits reads "Quota exceeded for metric: ..., limit: 250, model:
// gemini-2.5-flash, description: Generate content requests per day ..." lines.
func parseGoogleLimits(msg string) []GoogleLimit {
	var out []GoogleLimit
	seen := map[string]bool{}
	for _, m := range quotaLine.FindAllStringSubmatch(msg, -1) {
		what := "requests"
		if m[1] == "input_token_count" {
			what = "input tokens"
		}
		per := "per minute"
		if strings.Contains(strings.ToLower(m[4]), "per day") {
			per = "per day"
		}
		metric := what + " " + per
		if strings.Contains(strings.ToLower(m[4]), "free tier") {
			metric += " (free tier)"
		}
		if seen[metric] {
			continue
		}
		seen[metric] = true
		n, _ := strconv.ParseInt(m[2], 10, 64)
		out = append(out, GoogleLimit{Metric: metric, Limit: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metric < out[j].Metric })
	return out
}

// ModelQuota is one model's usage against its quotas.
type ModelQuota struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Today    struct {
		Requests     int64 `json:"requests"`
		OK           int64 `json:"ok"`
		QuotaErrors  int64 `json:"quota_errors"`
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"today"`
	LastMinute struct {
		Requests    int64 `json:"requests"`
		InputTokens int64 `json:"input_tokens"`
	} `json:"last_minute"`
	LastQuotaError *struct {
		At      time.Time     `json:"at"`
		Limits  []GoogleLimit `json:"limits"` // what Google said the limits are
		Message string        `json:"message"`
	} `json:"last_quota_error,omitempty"`
}

// AIQuota is today's AI usage per model (since the Pacific-time reset).
func (s *Service) AIQuota() map[string]any {
	now := time.Now()
	start, next := quotaDayStart(now)
	type agg struct {
		Provider, Model                                string
		Requests, OK, QuotaErrors, InputTok, OutputTok int64
	}
	var today []agg
	s.db.Model(&AICall{}).Where("created_at >= ?", start).
		Select("provider, model, COUNT(*) AS requests, SUM(CASE WHEN ok THEN 1 ELSE 0 END) AS ok, " +
			"SUM(CASE WHEN quota_error THEN 1 ELSE 0 END) AS quota_errors, COALESCE(SUM(input_tokens),0) AS input_tok, COALESCE(SUM(output_tokens),0) AS output_tok").
		Group("provider, model").Scan(&today)
	var minute []agg
	s.db.Model(&AICall{}).Where("created_at >= ?", now.Add(-time.Minute)).
		Select("provider, model, COUNT(*) AS requests, COALESCE(SUM(input_tokens),0) AS input_tok").Group("provider, model").Scan(&minute)

	byModel := map[string]*ModelQuota{}
	get := func(provider, model string) *ModelQuota {
		if q, ok := byModel[model]; ok {
			return q
		}
		q := &ModelQuota{Provider: provider, Model: model}
		byModel[model] = q
		return q
	}
	for _, a := range today {
		q := get(a.Provider, a.Model)
		q.Today.Requests, q.Today.OK, q.Today.QuotaErrors, q.Today.InputTokens, q.Today.OutputTokens = a.Requests, a.OK, a.QuotaErrors, a.InputTok, a.OutputTok
	}
	for _, a := range minute {
		q := get(a.Provider, a.Model)
		q.LastMinute.Requests, q.LastMinute.InputTokens = a.Requests, a.InputTok
	}
	// The latest quota error of each model (last 7 days) tells its limits
	var errs []AICall
	s.db.Where("quota_error AND created_at >= ?", now.AddDate(0, 0, -7)).Order("id DESC").Limit(200).Find(&errs)
	for _, e := range errs {
		q := get(e.Provider, e.Model)
		if q.LastQuotaError != nil {
			continue
		}
		q.LastQuotaError = &struct {
			At      time.Time     `json:"at"`
			Limits  []GoogleLimit `json:"limits"`
			Message string        `json:"message"`
		}{At: e.CreatedAt, Limits: parseGoogleLimits(e.Error), Message: clip(e.Error, 600)}
	}
	models := make([]*ModelQuota, 0, len(byModel))
	for _, q := range byModel {
		models = append(models, q)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Today.Requests > models[j].Today.Requests })
	return map[string]any{
		"day_start": start, "resets_at": next, "now": now, "models": models,
		"month_spend_usd": s.MonthSpendUSD(), "monthly_budget_usd": s.limits.MonthlyBudgetUSD,
	}
}
