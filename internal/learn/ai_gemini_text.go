package learn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"
)

// geminiAI does the whole course-builder process with Gemini, using the same
// instructions as the Claude version: read documents, plan units, write each
// unit, and find references (Google Search). AI_PROVIDER=gemini.
type geminiAI struct {
	client        *genai.Client
	digestModel   string // reading PDFs and slides
	organiseModel string // planning units and writing lessons
	enrichModel   string // references with Google Search
	questionModel string // writing test questions
	markModel     string // marking written answers
	http          *http.Client
}

func init() {
	geminiPrices["gemini-2.5-pro"] = [2]float64{1.25, 10}
	geminiPrices["gemini-3.1-pro-preview"] = [2]float64{2, 12}
}

// inlineLimit: larger PDFs go through the Files API (requests cap at ~20 MB).
const inlineLimit = 14 << 20

// NewGeminiAI returns the Gemini provider (nil without a key).
func NewGeminiAI(ctx context.Context, apiKey, digestModel, organiseModel, enrichModel string) (*geminiAI, error) {
	if apiKey == "" {
		return nil, nil
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: apiKey, Backend: genai.BackendGeminiAPI})
	if err != nil {
		return nil, fmt.Errorf("gemini client: %w", err)
	}
	if digestModel == "" {
		digestModel = "gemini-2.5-flash"
	}
	if organiseModel == "" {
		organiseModel = "gemini-3.1-pro-preview" // 2.5-pro is closed to new API users
	}
	if enrichModel == "" {
		enrichModel = "gemini-2.5-flash"
	}
	return &geminiAI{
		client: client, digestModel: digestModel, organiseModel: organiseModel, enrichModel: enrichModel,
		questionModel: "gemini-3.1-pro-preview", markModel: "gemini-2.5-flash",
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: publicOnlyTransport(),
			// Follow redirects ourselves (grounding links are Google redirects)
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// SetExamModels overrides the question-writing and marking models.
func (g *geminiAI) SetExamModels(question, mark string) {
	if question != "" {
		g.questionModel = question
	}
	if mark != "" {
		g.markModel = mark
	}
}

// generate calls Gemini with backoff on 429/5xx and returns the text and usage.
func (g *geminiAI) generate(ctx context.Context, model, system string, parts []*genai.Part, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, string, Usage, error) {
	u := Usage{Provider: "gemini", Model: model}
	if cfg == nil {
		cfg = &genai.GenerateContentConfig{}
	}
	cfg.SystemInstruction = genai.NewContentFromText(system, genai.RoleUser)
	if cfg.MaxOutputTokens == 0 {
		cfg.MaxOutputTokens = 65536
	}
	var resp *genai.GenerateContentResponse
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		resp, err = g.client.Models.GenerateContent(ctx, model, []*genai.Content{genai.NewContentFromParts(parts, genai.RoleUser)}, cfg)
		if err != nil && noQuota(err) && model != g.digestModel {
			// The key's plan has no quota at all for this model (the free tier
			// allows no Pro models): use the reading model (Flash) instead
			slog.Warn("gemini model not available on this plan, using fallback", "model", model, "fallback", g.digestModel)
			model = g.digestModel
			u.Model = model
			resp, err = g.client.Models.GenerateContent(ctx, model, []*genai.Content{genai.NewContentFromParts(parts, genai.RoleUser)}, cfg)
		}
		if err == nil || !retryableGemini(err) || ctx.Err() != nil {
			break
		}
		select {
		case <-time.After(time.Duration(5*(attempt+1)*(attempt+1)) * time.Second):
		case <-ctx.Done():
		}
	}
	if err != nil {
		return nil, "", u, fmt.Errorf("gemini: %w", err)
	}
	if m := resp.UsageMetadata; m != nil {
		u.InputTokens = int64(m.PromptTokenCount)
		u.OutputTokens = int64(m.CandidatesTokenCount + m.ThoughtsTokenCount)
		if p, ok := geminiPrices[model]; ok {
			u.CostMicroUSD = int64(float64(u.InputTokens)*p[0] + float64(u.OutputTokens)*p[1])
		}
	}
	if len(resp.Candidates) > 0 && resp.Candidates[0].FinishReason == genai.FinishReasonMaxTokens {
		return resp, "", u, errors.New("the AI response was cut off (too long); try fewer or smaller materials")
	}
	return resp, resp.Text(), u, nil
}

// ── reading ──────────────────────────────────────────────────────────────

func (g *geminiAI) Digest(ctx context.Context, in DigestInput) (Digest, Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()

	var parts []*genai.Part
	if in.Kind == KindPDF && len(in.PDF) > 0 {
		if len(in.PDF) <= inlineLimit {
			parts = append(parts, genai.NewPartFromBytes(in.PDF, "application/pdf"))
		} else {
			f, err := g.client.Files.Upload(ctx, bytes.NewReader(in.PDF), &genai.UploadFileConfig{MIMEType: "application/pdf", DisplayName: "material.pdf"})
			if err != nil {
				return Digest{}, Usage{Provider: "gemini", Model: g.digestModel}, fmt.Errorf("gemini file upload: %w", err)
			}
			defer g.client.Files.Delete(context.Background(), f.Name, nil)
			parts = append(parts, genai.NewPartFromURI(f.URI, "application/pdf"))
		}
		parts = append(parts, genai.NewPartFromText(pdfPrompt(in)))
	} else {
		parts = append(parts, genai.NewPartFromText(fmt.Sprintf(
			"Material type: %s\nTitle: %s\n\n<material>\n%s\n</material>\n\nDigest this material.", in.Kind, in.Title, in.Text)))
	}
	cfg := &genai.GenerateContentConfig{ResponseMIMEType: "application/json", Temperature: genai.Ptr[float32](0.2)}
	_, text, u, err := g.generate(ctx, g.digestModel, digestSystem+digestFormat, parts, cfg)
	if err != nil {
		return Digest{}, u, err
	}
	var d Digest
	if err := json.Unmarshal([]byte(extractJSON(text)), &d); err != nil {
		return Digest{}, u, fmt.Errorf("the AI returned an unreadable answer: %w", err)
	}
	return d, u, validDigest(d)
}

// ── planning and writing ─────────────────────────────────────────────────

func (g *geminiAI) Organise(ctx context.Context, in OrganiseInput) (Outline, Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	total := Usage{Provider: "gemini", Model: g.organiseModel}

	prompt := planPrompt(in)
	cfg := &genai.GenerateContentConfig{ResponseMIMEType: "application/json", Temperature: genai.Ptr[float32](0.3)}
	_, text, u, err := g.generate(ctx, g.organiseModel, planSystem, []*genai.Part{genai.NewPartFromText(prompt)}, cfg)
	total.add(u)
	if err != nil {
		return Outline{}, total, err
	}
	var plan Outline
	if err := json.Unmarshal([]byte(extractJSON(text)), &plan); err != nil {
		return Outline{}, total, fmt.Errorf("the AI returned an unreadable course plan: %w", err)
	}
	if err := validPlan(plan); err != nil {
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
			written, u, err := g.writeUnit(ctx, in, plan.Units[i], excerpts)
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

func (g *geminiAI) writeUnit(ctx context.Context, in OrganiseInput, unit OutlineUnit, excerpts map[string]string) ([]OutlineConcept, Usage, error) {
	prompt := unitWritePrompt(in, unit, excerpts)
	var total Usage
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		_, text, u, err := g.generate(ctx, g.organiseModel, writeSystem, []*genai.Part{genai.NewPartFromText(prompt)},
			&genai.GenerateContentConfig{Temperature: genai.Ptr[float32](0.4)})
		total.add(u)
		if err != nil {
			lastErr = err
			continue
		}
		if written, ok := fillExplanations(unit, splitConcepts(text, len(unit.Concepts))); ok {
			return written, total, nil
		}
		lastErr = errors.New("the AI did not write every explanation")
	}
	return nil, total, lastErr
}

// ── references (Google Search) ───────────────────────────────────────────

func (g *geminiAI) FindReferences(ctx context.Context, in EnrichInput) (map[int][]Reference, Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()

	concepts, _ := json.Marshal(in.Concepts)
	prompt := fmt.Sprintf("Course: %s\nCourse language: %s\nUnit: %s\n\n<concepts>\n%s\n</concepts>\n\nFind references for each concept.",
		in.CourseTitle, in.Language, in.UnitTitle, concepts)
	// Google Search grounding cannot be combined with JSON mode: the JSON is in the text
	cfg := &genai.GenerateContentConfig{Tools: []*genai.Tool{{GoogleSearch: &genai.GoogleSearch{}}}, Temperature: genai.Ptr[float32](0.2)}
	resp, text, u, err := g.generate(ctx, g.enrichModel, enrichSystem, []*genai.Part{genai.NewPartFromText(prompt)}, cfg)
	if err != nil {
		return nil, u, err
	}

	// Sites that actually appeared in this call's Google Search results
	hosts := map[string]bool{}
	for _, c := range resp.Candidates {
		if c.GroundingMetadata == nil {
			continue
		}
		for _, ch := range c.GroundingMetadata.GroundingChunks {
			if ch.Web == nil {
				continue
			}
			if real := g.resolve(ctx, ch.Web.URI); real != "" {
				hosts[siteOf(real)] = true
			}
			if ch.Web.Domain != "" {
				hosts[strings.TrimPrefix(strings.ToLower(ch.Web.Domain), "www.")] = true
			}
			if ch.Web.Title != "" && strings.Contains(ch.Web.Title, ".") && !strings.Contains(ch.Web.Title, " ") {
				hosts[strings.TrimPrefix(strings.ToLower(ch.Web.Title), "www.")] = true // titles are often the domain
			}
		}
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
	if err := json.Unmarshal([]byte(extractJSON(text)), &out); err != nil {
		return nil, u, fmt.Errorf("the AI returned unreadable references: %w", err)
	}

	// Keep a link only if its site came from the search AND the page loads
	refs := map[int][]Reference{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for _, c := range out.Concepts {
		for _, r := range c.References {
			if !hosts[siteOf(r.URL)] {
				continue
			}
			wg.Add(1)
			go func(index int, title, link, why string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if !g.pageLoads(ctx, link) {
					return
				}
				mu.Lock()
				defer mu.Unlock()
				if len(refs[index]) < 3 {
					refs[index] = append(refs[index], Reference{Title: clip(title, 255), URL: link, Site: siteOf(link), Why: clip(why, 300)})
				}
			}(c.Index, r.Title, r.URL, r.Why)
		}
	}
	wg.Wait()
	return refs, u, nil
}

// resolve follows up to 5 redirects and returns the final URL ("" on failure).
func (g *geminiAI) resolve(ctx context.Context, link string) string {
	for i := 0; i < 5; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
		if err != nil {
			return ""
		}
		resp, err := g.http.Do(req)
		if err != nil {
			return ""
		}
		resp.Body.Close()
		if resp.StatusCode < 300 || resp.StatusCode >= 400 {
			return link
		}
		next, err := resp.Location()
		if err != nil {
			return ""
		}
		link = next.String()
	}
	return link
}

// pageLoads checks the link answers 2xx after redirects (public http(s) only).
func (g *geminiAI) pageLoads(ctx context.Context, link string) bool {
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return false
	}
	final := g.resolve(ctx, link)
	if final == "" {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, final, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; FocusFlowLinkCheck/1.0)")
	resp, err := g.http.Do(req)
	if err != nil {
		slog.Debug("reference check failed", "url", link, "error", err)
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// publicOnlyTransport refuses connections to loopback, private, link-local
// and other non-public addresses, so checking AI-suggested links can never
// reach the server itself or the internal network (checked at dial time,
// after DNS, so redirects and DNS tricks are covered too).
func publicOnlyTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if !isPublicIP(ip.IP) {
				return nil, fmt.Errorf("refusing non-public address %s", ip.IP)
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
	return t
}

func isPublicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.Equal(net.ParseIP("169.254.169.254")))
}
