package learn

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

// Prices in USD per million tokens (input, output), for the usage log.
var claudePrices = map[string][2]float64{
	"claude-opus-5-5":   {4, 20},
	"claude-sonnet-5-5": {2, 10},
	"claude-haiku-5-5":  {0.10, 0.50},
}

// claudeAI implements AI with Claude.
type claudeAI struct {
	client        anthropic.Client
	digestModel   string // cheap bulk reading
	organiseModel string // careful structuring and writing
	enrichModel   string // web search for references
}

// NewClaudeAI returns the Claude provider, or a disabled one without a key.
func NewClaudeAI(apiKey, digestModel, organiseModel, enrichModel string) AI {
	if apiKey == "" {
		return disabledAI{}
	}
	if digestModel == "" {
		digestModel = "claude-haiku-5-5"
	}
	if organiseModel == "" {
		organiseModel = "claude-opus-5-5"
	}
	if enrichModel == "" {
		enrichModel = organiseModel
	}
	return &claudeAI{
		// The SDK retries 408/409/429/5xx and connection errors with backoff
		client:        anthropic.NewClient(option.WithAPIKey(apiKey), option.WithMaxRetries(4)),
		digestModel:   digestModel,
		organiseModel: organiseModel,
		enrichModel:   enrichModel,
	}
}

const digestSystem = `You read one study material (lecture notes, slides, or a video or web page description) for a course builder.
Return:
- summary: 2 to 4 sentences on what the material covers.
- topics: the main topics.
- key_points: the most important takeaways.
- concepts: every distinct concept the material teaches (usually 3 to 15), in the order they appear. For each:
  - name: a short name.
  - summary: one or two sentences on how THIS material explains it.
  - details: what the material actually says about it, faithfully and specifically: its definitions, steps, rules, formulas, worked examples, diagrams described in words, and any warnings or common mistakes it mentions (80 to 300 words). Keep the material's terminology. This is used to write accurate lessons, so do not generalise or add outside knowledge.
  - pages: for a PDF, the page numbers where it is covered (1 = first page of the file); slides: for slides, the slide numbers (from the "Slide N:" labels). Leave the other list empty.
Use only what is in the material; do not invent content. Write in the material's own language.`

const organiseSystem = `You are an expert teacher turning a creator's study materials into a course for self-taught learners and students. You get each material's summary and the concepts found in it, each with what the material says about it ("details") and a reference key (M12 is a material, M12-C3 is concept 3 of material 12).

Build the course:
- 3 to 10 units in a sensible learning order, from foundations to advanced; prerequisites come before the ideas that need them.
- Each unit: a title, a short intro (2 to 4 sentences, Markdown) saying what the learner will be able to do after it, and its concepts in teaching order (usually 2 to 6).
- Each concept gets a clear title, a one-sentence summary, and an explanation in Markdown (150 to 400 words) that breaks the idea down correctly, in this order, using bold labels:
  **What it is**: a plain-language definition.
  **How it works**: the idea step by step.
  **Example**: a concrete example, taken from the materials whenever they have one.
  **Watch out**: a common mistake or misconception.
  **Check yourself**: one short question the learner can answer to test their understanding (no answer given).
  Base every fact on the materials' details. Where they are thin, say less rather than guessing; never contradict them. Define every technical term the first time it appears.
- sources: the reference keys where learners can check the concept. When the same concept appears in several materials (notes, slides, videos), merge it into one concept and list every source, so a learner can read it in the notes and watch it in the video. Add a related link (its M key) when its title or description clearly matches.
- Cover every concept from the materials somewhere. Use only the reference keys given. Write in the course language.`

const enrichSystem = `You help learners go beyond a course. For each concept you are given, use web search to find 2 or 3 high-quality, free resources that explain it well for a learner: official documentation, university course pages (for example MIT OpenCourseWare), Khan Academy, Wikipedia, well-known tutorial sites, or respected educational videos. Prefer resources in the course language; English is fine when nothing good exists in that language.
Only use pages that appear in your search results. Never write a URL from memory.
After searching, answer with ONLY a JSON object, no other text:
{"concepts":[{"index":1,"references":[{"title":"...","url":"https://...","why":"one line on what the learner gains"}]}]}`

func digestSchema() map[string]any {
	intList := map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}
	strList := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary":    map[string]any{"type": "string"},
			"topics":     strList,
			"key_points": strList,
			"concepts": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":    map[string]any{"type": "string"},
						"summary": map[string]any{"type": "string"},
						"details": map[string]any{"type": "string"},
						"pages":   intList,
						"slides":  intList,
					},
					"required":             []string{"name", "summary", "details", "pages", "slides"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"summary", "topics", "key_points", "concepts"},
		"additionalProperties": false,
	}
}

func outlineSchema() map[string]any {
	concept := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":       map[string]any{"type": "string"},
			"explanation": map[string]any{"type": "string"},
			"sources":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required":             []string{"title", "explanation", "sources"},
		"additionalProperties": false,
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"units": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title":    map[string]any{"type": "string"},
						"intro":    map[string]any{"type": "string"},
						"concepts": map[string]any{"type": "array", "items": concept},
					},
					"required":             []string{"title", "intro", "concepts"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"units"},
		"additionalProperties": false,
	}
}

func (a *claudeAI) Digest(ctx context.Context, in DigestInput) (Digest, Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()

	var blocks []anthropic.BetaContentBlockParamUnion
	switch {
	case in.Kind == KindPDF && len(in.PDF) > 0:
		blocks = append(blocks, anthropic.NewBetaDocumentBlock(anthropic.BetaBase64PDFSourceParam{
			Data: base64.StdEncoding.EncodeToString(in.PDF),
		}))
		blocks = append(blocks, anthropic.NewBetaTextBlock("Title: "+in.Title+"\nDigest this PDF."))
	default:
		blocks = append(blocks, anthropic.NewBetaTextBlock(fmt.Sprintf(
			"Material type: %s\nTitle: %s\n\n<material>\n%s\n</material>\n\nDigest this material.", in.Kind, in.Title, in.Text)))
	}

	var d Digest
	u, err := a.call(ctx, a.digestModel, digestSystem, blocks, digestSchema(), 16000, &d)
	return d, u, err
}

func (a *claudeAI) Organise(ctx context.Context, in OrganiseInput) (Outline, Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()

	materials, _ := json.Marshal(in.Materials)
	prompt := fmt.Sprintf("Course title: %s\nCourse description: %s\nCourse language: %s\n\n<materials>\n%s\n</materials>\n\nBuild the course from these materials and concepts.",
		in.CourseTitle, in.CourseDescription, in.Language, materials)

	var o Outline
	u, err := a.call(ctx, a.organiseModel, organiseSystem,
		[]anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(prompt)}, outlineSchema(), 64000, &o)
	return o, u, err
}

// call sends one structured-output request and decodes the JSON into out.
func (a *claudeAI) call(ctx context.Context, model, system string, blocks []anthropic.BetaContentBlockParamUnion,
	schema map[string]any, maxTokens int64, out any) (Usage, error) {

	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTokens,
		System:    []anthropic.BetaTextBlockParam{{Text: system}},
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(blocks...)},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Format: anthropic.BetaJSONOutputFormatParam{Schema: schema},
		},
	}
	if strings.HasPrefix(model, "claude-opus") || strings.HasPrefix(model, "claude-sonnet") {
		// If a request is declined by a safety classifier, the API re-runs it
		// on a fallback model inside the same call (Haiku has no fallback)
		params.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
	}
	if strings.HasPrefix(model, "claude-opus") {
		params.OutputConfig.Effort = anthropic.BetaOutputConfigEffortMedium
	}

	// Stream long answers so the HTTP request can't time out; short ones in one call
	var resp *anthropic.BetaMessage
	var err error
	if maxTokens > 16000 {
		stream := a.client.Beta.Messages.NewStreaming(ctx, params)
		msg := anthropic.BetaMessage{}
		for stream.Next() {
			if aerr := msg.Accumulate(stream.Current()); aerr != nil {
				err = aerr
				break
			}
		}
		if err == nil {
			err = stream.Err()
		}
		resp = &msg
	} else {
		resp, err = a.client.Beta.Messages.New(ctx, params)
	}
	u := Usage{Provider: "claude", Model: model}
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return u, fmt.Errorf("claude %d: %s", apiErr.StatusCode, apiErr.Error())
		}
		return u, fmt.Errorf("claude: %w", err)
	}
	u.Model = string(resp.Model)
	u.InputTokens = resp.Usage.InputTokens + resp.Usage.CacheReadInputTokens + resp.Usage.CacheCreationInputTokens
	u.OutputTokens = resp.Usage.OutputTokens
	if p, ok := claudePrices[model]; ok {
		u.CostMicroUSD = int64(float64(u.InputTokens)*p[0] + float64(u.OutputTokens)*p[1])
	}

	switch resp.StopReason {
	case anthropic.BetaStopReasonRefusal:
		return u, fmt.Errorf("the AI declined this material")
	case anthropic.BetaStopReasonMaxTokens:
		return u, fmt.Errorf("the AI response was cut off (too long); try fewer or smaller materials")
	}
	var text strings.Builder
	for _, b := range resp.Content {
		if t, ok := b.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	if err := json.Unmarshal([]byte(text.String()), out); err != nil {
		return u, fmt.Errorf("the AI returned an unreadable answer: %w", err)
	}
	return u, nil
}

// FindReferences searches the web for each concept of one unit and keeps only
// URLs that appeared in the search results of this very call.
func (a *claudeAI) FindReferences(ctx context.Context, in EnrichInput) (map[int][]Reference, Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	model := a.enrichModel
	u := Usage{Provider: "claude", Model: model}

	concepts, _ := json.Marshal(in.Concepts)
	prompt := fmt.Sprintf("Course: %s\nCourse language: %s\nUnit: %s\n\n<concepts>\n%s\n</concepts>\n\nFind references for each concept.",
		in.CourseTitle, in.Language, in.UnitTitle, concepts)
	messages := []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(prompt))}
	maxSearches := int64(2 + 2*len(in.Concepts))
	if maxSearches > 12 {
		maxSearches = 12
	}
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: 16000,
		System:    []anthropic.BetaTextBlockParam{{Text: enrichSystem}},
		Tools: []anthropic.BetaToolUnionParam{{OfWebSearchTool20260209: &anthropic.BetaWebSearchTool20260209Param{
			MaxUses: anthropic.Int(maxSearches),
		}}},
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortMedium},
	}
	if strings.HasPrefix(model, "claude-opus") || strings.HasPrefix(model, "claude-sonnet") {
		params.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
	}

	found := map[string]string{} // URL from search results → its title
	var text strings.Builder
	// The server can pause a long search turn; continue it a few times
	for turn := 0; turn < 4; turn++ {
		params.Messages = messages
		resp, err := a.client.Beta.Messages.New(ctx, params)
		if err != nil {
			return nil, u, fmt.Errorf("claude: %w", err)
		}
		u.InputTokens += resp.Usage.InputTokens + resp.Usage.CacheReadInputTokens + resp.Usage.CacheCreationInputTokens
		u.OutputTokens += resp.Usage.OutputTokens
		text.Reset()
		for _, b := range resp.Content {
			switch v := b.AsAny().(type) {
			case anthropic.BetaWebSearchToolResultBlock:
				for _, r := range v.Content.OfBetaWebSearchResultBlockArray {
					found[normaliseURL(r.URL)] = r.Title
				}
			case anthropic.BetaTextBlock:
				text.WriteString(v.Text)
			}
		}
		if resp.StopReason == anthropic.BetaStopReasonRefusal {
			return nil, u, fmt.Errorf("the AI declined this request")
		}
		if resp.StopReason != anthropic.BetaStopReasonPauseTurn {
			break
		}
		messages = append(messages, resp.ToParam())
	}
	if p, ok := claudePrices[model]; ok {
		u.CostMicroUSD = int64(float64(u.InputTokens)*p[0] + float64(u.OutputTokens)*p[1])
	}

	var out struct {
		Concepts []struct {
			Index      int `json:"index"`
			References []struct {
				Title string `json:"title"`
				URL   string `json:"url"`
				Why   string `json:"why"`
			} `json:"references"`
		} `json:"concepts"`
	}
	if err := json.Unmarshal([]byte(extractJSON(text.String())), &out); err != nil {
		return nil, u, fmt.Errorf("the AI returned unreadable references: %w", err)
	}
	refs := map[int][]Reference{}
	for _, c := range out.Concepts {
		seen := map[string]bool{}
		for _, r := range c.References {
			key := normaliseURL(r.URL)
			title, ok := found[key]
			if !ok || seen[key] || len(refs[c.Index]) >= 3 {
				continue // not from this call's search results: drop it
			}
			seen[key] = true
			if strings.TrimSpace(r.Title) != "" {
				title = r.Title
			}
			refs[c.Index] = append(refs[c.Index], Reference{Title: clip(title, 255), URL: r.URL, Site: siteOf(r.URL), Why: clip(r.Why, 300)})
		}
	}
	return refs, u, nil
}

// extractJSON takes the JSON object out of a reply (fenced or bare).
func extractJSON(s string) string {
	if i := strings.Index(s, "```json"); i >= 0 {
		s = s[i+7:]
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
	}
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return ""
	}
	return s[i : j+1]
}

func normaliseURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(raw)
	}
	u.Fragment = ""
	u.Host = strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	u.Scheme = "https"
	return strings.TrimRight(u.String(), "/")
}

func siteOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	}
	return ""
}
