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
}

// Approximate prices in USD per million tokens (input, output), for the usage log.
var geminiPrices = map[string][2]float64{
	"gemini-2.5-flash":      {0.30, 2.50},
	"gemini-2.5-flash-lite": {0.10, 0.40},
}

type geminiVideoAI struct {
	client *genai.Client
	model  string
}

// NewGeminiVideoAI returns nil without a key (videos then use title and note only).
func NewGeminiVideoAI(ctx context.Context, apiKey, model string) (VideoAI, error) {
	if apiKey == "" {
		return nil, nil
	}
	if model == "" {
		model = "gemini-2.5-flash"
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: apiKey, Backend: genai.BackendGeminiAPI})
	if err != nil {
		return nil, fmt.Errorf("gemini client: %w", err)
	}
	return &geminiVideoAI{client: client, model: model}, nil
}

const videoDigestPrompt = `Watch this video for a course builder. Return:
- summary: 2 to 4 sentences on what the video teaches.
- topics: the main topics.
- key_points: the most important takeaways.
- concepts: every distinct concept the video teaches (usually 3 to 15), in the order they appear. For each: a short name, a 1 to 3 sentence summary of how THIS video explains it, and start_seconds: the moment (in seconds from the start) where the explanation of that concept begins.
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
					"start_seconds": {Type: genai.TypeInteger},
				},
				Required: []string{"name", "summary", "start_seconds"},
			}},
		},
		Required: []string{"summary", "topics", "key_points", "concepts"},
	}
}

func (g *geminiVideoAI) DigestVideo(ctx context.Context, youtubeID, title, note string) (Digest, Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	u := Usage{Provider: "gemini", Model: g.model}

	intro := "Video title: " + title
	if strings.TrimSpace(note) != "" {
		intro += "\nCreator's note: " + note
	}
	parts := []*genai.Part{
		genai.NewPartFromText(intro),
		{FileData: &genai.FileData{FileURI: "https://www.youtube.com/watch?v=" + youtubeID, MIMEType: "video/*"}},
		genai.NewPartFromText(videoDigestPrompt),
	}
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
		resp, err = g.client.Models.GenerateContent(ctx, g.model,
			[]*genai.Content{genai.NewContentFromParts(parts, genai.RoleUser)}, cfg)
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
			StartSeconds int    `json:"start_seconds"`
		} `json:"concepts"`
	}
	if err := json.Unmarshal([]byte(resp.Text()), &out); err != nil {
		return Digest{}, u, fmt.Errorf("gemini returned an unreadable answer: %w", err)
	}
	d := Digest{Summary: out.Summary, Topics: out.Topics, KeyPoints: out.KeyPoints}
	for _, c := range out.Concepts {
		start := c.StartSeconds
		if start < 0 {
			start = 0
		}
		d.Concepts = append(d.Concepts, SourceConcept{Name: c.Name, Summary: c.Summary, StartSeconds: &start})
	}
	return d, u, nil
}

func retryableGemini(err error) bool {
	s := err.Error()
	for _, code := range []string{"429", "500", "502", "503", "504", "RESOURCE_EXHAUSTED", "UNAVAILABLE", "DEADLINE"} {
		if strings.Contains(s, code) {
			return true
		}
	}
	return false
}
