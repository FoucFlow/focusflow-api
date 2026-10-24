package learn

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

// Web search is billed per search: $10 per 1,000.
const webSearchMicroUSD = 10_000

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

const digestSystem = `You read one study material (lecture notes, a textbook chapter, slides, or a video or web page description) for a course builder. Read ALL of it, from the first page or slide to the last; do not stop early or sample.
Return:
- summary: 2 to 4 sentences on what the material covers.
- topics: the main topics, in order.
- key_points: the most important takeaways.
- concepts: EVERY distinct concept the material teaches, in the order it appears. There is no maximum: a few pages may have 3 concepts, a long chapter 40 or more. Split broad topics into separate concepts a learner would study one at a time. For each:
  - name: a short, specific name.
  - summary: one or two sentences on how THIS material explains it.
  - details: what the material actually says about it, faithfully and specifically: its definitions, steps, rules, formulas, code, worked examples, diagrams described in words, and any warnings or common mistakes (80 to 300 words). Keep the material's terminology and copy its examples. This is used to write accurate lessons, so do not generalise or add outside knowledge.
  - pages: for a PDF, the page numbers where it is covered (1 = first page of the file); slides: for slides, the slide numbers (from the "Slide N:" labels). Leave the other list empty.
Use only what is in the material; do not invent content. Write in the material's own language.`

const digestFormat = `

Answer with ONLY a JSON object, no other text, in exactly this shape:
{"summary":"...","topics":["..."],"key_points":["..."],"concepts":[{"name":"...","summary":"...","details":"...","pages":[1,2],"slides":[]}]}`

// validDigest rejects empty or skeleton readings.
func validDigest(d Digest) error {
	named := 0
	for _, c := range d.Concepts {
		if strings.TrimSpace(c.Name) != "" && !strings.Contains(c.Name, "placeholder") {
			named++
		}
	}
	if strings.TrimSpace(d.Summary) == "" || named == 0 {
		return fmt.Errorf("the AI returned an empty reading of this material")
	}
	return nil
}

const planSystem = `You are an expert teacher planning a course that a beginner can follow on their own, from a creator's study materials. You get every material's summary and ALL the concepts found in it (with what the material says, its "details") and a reference key (M12 is a material, M12-C3 is concept 3 of material 12).

Plan the units:
- First group concepts that belong together into topics, and merge duplicates: the same idea found in the notes, the slides and a video becomes ONE concept with all its sources.
- YOU decide how many units the material needs. Each unit is one coherent topic a learner can finish in one sitting. Short notes may make 1 or 2 units; a full textbook or a long lecture series may make 15 or more. Do not pad thin material and do not cram a large one into a few units.
- Order units so prerequisites come first, from foundations to advanced, and order the concepts inside each unit the same way.
- Every concept from the materials must appear in exactly one unit. Do not drop any.
- Each unit: a clear title, and an intro (2 to 4 sentences, Markdown) saying what the learner will understand and be able to do after it.
- Each concept: a clear title and its sources: the reference keys where learners can check it (concept keys like M1-C2, or a whole material like M3 for a clearly related link).
Use only the reference keys given. Write in the course language.

Answer with ONLY a JSON object, no other text, in exactly this shape:
{"units":[{"title":"...","intro":"...","concepts":[{"title":"...","sources":["M1-C2","M3"]}]}]}`

const writeSystem = `You are an expert teacher writing one unit of a course for beginners studying on their own. For each concept you get what the creator's materials say about it ("source excerpts"). Write an explanation a beginner understands on first reading, in Markdown, in this order, with these bold labels:
**What it is**: a plain-language definition in one or two sentences, then the precise definition from the materials.
**Think of it like this**: a short everyday analogy.
**How it works**: the idea step by step, defining every technical term the first time it appears.
**Example**: a concrete, worked example, shown step by step. Use the materials' own example whenever they have one; if they don't, write a simple one and add "(example added for clarity)".
**Watch out**: a common mistake or misconception.
**Check yourself**: one short question to test understanding (no answer given).
Usually 200 to 450 words per concept. Base every fact on the source excerpts and never contradict them; where they are thin, keep it short rather than inventing facts. Build on the earlier concepts of the unit instead of repeating them. Write in the course language.

Format: write the explanations in the order given, each starting on its own line with a marker "### CONCEPT <number>" (for example "### CONCEPT 1"), followed by the explanation in Markdown. Write nothing before the first marker and do not repeat the concept titles.`

// validOutline rejects empty or skeleton answers.
func validOutline(o Outline) error {
	if len(o.Units) == 0 {
		return fmt.Errorf("the AI proposed no units")
	}
	concepts := 0
	for _, u := range o.Units {
		if strings.TrimSpace(u.Title) == "" || strings.Contains(u.Title, "placeholder") {
			return fmt.Errorf("the AI returned an incomplete course outline")
		}
		for _, c := range u.Concepts {
			if strings.TrimSpace(c.Title) != "" && strings.TrimSpace(c.Explanation) != "" {
				concepts++
			}
		}
	}
	if concepts == 0 {
		return fmt.Errorf("the AI returned units without concepts")
	}
	return nil
}

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
	// Plain JSON answer, checked here: structured-output mode returned empty
	// skeletons on large documents. Generous budget (streamed above 16k).
	u, err := a.call(ctx, a.digestModel, digestSystem+digestFormat, blocks, nil, 48000, &d)
	if err == nil {
		err = validDigest(d)
	}
	return d, u, err
}

// Organise plans the units in one call, then writes each unit's explanations
// in its own call (three at a time), from that unit's source excerpts only.
func (a *claudeAI) Organise(ctx context.Context, in OrganiseInput) (Outline, Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	total := Usage{Provider: "claude", Model: a.organiseModel}

	materials, _ := json.Marshal(in.Materials)
	prompt := fmt.Sprintf("Course title: %s\nCourse description: %s\nCourse language: %s\n\n<materials>\n%s\n</materials>\n\nPlan the units.",
		in.CourseTitle, in.CourseDescription, in.Language, materials)
	var plan Outline
	u, err := a.call(ctx, a.organiseModel, planSystem,
		[]anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(prompt)}, nil, 64000, &plan)
	total.add(u)
	if err == nil {
		err = validPlan(plan)
	}
	if err != nil {
		return Outline{}, total, err
	}

	excerpts := excerptIndex(in)
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for i := range plan.Units {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			written, u, err := a.writeUnit(ctx, in, plan.Units[i], excerpts)
			mu.Lock()
			defer mu.Unlock()
			total.add(u)
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("unit %q: %w", plan.Units[i].Title, err)
				}
				return
			}
			plan.Units[i].Concepts = written
		}(i)
	}
	wg.Wait()
	if firstErr != nil {
		return Outline{}, total, firstErr
	}
	return plan, total, validOutline(plan)
}

// writeUnit writes the explanations of one planned unit.
func (a *claudeAI) writeUnit(ctx context.Context, in OrganiseInput, unit OutlineUnit, excerpts map[string]string) ([]OutlineConcept, Usage, error) {
	type item struct {
		Title    string   `json:"title"`
		Excerpts []string `json:"source_excerpts"`
	}
	items := make([]item, len(unit.Concepts))
	for i, c := range unit.Concepts {
		it := item{Title: c.Title}
		for _, ref := range c.Sources {
			if e, ok := excerpts[strings.ToUpper(strings.TrimSpace(ref))]; ok {
				it.Excerpts = append(it.Excerpts, e)
			}
		}
		items[i] = it
	}
	body, _ := json.Marshal(items)
	prompt := fmt.Sprintf("Course: %s\nCourse language: %s\nUnit: %s\nUnit intro: %s\n\n<concepts>\n%s\n</concepts>\n\nWrite the explanations for these %d concepts.",
		in.CourseTitle, in.Language, unit.Title, unit.Intro, body, len(items))

	var total Usage
	var lastErr error
	// One retry: a single bad answer should not fail the whole course
	for attempt := 0; attempt < 2; attempt++ {
		text, u, err := a.text(ctx, a.organiseModel, writeSystem,
			[]anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(prompt)}, 64000)
		total.add(u)
		if err != nil {
			lastErr = err
			continue
		}
		parts := splitConcepts(text, len(unit.Concepts))
		filled := 0
		written := make([]OutlineConcept, len(unit.Concepts))
		for i, c := range unit.Concepts {
			written[i] = c
			written[i].Explanation = parts[i]
			if strings.TrimSpace(parts[i]) != "" {
				filled++
			}
		}
		if filled == len(unit.Concepts) {
			return written, total, nil
		}
		lastErr = fmt.Errorf("the AI wrote %d of %d explanations", filled, len(unit.Concepts))
	}
	return nil, total, lastErr
}

var conceptMarker = regexp.MustCompile(`(?m)^#{2,4}\s*CONCEPT\s+(\d+)\s*$`)

// splitConcepts cuts "### CONCEPT n" sections into n explanations (by number).
func splitConcepts(text string, n int) []string {
	out := make([]string, n)
	locs := conceptMarker.FindAllStringSubmatchIndex(text, -1)
	for i, loc := range locs {
		num, _ := strconv.Atoi(text[loc[2]:loc[3]])
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		if num >= 1 && num <= n && out[num-1] == "" {
			out[num-1] = strings.TrimSpace(text[loc[1]:end])
		}
	}
	return out
}

// excerptIndex maps every reference key to the text the writer should see.
func excerptIndex(in OrganiseInput) map[string]string {
	idx := map[string]string{}
	for _, m := range in.Materials {
		idx[strings.ToUpper(m.Ref)] = fmt.Sprintf("[%s] %s: %s", m.Kind, m.Title, m.Summary)
		for _, c := range m.Concepts {
			idx[strings.ToUpper(c.Ref)] = fmt.Sprintf("[%s: %s] %s. %s", m.Kind, m.Title, c.Name, strings.TrimSpace(c.Summary+" "+c.Details))
		}
	}
	return idx
}

func (u *Usage) add(o Usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CostMicroUSD += o.CostMicroUSD
}

// validPlan rejects empty or skeleton plans.
func validPlan(o Outline) error {
	if len(o.Units) == 0 {
		return fmt.Errorf("the AI proposed no units")
	}
	n := 0
	for _, u := range o.Units {
		if strings.TrimSpace(u.Title) == "" || strings.Contains(u.Title, "placeholder") {
			return fmt.Errorf("the AI returned an incomplete course plan")
		}
		n += len(u.Concepts)
	}
	if n == 0 {
		return fmt.Errorf("the AI planned units without concepts")
	}
	return nil
}

// text sends one request and returns the answer's text.
func (a *claudeAI) text(ctx context.Context, model, system string, blocks []anthropic.BetaContentBlockParamUnion, maxTokens int64) (string, Usage, error) {
	var out string
	u, err := a.call(ctx, model, system, blocks, nil, maxTokens, &out)
	return out, u, err
}

// call sends one structured-output request and decodes the JSON into out.
func (a *claudeAI) call(ctx context.Context, model, system string, blocks []anthropic.BetaContentBlockParamUnion,
	schema map[string]any, maxTokens int64, out any) (Usage, error) {

	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTokens,
		System:    []anthropic.BetaTextBlockParam{{Text: system}},
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(blocks...)},
	}
	if schema != nil {
		params.OutputConfig.Format = anthropic.BetaJSONOutputFormatParam{Schema: schema}
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
	var kinds []string
	for _, b := range resp.Content {
		kinds = append(kinds, b.Type)
		if t, ok := b.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	raw := text.String()
	if len(raw) > 1500 {
		raw = raw[:1500]
	}
	slog.Debug("claude response", "model", resp.Model, "stop", resp.StopReason, "blocks", kinds, "text", raw)
	body := text.String()
	if sp, ok := out.(*string); ok {
		*sp = body
		return u, nil
	}
	if schema == nil {
		body = extractJSON(body)
	}
	if err := json.Unmarshal([]byte(body), out); err != nil {
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
	var searches int64
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
		searches += resp.Usage.ServerToolUse.WebSearchRequests
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
	u.CostMicroUSD += searches * webSearchMicroUSD

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
