package learn

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/genai"
)

// VideoAI watches a YouTube video and returns its concepts with timestamps.
// Documents go to Claude; videos go to Gemini (FEATURES.md section 8).
type VideoAI interface {
	DigestVideo(ctx context.Context, youtubeID, title, note string) (Digest, Usage, error)
	// DigestVideoPart watches only From..To (seconds) of a long video.
	DigestVideoPart(ctx context.Context, youtubeID, title, note string, part VideoPart) (Digest, Usage, error)
}

// VideoPart is one stretch of a long video, and what earlier parts found.
type VideoPart struct {
	From, To, Total int      // seconds
	Earlier         []string // concept names found in earlier parts
}

// videoPartPrompt tells the model which stretch to watch and what it saw before.
func videoPartPrompt(p VideoPart) string {
	s := fmt.Sprintf("This is one part of a long video: you are given only %s to %s of a %s video. Watch ALL of this part. "+
		"Give start_seconds counted from the start of the WHOLE video (so between %d and %d).",
		clock(p.From), clock(p.To), clock(p.Total), p.From, p.To)
	if len(p.Earlier) > 0 {
		names, _ := json.Marshal(p.Earlier)
		s += "\nEarlier parts of this video already covered these concepts: " + string(names) +
			". Continue from there: do not list them again, unless this part teaches something new about one of them (then name what is new)."
	}
	return s
}

// clock formats seconds as m:ss or h:mm:ss.
func clock(sec int) string {
	if sec >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", sec/3600, sec%3600/60, sec%60)
	}
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

// Approximate prices in USD per million tokens (input, output), for the usage log.
var geminiPrices = map[string][2]float64{
	"gemini-2.5-flash":      {0.30, 2.50},
	"gemini-2.5-flash-lite": {0.10, 0.40},
}

type geminiVideoAI struct {
	pool   *geminiPool
	model  string
	record CallRecorder
}

// SetRecorder logs every request (for quota tracking).
func (g *geminiVideoAI) SetRecorder(r CallRecorder) { g.record = r }

// NewGeminiVideoAI returns nil without a key (videos then use title and note only).
func NewGeminiVideoAI(ctx context.Context, keys []string, model string) (VideoAI, error) {
	pool, err := geminiPoolFor(ctx, keys)
	if err != nil || pool == nil {
		return nil, err
	}
	if model == "" {
		model = "gemini-2.5-flash"
	}
	return &geminiVideoAI{pool: pool, model: model}, nil
}

const videoDigestPrompt = `Watch this ENTIRE video, from start to end, for a course builder. Return:
- summary: 2 to 4 sentences on what the video teaches.
- topics: the main topics.
- key_points: the most important takeaways.
- concepts: EVERY distinct concept the video teaches, in the order they appear. There is no maximum: a short clip may have 3, a one-hour lecture 30 or more. Split broad topics into separate concepts a learner would study one at a time. For each: a short name; a 1 to 3 sentence summary of how THIS video explains it; details: what the video actually says and shows about it, faithfully and specifically (definitions, steps, worked examples, code or formulas shown on screen, warnings), 80 to 300 words, keeping the video's own examples; and start_seconds: the moment (in seconds from the start) where the explanation of that concept begins.
Use only what is in the video; do not invent content. Write in the video's own language.`

func videoDigestSchema() *genai.Schema {
	str := &genai.Schema{Type: genai.TypeString}
	return &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"summary":    str,
			"topics":     {Type: genai.TypeArray, Items: str},
			"key_points": {Type: genai.TypeArray, Items: str},
			"concepts": {Type: genai.TypeArray, Items: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"name":          str,
					"summary":       str,
					"details":       str,
					"start_seconds": {Type: genai.TypeInteger},
				},
				Required: []string{"name", "summary", "details", "start_seconds"},
			}},
		},
		Required: []string{"summary", "topics", "key_points", "concepts"},
	}
}

func (g *geminiVideoAI) DigestVideo(ctx context.Context, youtubeID, title, note string) (Digest, Usage, error) {
	return g.watch(ctx, youtubeID, title, note, nil)
}

func (g *geminiVideoAI) DigestVideoPart(ctx context.Context, youtubeID, title, note string, part VideoPart) (Digest, Usage, error) {
	return g.watch(ctx, youtubeID, title, note, &part)
}

// watch digests the whole video, or only part of it.
func (g *geminiVideoAI) watch(ctx context.Context, youtubeID, title, note string, part *VideoPart) (Digest, Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	u := Usage{Provider: "gemini", Model: g.model}

	intro := "Video title: " + title
	if strings.TrimSpace(note) != "" {
		intro += "\nCreator's note: " + note
	}
	video := &genai.Part{FileData: &genai.FileData{FileURI: "https://www.youtube.com/watch?v=" + youtubeID, MIMEType: "video/*"},
		// One frame every 2 s (default 1 per second): lectures change slowly,
		// and frames are most of a video's tokens
		VideoMetadata: &genai.VideoMetadata{FPS: genai.Ptr(videoFPS)}}
	prompt := videoDigestPrompt
	if part != nil {
		video.VideoMetadata.StartOffset, video.VideoMetadata.EndOffset = time.Duration(part.From)*time.Second, time.Duration(part.To)*time.Second
		prompt = strings.Replace(prompt, "Watch this ENTIRE video, from start to end,", "Watch this part of the video,", 1) + "\n\n" + videoPartPrompt(*part)
	}
	parts := []*genai.Part{genai.NewPartFromText(intro), video, genai.NewPartFromText(prompt)}
	cfg := &genai.GenerateContentConfig{
		ResponseMIMEType: "application/json",
		ResponseSchema:   videoDigestSchema(),
		Temperature:      genai.Ptr[float32](0.2),
		// Low resolution: about a third of the tokens per second of video, so
		// long lectures fit; slides on screen are still readable enough
		MediaResolution: genai.MediaResolutionLow,
	}

	var resp *genai.GenerateContentResponse
	var err error
	// Gemini's free tier often answers 429/503 under load: back off and retry
	for attempt := 0; attempt < 4; attempt++ {
		err = g.pool.do(g.model, func(k *poolKey) error {
			var err error
			resp, err = k.client.Models.GenerateContent(ctx, g.model,
				[]*genai.Content{genai.NewContentFromParts(parts, genai.RoleUser)}, cfg)
			recordGemini(g.record, k, g.model, resp, err)
			return err
		})
		if err == nil || !retryableGemini(err) || ctx.Err() != nil {
			break
		}
		select {
		case <-time.After(time.Duration(5*(attempt+1)*(attempt+1)) * time.Second):
		case <-ctx.Done():
		}
	}
	if err != nil {
		return Digest{}, u, fmt.Errorf("gemini: %w", err)
	}
	if m := resp.UsageMetadata; m != nil {
		u.InputTokens = int64(m.PromptTokenCount)
		u.OutputTokens = int64(m.CandidatesTokenCount + m.ThoughtsTokenCount)
		if p, ok := geminiPrices[g.model]; ok {
			u.CostMicroUSD = int64(float64(u.InputTokens)*p[0] + float64(u.OutputTokens)*p[1])
		}
	}

	var out struct {
		Summary   string   `json:"summary"`
		Topics    []string `json:"topics"`
		KeyPoints []string `json:"key_points"`
		Concepts  []struct {
			Name         string `json:"name"`
			Summary      string `json:"summary"`
			Details      string `json:"details"`
			StartSeconds int    `json:"start_seconds"`
		} `json:"concepts"`
	}
	if err := json.Unmarshal([]byte(resp.Text()), &out); err != nil {
		return Digest{}, u, fmt.Errorf("gemini returned an unreadable answer: %w", err)
	}
	d := Digest{Summary: out.Summary, Topics: out.Topics, KeyPoints: out.KeyPoints}
	for _, c := range out.Concepts {
		start := c.StartSeconds
		if part != nil {
			// Some answers count from the start of the clip: move them into the part
			if start < part.From && part.From+start <= part.To {
				start += part.From
			}
			start = min(max(start, part.From), part.To)
		}
		if start < 0 {
			start = 0
		}
		d.Concepts = append(d.Concepts, SourceConcept{Name: c.Name, Summary: c.Summary, Details: c.Details, StartSeconds: &start})
	}
	return d, u, nil
}

// noQuota: the plan allows this model no use at all ("limit: 0"); waiting won't help.
const videoFPS = 0.5

func noQuota(err error) bool {
	s := err.Error()
	return strings.Contains(s, "429") && strings.Contains(s, "limit: 0")
}

// dailyQuota: a daily limit was hit (on every key): waiting minutes won't help.
func dailyQuota(err error) bool {
	s := err.Error()
	return strings.Contains(strings.ToLower(s), "per day") || strings.Contains(s, "PerDay")
}

func retryableGemini(err error) bool {
	if noQuota(err) || dailyQuota(err) {
		return false
	}
	s := err.Error()
	for _, code := range []string{"429", "500", "502", "503", "504", "RESOURCE_EXHAUSTED", "UNAVAILABLE", "DEADLINE"} {
		if strings.Contains(s, code) {
			return true
		}
	}
	return false
}
