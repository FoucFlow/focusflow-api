package learn

import (
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// Quiz import (F2.9): a FocusFlow CSV template, Moodle GIFT and Aiken. The
// import is previewed with an error per line before anything is saved.

const maxImportBytes = 1 << 20
const maxImportQuestions = 500

// ImportError points at the line of the file that could not be read.
type ImportError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// CSVTemplate is the header row of the FocusFlow template.
const CSVTemplate = "type,question,option_a,option_b,option_c,option_d,option_e,option_f,correct,answer_guide,concept\n" +
	"mcq,What does a base case do?,Stops the recursion,Calls the function again,Doubles the input,Prints the result,,,A,It answers the smallest problem directly.,Base case\n" +
	"multi,Which are recursive?,factorial(n) = n * factorial(n-1),x = x + 1,fib(n) = fib(n-1) + fib(n-2),,,,A;C,,Recursion\n" +
	"truefalse,Every recursive function needs a base case.,,,,,,,True,,Base case\n" +
	"short,In one sentence: what is recursion?,,,,,,,,A function that calls itself on a smaller problem.,Recursion\n" +
	"explain,Explain how factorial(3) is computed.,,,,,,,,\"- calls factorial(2) and factorial(1)\n- multiplies on the way back\",Call stack\n"

// parseCSVQuestions reads the template (header row required).
func parseCSVQuestions(text string) ([]Question, []ImportError) {
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	header, err := r.Read()
	if err != nil {
		return nil, []ImportError{{1, "The file is empty or not a CSV file."}}
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF")))] = i
	}
	for _, need := range []string{"type", "question"} {
		if _, ok := col[need]; !ok {
			return nil, []ImportError{{1, fmt.Sprintf("The header row needs a %q column. Download the template to see the columns.", need)}}
		}
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	var out []Question
	var errs []ImportError
	line := 1
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			errs = append(errs, ImportError{line, "This line could not be read (check the quotes)."})
			continue
		}
		if strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		q := Question{Kind: strings.ToLower(get(rec, "type")), Prompt: get(rec, "question"), AnswerGuide: get(rec, "answer_guide"),
			Concept: clip(get(rec, "concept"), 255), Source: "manual", Options: []string{}, Corrects: []int{}}
		if q.Kind == "true/false" || q.Kind == "tf" {
			q.Kind = QuestionTrueFalse
		}
		for _, l := range "abcdef" {
			if o := get(rec, "option_"+string(l)); o != "" {
				q.Options = append(q.Options, o)
			}
		}
		correct := get(rec, "correct")
		switch q.Kind {
		case QuestionMCQ, QuestionMulti:
			idx, msg := letterIndexes(correct, len(q.Options))
			if msg != "" {
				errs = append(errs, ImportError{line, msg})
				continue
			}
			if q.Kind == QuestionMCQ {
				if len(idx) != 1 {
					errs = append(errs, ImportError{line, "A multiple-choice question has exactly one correct letter (use type multi for several)."})
					continue
				}
				q.Correct = idx[0]
			} else {
				q.Corrects, q.Correct = idx, idx[0]
			}
		case QuestionTrueFalse:
			v, ok := truthValue(correct)
			if !ok {
				errs = append(errs, ImportError{line, "For true/false, correct must be True or False."})
				continue
			}
			q.Options, q.Correct = []string{"True", "False"}, map[bool]int{true: 0, false: 1}[v]
		}
		if msg := (questionIn{}).apply(&q); msg != "" {
			errs = append(errs, ImportError{line, msg})
			continue
		}
		out = append(out, q)
	}
	return out, errs
}

// letterIndexes reads "A", "a;c" or "B, D" into option indexes.
func letterIndexes(s string, n int) ([]int, string) {
	parts := strings.FieldsFunc(strings.ToUpper(s), func(r rune) bool { return r == ';' || r == ',' || r == ' ' || r == '|' })
	if len(parts) == 0 {
		return nil, "Give the correct option letter(s) in the correct column, e.g. A or A;C."
	}
	seen := map[int]bool{}
	var out []int
	for _, p := range parts {
		if len(p) != 1 || p[0] < 'A' || int(p[0]-'A') >= n {
			return nil, fmt.Sprintf("%q is not one of the option letters A to %c.", p, 'A'+n-1)
		}
		i := int(p[0] - 'A')
		if !seen[i] {
			seen[i] = true
			out = append(out, i)
		}
	}
	return out, ""
}

func truthValue(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "t", "yes", "vrai", "1":
		return true, true
	case "false", "f", "no", "faux", "0":
		return false, true
	}
	return false, false
}

// ── Aiken ────────────────────────────────────────────────────────────────

var aikenOption = regexp.MustCompile(`^([A-Za-z])[.)]\s+(.*)$`)
var aikenAnswer = regexp.MustCompile(`(?i)^ANSWER:\s*([A-Za-z])\s*$`)

// parseAiken reads multiple-choice questions: question, "A. option" lines,
// "ANSWER: B", blank line between questions.
func parseAiken(text string) ([]Question, []ImportError) {
	var out []Question
	var errs []ImportError
	var cur *Question
	start := 0
	var letters []string
	flushBad := func(line int, msg string) {
		errs = append(errs, ImportError{line, msg})
		cur, letters = nil, nil
	}
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		n := i + 1
		switch {
		case line == "":
			if cur != nil {
				flushBad(start, "This question has no ANSWER: line.")
			}
		case cur == nil:
			cur, start, letters = &Question{Kind: QuestionMCQ, Prompt: line, Options: []string{}, Corrects: []int{}, Source: "manual"}, n, nil
		case aikenAnswer.MatchString(line):
			l := strings.ToUpper(aikenAnswer.FindStringSubmatch(line)[1])
			idx := -1
			for j, x := range letters {
				if x == l {
					idx = j
				}
			}
			if idx < 0 {
				flushBad(n, "The answer letter is not one of the options.")
				continue
			}
			cur.Correct = idx
			cur.AnswerGuide = ""
			if msg := (questionIn{}).apply(cur); msg != "" {
				flushBad(start, msg)
				continue
			}
			out = append(out, *cur)
			cur, letters = nil, nil
		case aikenOption.MatchString(line):
			m := aikenOption.FindStringSubmatch(line)
			letters = append(letters, strings.ToUpper(m[1]))
			cur.Options = append(cur.Options, strings.TrimSpace(m[2]))
		case len(cur.Options) == 0:
			cur.Prompt += "\n" + line // questions may span lines
		default:
			flushBad(n, "Expected an option (A. …) or the ANSWER: line.")
		}
	}
	if cur != nil {
		errs = append(errs, ImportError{start, "This question has no ANSWER: line."})
	}
	return out, errs
}

// ── GIFT ─────────────────────────────────────────────────────────────────

var giftComment = regexp.MustCompile(`(?m)^\s*//.*$`)
var giftTitle = regexp.MustCompile(`^::(.*?)::`)
var giftWeight = regexp.MustCompile(`^%(-?\d+(?:\.\d+)?)%`)

// parseGIFT reads the common GIFT question types: multiple choice
// ({=right ~wrong}), multiple select (~%50%right ~%-100%wrong), true/false
// ({T} {F}), short answer ({=answer}) and essay ({}).
func parseGIFT(text string) ([]Question, []ImportError) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var out []Question
	var errs []ImportError
	// Questions are separated by blank lines; track line numbers per block.
	lines := strings.Split(text, "\n")
	var block []string
	blockStart := 0
	flush := func() {
		if len(block) == 0 {
			return
		}
		src := giftComment.ReplaceAllString(strings.Join(block, "\n"), "")
		block = nil
		if strings.TrimSpace(src) == "" || strings.HasPrefix(strings.TrimSpace(src), "$CATEGORY") {
			return
		}
		q, err := giftQuestion(src)
		if err != "" {
			errs = append(errs, ImportError{blockStart, err})
			return
		}
		if msg := (questionIn{}).apply(&q); msg != "" {
			errs = append(errs, ImportError{blockStart, msg})
			return
		}
		out = append(out, q)
	}
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			flush()
			continue
		}
		if len(block) == 0 {
			blockStart = i + 1
		}
		block = append(block, l)
	}
	flush()
	return out, errs
}

func giftUnescape(s string) string {
	r := strings.NewReplacer(`\=`, "=", `\~`, "~", `\{`, "{", `\}`, "}", `\#`, "#", `\:`, ":", `\n`, "\n")
	return strings.TrimSpace(r.Replace(s))
}

// giftSplit finds the unescaped answer block.
func giftSplit(src string) (prompt, answers string, ok bool) {
	open, close := -1, -1
	for i := 0; i < len(src); i++ {
		if src[i] == '\\' {
			i++
			continue
		}
		if src[i] == '{' && open < 0 {
			open = i
		} else if src[i] == '}' && open >= 0 {
			close = i
			break
		}
	}
	if open < 0 || close < 0 {
		return "", "", false
	}
	return src[:open] + src[close+1:], src[open+1 : close], true
}

func giftQuestion(src string) (Question, string) {
	src = strings.TrimSpace(src)
	src = giftTitle.ReplaceAllString(src, "")
	prompt, answers, ok := giftSplit(src)
	if !ok {
		return Question{}, "No answer block { … } found."
	}
	prompt = giftUnescape(strings.TrimPrefix(strings.TrimSpace(prompt), "[markdown]"))
	q := Question{Prompt: prompt, Options: []string{}, Corrects: []int{}, Source: "manual"}
	a := strings.TrimSpace(answers)
	// General feedback "####" becomes the answer guide
	if i := strings.Index(a, "####"); i >= 0 {
		q.AnswerGuide = giftUnescape(a[i+4:])
		a = strings.TrimSpace(a[:i])
	}
	switch strings.ToUpper(a) {
	case "":
		q.Kind = QuestionExplain
		if q.AnswerGuide == "" {
			return q, "An essay question needs a guide for marking: add it after #### inside the braces."
		}
		return q, ""
	case "T", "TRUE", "F", "FALSE":
		q.Kind, q.Options = QuestionTrueFalse, []string{"True", "False"}
		if strings.HasPrefix(strings.ToUpper(a), "F") {
			q.Correct = 1
		}
		return q, ""
	}
	// Tokens start with = or ~ (unescaped)
	type tok struct {
		mark byte
		text string
	}
	var toks []tok
	cur := -1
	for i := 0; i < len(a); i++ {
		if a[i] == '\\' && i+1 < len(a) {
			if cur >= 0 {
				toks[cur].text += a[i : i+2]
			}
			i++
			continue
		}
		if a[i] == '=' || a[i] == '~' {
			toks = append(toks, tok{mark: a[i]})
			cur = len(toks) - 1
			continue
		}
		if cur >= 0 {
			toks[cur].text += string(a[i])
		}
	}
	if len(toks) == 0 {
		return q, "The answer block has no answers (= right, ~ wrong)."
	}
	allRight := true
	weighted := false
	for _, t := range toks {
		if t.mark != '=' {
			allRight = false
		}
	}
	for i, t := range toks {
		text := t.text
		if j := strings.Index(text, "#"); j >= 0 && (j == 0 || text[j-1] != '\\') {
			text = text[:j] // per-answer feedback is dropped
		}
		w := 0.0
		if m := giftWeight.FindStringSubmatch(strings.TrimSpace(text)); m != nil {
			weighted = true
			w, _ = strconv.ParseFloat(m[1], 64)
			text = giftWeight.ReplaceAllString(strings.TrimSpace(text), "")
		}
		text = giftUnescape(text)
		q.Options = append(q.Options, text)
		if (t.mark == '=' && !weighted) || w > 0 {
			q.Corrects = append(q.Corrects, i)
		}
	}
	switch {
	case allRight:
		// Short answer: every listed answer is accepted; the AI marks against them
		q.Kind = QuestionShort
		q.AnswerGuide = strings.TrimSpace("Accepted answers: " + strings.Join(q.Options, "; ") + "\n" + q.AnswerGuide)
		q.Options, q.Corrects = []string{}, []int{}
	case len(q.Corrects) > 1:
		q.Kind, q.Correct = QuestionMulti, q.Corrects[0]
	case len(q.Corrects) == 1:
		q.Kind, q.Correct, q.Corrects = QuestionMCQ, q.Corrects[0], []int{}
	default:
		return q, "No right answer is marked (=, or a positive %weight%)."
	}
	return q, ""
}

// POST /api/learn/units/:uid/questions/import {format: csv|gift|aiken, text, save}
// Without save it only previews. With save, nothing is saved if any line fails.
func (h *Handler) ImportQuestions(c *gin.Context) {
	u, _, ok := h.ownedUnit(c)
	if !ok {
		return
	}
	var in struct {
		Format string `json:"format"`
		Text   string `json:"text"`
		Save   bool   `json:"save"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "The request body must be valid JSON.")
		return
	}
	if len(in.Text) > maxImportBytes {
		fail(c, http.StatusUnprocessableEntity, "The file is too large (1 MB at most).")
		return
	}
	var qs []Question
	var errs []ImportError
	switch strings.ToLower(in.Format) {
	case "csv":
		qs, errs = parseCSVQuestions(in.Text)
	case "gift":
		qs, errs = parseGIFT(in.Text)
	case "aiken":
		qs, errs = parseAiken(in.Text)
	default:
		fail(c, http.StatusUnprocessableEntity, "format must be csv, gift or aiken.")
		return
	}
	if len(qs) > maxImportQuestions {
		errs = append(errs, ImportError{0, fmt.Sprintf("At most %d questions per import.", maxImportQuestions)})
	}
	if errs == nil {
		errs = []ImportError{}
	}
	if qs == nil {
		qs = []Question{}
	}
	if !in.Save || len(errs) > 0 || len(qs) == 0 {
		status := http.StatusOK
		if in.Save {
			status = http.StatusUnprocessableEntity
		}
		c.JSON(status, gin.H{"data": gin.H{"questions": qs, "errors": errs, "saved": 0}})
		return
	}
	for i := range qs {
		qs[i].UnitID, qs[i].CourseID = u.ID, u.CourseID
	}
	if err := h.s.db.Create(&qs).Error; err != nil {
		fail(c, http.StatusInternalServerError, "Could not save the questions.")
		return
	}
	h.s.syncOptional(u.ID)
	c.JSON(http.StatusCreated, gin.H{"data": gin.H{"questions": qs, "errors": errs, "saved": len(qs)}})
}
