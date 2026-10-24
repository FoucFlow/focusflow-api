package learn

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
}

// NewClaudeAI returns the Claude provider, or a disabled one without a key.
func NewClaudeAI(apiKey, digestModel, organiseModel string) AI {
	if apiKey == "" {
		return disabledAI{}
	}
	if digestModel == "" {
		digestModel = "claude-haiku-5-5"
	}
	if organiseModel == "" {
		organiseModel = "claude-opus-5-5"
	}
	return &claudeAI{
		// The SDK retries 408/409/429/5xx and connection errors with backoff
		client:        anthropic.NewClient(option.WithAPIKey(apiKey), option.WithMaxRetries(4)),
		digestModel:   digestModel,
		organiseModel: organiseModel,
	}
}

const digestSystem = `You read one study material (lecture notes, slides, or a video or web page description) for a course builder.
Return a faithful digest of what the material actually covers: a short summary, the main topics, and the key points a learner should take away.
Use only what is in the material. Do not invent content. Write in the material's own language.`

const organiseSystem = `You organise a creator's study materials into a course made of units, for self-taught learners and students.
Rules:
- Group materials into 3 to 10 units in a sensible learning order, from foundations to advanced topics.
- Every unit gets a clear title and a unit text in Markdown (300 to 700 words) that explains the unit's ideas, written from the materials' digests. Do not invent facts beyond them; where the materials are thin, keep the text short.
- List the material IDs that belong to each unit, in the order a learner should use them. Use only the IDs given. Every material should appear in at least one unit.
- Write in the course language.`

func digestSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary":    map[string]any{"type": "string"},
			"topics":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"key_points": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required":             []string{"summary", "topics", "key_points"},
		"additionalProperties": false,
	}
}

func outlineSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"units": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title":        map[string]any{"type": "string"},
						"body":         map[string]any{"type": "string"},
						"material_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
					},
					"required":             []string{"title", "body", "material_ids"},
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
	u, err := a.call(ctx, a.digestModel, digestSystem, blocks, digestSchema(), 4000, &d)
	return d, u, err
}

func (a *claudeAI) Organise(ctx context.Context, in OrganiseInput) (Outline, Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()

	materials, _ := json.Marshal(in.Materials)
	prompt := fmt.Sprintf("Course title: %s\nCourse description: %s\nCourse language: %s\n\n<materials>\n%s\n</materials>\n\nOrganise these materials into units.",
		in.CourseTitle, in.CourseDescription, in.Language, materials)

	var o Outline
	u, err := a.call(ctx, a.organiseModel, organiseSystem,
		[]anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(prompt)}, outlineSchema(), 32000, &o)
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

	resp, err := a.client.Beta.Messages.New(ctx, params)
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
