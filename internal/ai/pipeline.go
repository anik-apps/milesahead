package ai

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/kumaranik/milesahead/internal/editornote"
	"github.com/kumaranik/milesahead/internal/metrics"
)

// KeyDetail represents a single key detail extracted from structured output.
type KeyDetail struct {
	Label string
	Value string
}

// ArticleLink represents a link extracted from structured output.
type ArticleLink struct {
	Text string
	URL  string
}

// RelatedCard represents a card linked from an article.
type RelatedCard struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

// PipelineResult holds the output of a full generate-then-verify cycle.
type PipelineResult struct {
	Content          string
	Title            string
	TLDR             string
	KeyDetails       []KeyDetail
	Links            []ArticleLink
	Type             string
	Verification     *VerificationResult
	PassedThreshold  bool
	ImageURL         string
	ImageThumbnail   string
	ImageAttribution string
	ScoutsTake       string
	RelatedCards     []RelatedCard
	Tags             []string
	// EditorNotes are remarks the model addressed to the admin as
	// <div class="editor-note">, taken out of Content (see package
	// editornote). One per line; never published.
	EditorNotes string
}

// Pipeline orchestrates the generator and two critics to produce verified articles.
// Critic1 checks factual accuracy and source compliance.
// Critic2 checks logical consistency and common sense.
type Pipeline struct {
	generator *Generator
	critic    *Critic
	critic2   *Critic
	threshold int
	// detailModels is the chain for the inline key-details call; empty means
	// the critic chain. See SetDetailModels.
	detailModels []string
	// detailClient carries the inline key-details call; nil uses the
	// critic's client. See SetDetailClient.
	detailClient HFClientInterface
	// outlets lists the news sites and blogs articles are written from (the
	// sources table), read once per article. See SetOutlets.
	outlets func() []Outlet
}

// NewPipeline creates a new Pipeline with the given quality threshold (0-100).
// critic2 can be nil for single-critic mode (backwards compatible).
func NewPipeline(generator *Generator, critic *Critic, threshold int) *Pipeline {
	return &Pipeline{generator: generator, critic: critic, threshold: threshold}
}

// SetCritic2 adds a second critic for reasoning/consistency checks.
func (p *Pipeline) SetCritic2(critic2 *Critic) {
	p.critic2 = critic2
}

// SetDetailModels sets the model chain for the key-details call Process makes
// when the article carries no structured key details.
//
// That call used to go to the critic model. It asks for 1024 tokens, so it is
// bounded by ShortCallTimeout (30s), and a critic whose replies take minutes
// times out on every attempt -- each article then waited out four 30s
// attempts before continuing without details. Production passes the fast
// enrichment chain.
func (p *Pipeline) SetDetailModels(models []string) {
	p.detailModels = models
}

// SetDetailClient sets the client the inline key-details call goes through.
// Unset, it is the critic's client, as before. Production passes a copy of the
// critic client labelled for the enrichment step: the call runs on
// enrichment_model, so model health must count it against that list, not the
// critic's.
func (p *Pipeline) SetDetailClient(c HFClientInterface) { p.detailClient = c }

// SetOutlets sets where the pipeline reads the outlets articles must not name
// or link to: production passes every row of the sources table. The article's
// own sources and the built-in outlets are checked whether or not it is set.
func (p *Pipeline) SetOutlets(fn func() []Outlet) {
	p.outlets = fn
}

// outletMatcher builds the outlet check for one article.
func (p *Pipeline) outletMatcher(sources []SourceArticle) *OutletMatcher {
	var table []Outlet
	if p.outlets != nil {
		table = p.outlets()
	}
	return NewOutletMatcher(OutletsFor(sources, table))
}

// ErrNotEnoughSources is returned when a cluster has fewer than 2 unique sources.
var ErrNotEnoughSources = fmt.Errorf("need at least 2 different sources to generate an article")

// Process generates an article, verifies it, and if the critic left anything
// to fix (see needsRevision), auto-revises once before finalizing.
// Requires at least 2 sources from different sites to reduce hallucination risk.
func (p *Pipeline) Process(articleType string, sources []SourceArticle) (*PipelineResult, error) {
	// Require multiple sources from different sites
	uniqueSources := make(map[string]bool)
	for _, s := range sources {
		uniqueSources[s.Name] = true
	}
	if len(uniqueSources) < 2 {
		return nil, ErrNotEnoughSources
	}

	// Auto-detect article type if not specified
	if articleType == "" || articleType == "auto" {
		articleType = detectArticleType(sources)
	}

	slog.Info("article generated", "type", articleType, "sources", len(sources))
	outlets := p.outletMatcher(sources)

	raw, err := p.generator.Generate(articleType, sources)
	if err != nil {
		return nil, err
	}

	// Notes come out first, so neither the key details, the critic nor the
	// auto-revision ever see them.
	raw, notes := takeEditorNotes(raw, sources)
	keyDetails, links := replyDetails(raw, sources)
	content := cleanMarkdown(raw)
	content, unresolved := cleanSourceRefs("generation", content, sources)
	content, mentions := checkOutlets("generation", content, outlets)

	// Critic reviews the article
	verification, err := p.critic.Verify(content, sources)
	if err != nil {
		return nil, err
	}
	flagSourceRefs(verification, unresolved, sources)
	flagOutletMentions(verification, mentions)
	verification.Score = adjustScore(verification)
	metrics.CriticScore.Observe(float64(verification.Score))

	slog.Info("critic scored", "model", verification.Model, "score", verification.Score,
		"passed", verification.Score >= p.threshold, "issues", len(verification.Issues))

	// If there is something to fix, generator revises, then critic reviews again
	if needsRevision(verification) {
		slog.Info("auto-revision triggered", "issues_count", len(verification.Issues))
		issuesStr := strings.Join(verification.Issues, "\n- ")
		revised, err := p.generator.GenerateWithFeedback(sources, content,
			"Fix ALL of the following issues. Do not add any information not in the original sources. Only state facts that are directly in the sources.",
			issuesStr)
		if err == nil && revised != "" {
			revised, revisedNotes := takeEditorNotes(revised, sources)
			revisedDetails, revisedLinks := replyDetails(revised, sources)
			revised = cleanMarkdown(revised)
			revised, revisedUnresolved := cleanSourceRefs("auto-revision", revised, sources)
			revised, revisedMentions := checkOutlets("auto-revision", revised, outlets)
			v2, err := p.critic.Verify(revised, sources)
			if err == nil {
				flagSourceRefs(v2, revisedUnresolved, sources)
				// One revision only: a mention that survives it stays on the
				// verdict for the admin, who can judge the exceptions.
				flagOutletMentions(v2, revisedMentions)
				v2.Score = adjustScore(v2)
				slog.Info("critic scored revision", "model", v2.Model, "score", v2.Score,
					"previous", verification.Score)
				if v2.Score >= verification.Score {
					content = revised
					verification = v2
					keyDetails, links = revisedDetails, revisedLinks
					notes = revisedNotes
				}
			}
		}
	}

	content, parsedTitle, tldr := p.finalizeContent(content)

	// Only a reply without usable key details costs a separate call.
	if len(keyDetails) == 0 {
		slog.Info("no key details in the generated article, extracting them separately")
		kd, lk := p.extractDetails(parsedTitle, content)
		kd = cleanDetailSourceRefs(kd, sources)
		kd = dropOutletDetails(kd, outlets, parsedTitle)
		if len(kd) > 0 {
			keyDetails = kd
		}
		if len(lk) > 0 && len(links) == 0 {
			links = lk
		}
	}

	return &PipelineResult{
		Content:         content,
		Title:           parsedTitle,
		TLDR:            tldr,
		KeyDetails:      keyDetails,
		Links:           outlets.DropOutletLinks(links),
		Type:            articleType,
		Verification:    verification,
		PassedThreshold: verification.Score >= p.threshold,
		EditorNotes:     notes,
	}, nil
}

// needsRevision reports whether a verdict leaves anything for the
// auto-revision to fix: an issue that survived dropPassedChecks, which
// includes the source-reference and news-site issues flagSourceRefs and
// flagOutletMentions add.
//
// The score and the checks are deliberately not part of the decision. In
// production (2026-09-21 to 10-01) verdicts with all four checks passing and
// scores of 85-95 still carried real errors -- a miscalculated per-point
// price, a wrong joint-venture member, a "202026" typo -- so gating on them
// would publish fixable mistakes. What wasted revisions was issues that were
// not problems at all, which the critic prompt and dropPassedChecks remove.
func needsRevision(v *VerificationResult) bool {
	return len(v.Issues) > 0
}

// extractDetails makes a separate AI call to extract key details and links from the article.
func (p *Pipeline) extractDetails(title, content string) ([]KeyDetail, []ArticleLink) {
	prompt := ExtractDetailsPrompt(title, content)
	models := p.detailModels
	if len(models) == 0 {
		models = p.critic.models
	}
	client := p.detailClient
	if client == nil {
		client = p.critic.client
	}
	resp, err := TryModels(client, models, prompt, 1024)
	if err != nil {
		slog.Warn("extract details failed", "error", err)
		return nil, nil
	}

	// Parse JSON
	resp = strings.TrimSpace(resp)
	if idx := strings.Index(resp, "{"); idx != -1 {
		resp = resp[idx:]
	}
	if idx := strings.LastIndex(resp, "}"); idx != -1 {
		resp = resp[:idx+1]
	}

	var result struct {
		KeyDetails []KeyDetail   `json:"key_details"`
		Links      []ArticleLink `json:"links"`
	}
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		slog.Warn("extract details parse failed", "error", err)
		return nil, nil
	}

	slog.Info("extracted details", "key_details", len(result.KeyDetails), "links", len(result.Links))
	return result.KeyDetails, result.Links
}

// ProcessWithFeedback revises a previously generated article using feedback,
// then verifies the revision.
func (p *Pipeline) ProcessWithFeedback(sources []SourceArticle, previousOutput, feedback, criticIssues string) (*PipelineResult, error) {
	content, err := p.generator.GenerateWithFeedback(sources, previousOutput, feedback, criticIssues)
	if err != nil {
		return nil, err
	}

	return p.buildResult(content, "", sources)
}

func (p *Pipeline) buildResult(raw, articleType string, sources []SourceArticle) (*PipelineResult, error) {
	raw, notes := takeEditorNotes(raw, sources)
	keyDetails, links := replyDetails(raw, sources)
	content := cleanMarkdown(raw)
	content, unresolved := cleanSourceRefs("revision", content, sources)
	outlets := p.outletMatcher(sources)
	content, mentions := checkOutlets("revision", content, outlets)

	verification, err := p.critic.Verify(content, sources)
	if err != nil {
		return nil, err
	}
	flagSourceRefs(verification, unresolved, sources)
	flagOutletMentions(verification, mentions)
	verification.Score = adjustScore(verification)
	metrics.CriticScore.Observe(float64(verification.Score))
	slog.Info("critic scored", "model", verification.Model, "score", verification.Score,
		"passed", verification.Score >= p.threshold, "issues", len(verification.Issues))

	content, parsedTitle, tldr := p.finalizeContent(content)
	// A rewrite's key details are stored with it (db.ApplyRevision), as
	// public text with no pass after this one, so they get the outlet rule
	// enrichment applies to extracted details.
	keyDetails = dropOutletDetails(keyDetails, outlets, parsedTitle)

	return &PipelineResult{
		Content:         content,
		Title:           parsedTitle,
		TLDR:            tldr,
		KeyDetails:      keyDetails,
		Links:           outlets.DropOutletLinks(links),
		Type:            articleType,
		Verification:    verification,
		PassedThreshold: verification.Score >= p.threshold,
		EditorNotes:     notes,
	}, nil
}

// takeEditorNotes splits the admin-only editor notes out of a raw generator
// reply before anything grades or parses it, so the key details, the critic,
// the auto-revision and the structured parsing see only the article. Notes are
// never published, so a numbered source reference in them is not flagged; it
// is spelled out as the outlet's name, which tells the admin which source the
// note is about.
func takeEditorNotes(content string, sources []SourceArticle) (clean, notes string) {
	clean, notes = editornote.Split(content)
	if notes != "" {
		notes = nameSourcesInNotes(notes, sources)
	}
	return clean, notes
}

// nameSourcesInNotes replaces "Source N" in admin-only editor notes with the
// name of the Nth source. Never used on published text: articles must not name
// the outlets they were written from.
func nameSourcesInNotes(notes string, sources []SourceArticle) string {
	return replaceEach(numberedSourceRE, notes, func(g []string, before, after string) string {
		n, err := strconv.Atoi(g[1])
		if err != nil || n < 1 || n > len(sources) || notASourceRef(before, after) {
			return g[0]
		}
		name := strings.TrimSpace(sources[n-1].Name)
		if name == "" {
			return g[0]
		}
		return name
	})
}

// cleanSourceRefs removes citation markers ("[1]") from a raw generator reply
// and returns the numbered source references left in it ("Source 1 notes...")
// for flagSourceRefs. It runs on the whole reply, so the title, TLDR and
// structured key details are covered along with the body.
//
// A reference is not rewritten to the outlet it stood for: articles must not
// name the sites they were written from. It is flagged instead -- the
// attribution check fails, the issue feeds the auto-revision that Process
// already performs, and an article that still carries one is scored down
// rather than published looking clean.
func cleanSourceRefs(stage, content string, _ []SourceArticle) (string, []string) {
	out, removed := StripCitationMarkers(content)
	if len(removed) > 0 {
		slog.Warn("removed citation markers", "stage", stage, "markers", removed)
	}
	refs := FindSourceRefs(out)
	if len(refs) > 0 {
		slog.Warn("numbered source references left for revision", "stage", stage, "refs", refs)
	}
	return out, refs
}

// flagSourceRefs records numbered references on a critic verdict, before
// adjustScore, so they cost the score the same way a failed attribution check
// reported by the critic does.
func flagSourceRefs(v *VerificationResult, refs []string, _ []SourceArticle) {
	if len(refs) == 0 {
		return
	}
	v.AttributionCheck = false
	v.Issues = append(v.Issues, SourceRefIssue(refs))
}

// replyDetails parses the KEY_DETAILS and LINKS sections out of a raw
// generator reply.
//
// It has to see the reply before cleanMarkdown does: cleanMarkdown turns every
// "- " line into "<li>...</li>", and the section parser only recognises "- "
// items, so parsing the cleaned text found no key details in a well-formed
// reply. Every article then paid for the separate extraction call in
// extractDetails, and shipped without key details whenever that call failed.
//
// The details are taken from the reply, not from the copy cleanSourceRefs
// checks, so they get the same per-detail treatment as extracted ones. Outlet
// mentions in them are left to the whole-reply check (checkOutlets), which
// sends them to the auto-revision.
func replyDetails(raw string, sources []SourceArticle) ([]KeyDetail, []ArticleLink) {
	_, _, details, links, _ := parseStructuredContent(raw)
	for i := range details {
		// Key details render as plain text, and cleanMarkdown no longer gets
		// the chance to turn a stray **bold** into tags.
		details[i].Label = strings.TrimSpace(strings.ReplaceAll(details[i].Label, "**", ""))
		details[i].Value = strings.TrimSpace(strings.ReplaceAll(details[i].Value, "**", ""))
	}
	return cleanDetailSourceRefs(details, sources), links
}

// cleanDetailSourceRefs removes citation markers from key details and drops
// any detail that still refers to a source by number: a bullet is public copy
// and there is no revision pass after this point.
func cleanDetailSourceRefs(details []KeyDetail, _ []SourceArticle) []KeyDetail {
	var kept []KeyDetail
	for _, d := range details {
		label, _ := StripCitationMarkers(d.Label)
		value, _ := StripCitationMarkers(d.Value)
		if refs := FindSourceRefs(label + " " + value); len(refs) > 0 {
			slog.Warn("dropped key detail with a numbered source reference", "label", d.Label, "refs", refs)
			continue
		}
		kept = append(kept, KeyDetail{Label: label, Value: value})
	}
	return kept
}

// checkOutlets unwraps links to outlet sites in a generator reply (articles
// link to official pages only; the link text stays) and returns the reply with
// every place its visible text names an outlet, for flagOutletMentions.
func checkOutlets(stage, content string, m *OutletMatcher) (string, []OutletMention) {
	out, unlinked := m.StripOutletLinks(content)
	if len(unlinked) > 0 {
		slog.Warn("removed links to news sites", "stage", stage, "outlets", unlinked)
	}
	mentions := m.Find(withoutOutletLinkLines(out, m))
	if len(mentions) > 0 {
		names := make([]string, len(mentions))
		for i, mn := range mentions {
			names[i] = mn.Text
		}
		slog.Warn("article names a news site", "stage", stage, "mentions", names)
	}
	return out, mentions
}

// withoutOutletLinkLines removes the entries of a reply's LINKS section that
// point to an outlet's site. They are dropped from the result anyway (see
// DropOutletLinks), so their text ("OMAAT post") is not worth a revision.
func withoutOutletLinkLines(content string, m *OutletMatcher) string {
	return linksBlockRE.ReplaceAllStringFunc(content, func(block string) string {
		lines := strings.Split(block, "\n")
		kept := lines[:0]
		for _, line := range lines {
			if i := strings.LastIndex(line, " | "); i >= 0 {
				u := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line[i+3:]), "</li>"))
				if _, ok := m.OutletForURL(u); ok {
					continue
				}
			}
			kept = append(kept, line)
		}
		return strings.Join(kept, "\n")
	})
}

// flagOutletMentions records outlet mentions on a critic verdict, before
// adjustScore, as a failed attribution check with one issue per outlet.
func flagOutletMentions(v *VerificationResult, mentions []OutletMention) {
	if len(mentions) == 0 {
		return
	}
	v.AttributionCheck = false
	v.Issues = append(v.Issues, OutletIssues(mentions)...)
}

// dropOutletDetails drops key details that name an outlet the title does not.
// They come from a separate call with no revision pass after it; an outlet the
// title names is taken to be the subject of the story, which may be named.
func dropOutletDetails(details []KeyDetail, m *OutletMatcher, title string) []KeyDetail {
	var kept []KeyDetail
	for _, d := range details {
		if ms := m.DerivedTextMentions(d.Label+": "+d.Value, title); len(ms) > 0 {
			slog.Warn("dropped key detail that names a news site", "label", d.Label, "outlet", ms[0].Outlet)
			continue
		}
		kept = append(kept, d)
	}
	return kept
}

// finalizeContent turns the cleaned reply into the article body, title and
// TLDR. Key details and links come from the raw reply; see replyDetails.
func (p *Pipeline) finalizeContent(content string) (finalContent, title, tldr string) {
	parsedTitle, tldr, _, _, parsedBody := parseStructuredContent(content)
	if parsedTitle == "" {
		parsedTitle = extractTitle(content)
	}
	if parsedBody != "" {
		content = parsedBody
	}
	content = stripStructuralMarkers(content)
	// Format: if body was extracted or content is single-line, just wrap in HTML
	if parsedBody != "" || !strings.Contains(content, "\n") {
		if !strings.Contains(content, "<p>") && content != "" {
			blocks := strings.Split(content, "\n\n")
			var formatted []string
			for _, b := range blocks {
				b = strings.TrimSpace(b)
				if b != "" && !strings.HasPrefix(b, "<") {
					formatted = append(formatted, "<p>"+b+"</p>")
				} else if b != "" {
					formatted = append(formatted, b)
				}
			}
			content = strings.Join(formatted, "\n")
		}
	} else {
		content = formatContent(content)
	}
	return content, parsedTitle, tldr
}

// Compiled patterns for structural-marker handling. Resilient to malformed
// emissions where the model puts markers inline instead of on their own lines —
// e.g. "...KEY_DETAILS_START - foo KEY_DETAILS_END LINKS_START - bar | url LINKS_END <p>body</p>".
//
// Production incident motivating these patterns: 14 articles had structural
// markers leaked into the body and 16 had them in the title, all from the
// Nemotron 49B model that emitted single-line blobs the line-equality parser
// couldn't dissect.
var (
	// Span the entire KEY_DETAILS / LINKS block, lazily, across newlines.
	keyDetailsBlockRE = regexp.MustCompile(`(?s)KEY_DETAILS_START\s*(.*?)\s*KEY_DETAILS_END`)
	linksBlockRE      = regexp.MustCompile(`(?s)LINKS_START\s*(.*?)\s*LINKS_END`)
	// Defensive: any standalone marker that survived block-stripping.
	bareMarkerRE = regexp.MustCompile(`KEY_DETAILS_START|KEY_DETAILS_END|LINKS_START|LINKS_END`)
	// Strips a TLDR: line that landed in the body. Line-anchored on purpose —
	// canonical TLDR is extracted in parseStructuredContent, this only catches
	// leaked copies at line starts and avoids clobbering article prose that
	// happens to mention "TLDR:" mid-sentence.
	bodyTLDRLineRE = regexp.MustCompile(`(?m)^\s*TLDR:[^\n]*\n?`)
	// Truncates the title at the first leaked structural marker. Requires
	// preceding whitespace so we don't accidentally cut a legitimate word
	// that contains the marker as a suffix substring.
	titleStopRE = regexp.MustCompile(`\s+(TLDR:|KEY_DETAILS_START|LINKS_START)`)
	// Collapses excessive blank lines left by block removals.
	blankLineCollapseRE = regexp.MustCompile(`\n{3,}`)
)

// parseStructuredContent extracts sections from the generator's structured
// output. Tolerates malformed emissions where markers appear inline rather than
// on dedicated lines.
func parseStructuredContent(raw string) (title, tldr string, keyDetails []KeyDetail, links []ArticleLink, body string) {
	lines := strings.Split(raw, "\n")

	// Title: first non-empty line, truncated at any structural marker that
	// leaked onto the same line.
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if loc := titleStopRE.FindStringIndex(t); loc != nil {
			t = strings.TrimSpace(t[:loc[0]])
		}
		title = t
		break
	}
	if len(title) > 150 {
		if idx := strings.LastIndex(title[:150], " "); idx > 50 {
			title = title[:idx]
		} else {
			title = title[:150]
		}
	}

	// TLDR: search anywhere in raw for "TLDR:" and capture until newline or next marker.
	if loc := strings.Index(raw, "TLDR:"); loc >= 0 {
		rest := raw[loc+len("TLDR:"):]
		// Stop at the next newline OR the next structural marker, whichever comes first.
		end := len(rest)
		if i := strings.Index(rest, "\n"); i >= 0 && i < end {
			end = i
		}
		// Stop at the next structural marker in case a malformed emission
		// concatenated multiple sections without any newline (a second "TLDR:"
		// can also occur when the model echoed itself).
		for _, marker := range []string{"KEY_DETAILS_START", "LINKS_START", "TLDR:"} {
			if i := strings.Index(rest, marker); i >= 0 && i < end {
				end = i
			}
		}
		tldr = strings.TrimSpace(rest[:end])
	}

	// Key details: prefer regex block match (handles inline emissions). If found,
	// split items by line (well-formed) or fall through to inline split.
	if m := keyDetailsBlockRE.FindStringSubmatch(raw); m != nil {
		for _, item := range parseDashItems(m[1]) {
			if idx := strings.Index(item, ": "); idx > 0 {
				keyDetails = append(keyDetails, KeyDetail{
					Label: strings.TrimSpace(item[:idx]),
					Value: strings.TrimSpace(item[idx+2:]),
				})
			}
		}
	}

	// Links: same regex strategy.
	if m := linksBlockRE.FindStringSubmatch(raw); m != nil {
		for _, item := range parseDashItems(m[1]) {
			if idx := strings.Index(item, " | "); idx > 0 {
				links = append(links, ArticleLink{
					Text: strings.TrimSpace(item[:idx]),
					URL:  strings.TrimSpace(item[idx+3:]),
				})
			}
		}
	}

	// Body: everything after the LAST occurrence of "LINKS_END" anywhere in the
	// raw output (substring search, so it works even when LINKS_END is mid-line).
	if idx := strings.LastIndex(raw, "LINKS_END"); idx >= 0 {
		body = strings.TrimSpace(raw[idx+len("LINKS_END"):])
	} else {
		// Fallback: skip the title line, take the rest.
		body = raw
		if i := strings.Index(raw, "\n"); i != -1 {
			body = strings.TrimSpace(raw[i+1:])
		}
	}

	return
}

// parseDashItems splits a block of bullet-style items "- foo - bar - baz" into
// individual items. Handles both newline-separated (well-formed) and inline
// (malformed) layouts.
func parseDashItems(block string) []string {
	block = strings.TrimSpace(block)
	if block == "" {
		return nil
	}
	if strings.Contains(block, "\n") {
		var items []string
		for _, line := range strings.Split(block, "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "- ") {
				items = append(items, strings.TrimSpace(strings.TrimPrefix(t, "- ")))
			}
		}
		return items
	}
	// Inline: split on " - " between items, preserving the first one.
	if !strings.HasPrefix(block, "- ") {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(block, "- "), " - ")
	var items []string
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			items = append(items, t)
		}
	}
	return items
}

// stripStructuralMarkers removes structural markers from article body content.
// Handles both well-formed (markers on own lines) and malformed (inline)
// emissions by regex-stripping entire blocks first, then any bare residual
// markers.
func stripStructuralMarkers(content string) string {
	content = keyDetailsBlockRE.ReplaceAllString(content, "")
	content = linksBlockRE.ReplaceAllString(content, "")
	content = bareMarkerRE.ReplaceAllString(content, "")
	content = bodyTLDRLineRE.ReplaceAllString(content, "")
	content = blankLineCollapseRE.ReplaceAllString(content, "\n\n")
	return strings.TrimSpace(content)
}

// adjustScore enforces consistency between the score and the check results.
// Each failed check deducts points, and the score is capped accordingly.
func adjustScore(v *VerificationResult) int {
	score := v.Score
	failCount := 0
	if !v.FactualCheck {
		failCount++
	}
	if !v.AttributionCheck {
		failCount++
	}
	if !v.CompletenessCheck {
		failCount++
	}
	if !v.HallucinationCheck {
		failCount++
	}

	// Hallucination is the most severe — cap at 60
	if !v.HallucinationCheck {
		if score > 60 {
			score = 60
		}
	}

	// Each other failed check deducts 10 points
	otherFails := failCount
	if !v.HallucinationCheck {
		otherFails--
	}
	score -= otherFails * 10

	// Cap: can't be above 85 with any failed check, can't be above 70 with 2+ failures
	if failCount > 0 && score > 85 {
		score = 85
	}
	if failCount >= 2 && score > 70 {
		score = 70
	}
	if score < 0 {
		score = 0
	}

	return score
}

// AdjustScore is adjustScore for tools that measure critics outside the
// pipeline (cmd/ab-models), so they report the score production acts on
// rather than the critic's raw number.
func AdjustScore(v *VerificationResult) int {
	return adjustScore(v)
}

// detectArticleType determines the article type from source content.
// Deal: time-limited offers (bonuses, sales, discounts, signup offers)
// Summary: news, changes, analysis, reviews
func detectArticleType(sources []SourceArticle) string {
	dealKeywords := []string{"bonus", "sale", "discount", "offer", "deal", "save", "off", "promotion", "sign-up", "signup", "limited time", "flash", "promo"}
	dealScore := 0
	for _, s := range sources {
		lower := strings.ToLower(s.Title + " " + s.Content[:min(200, len(s.Content))])
		for _, kw := range dealKeywords {
			if strings.Contains(lower, kw) {
				dealScore++
			}
		}
	}
	if dealScore >= 2 {
		return "deal"
	}
	return "summary"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// combinedCritique runs both critics and merges their results.
func (p *Pipeline) combinedCritique(content string, sources []SourceArticle) *VerificationResult {
	// Critic 1: factual accuracy, source compliance
	v1, err := p.critic.Verify(content, sources)
	if err != nil {
		// If critic 1 fails, return a failing score
		return &VerificationResult{Score: 0, Issues: []string{"critic 1 error: " + err.Error()}}
	}
	v1.Score = adjustScore(v1)

	// Critic 2: reasoning, consistency (if configured)
	if p.critic2 == nil {
		return v1
	}

	v2, err := p.critic2.Verify2(content, sources)
	if err != nil {
		// If critic 2 fails, just use critic 1 results
		return v1
	}
	v2.Score = adjustScore(v2)

	// Merge: take the lower score, combine issues, AND any failed checks
	merged := &VerificationResult{
		Score:              v1.Score,
		Issues:             append(v1.Issues, v2.Issues...),
		FactualCheck:       v1.FactualCheck && v2.FactualCheck,
		AttributionCheck:   v1.AttributionCheck && v2.AttributionCheck,
		CompletenessCheck:  v1.CompletenessCheck && v2.CompletenessCheck,
		HallucinationCheck: v1.HallucinationCheck && v2.HallucinationCheck,
	}
	if v2.Score < merged.Score {
		merged.Score = v2.Score
	}
	merged.Score = adjustScore(merged)
	return merged
}

// cleanMarkdown converts common Markdown remnants to HTML.
func cleanMarkdown(s string) string {
	// Convert lines of dashes/equals (section dividers) to <hr>
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Lines that are mostly dashes or equals (e.g. "---", "====", "----------")
		if len(trimmed) >= 3 {
			allDashes := true
			for _, c := range trimmed {
				if c != '-' && c != '=' && c != ' ' {
					allDashes = false
					break
				}
			}
			if allDashes {
				lines[i] = `<hr class="topic-break">`
			}
		}
		// Also catch inline dashes like "Topic -------------------------------- Content"
		if strings.Contains(lines[i], "--------") {
			parts := strings.SplitN(lines[i], "--------", 2)
			before := strings.TrimSpace(parts[0])
			after := ""
			if len(parts) > 1 {
				// Remove any remaining dashes from the second part
				cleaned := strings.TrimLeft(parts[1], "-")
				after = strings.TrimSpace(cleaned)
			}
			result := ""
			if before != "" {
				result += before + "\n"
			}
			result += `<hr class="topic-break">`
			if after != "" {
				result += "\n" + after
			}
			lines[i] = result
		}
	}
	s = strings.Join(lines, "\n")

	// Convert **bold** to <strong>bold</strong>
	for strings.Contains(s, "**") {
		start := strings.Index(s, "**")
		rest := s[start+2:]
		end := strings.Index(rest, "**")
		if end == -1 {
			break
		}
		s = s[:start] + "<strong>" + rest[:end] + "</strong>" + rest[end+2:]
	}
	// Convert ## Heading to <h3>Heading</h3>
	// Convert * bullet and - bullet to <li>bullet</li>
	lines = strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			lines[i] = "<h3>" + strings.TrimPrefix(trimmed, "## ") + "</h3>"
		} else if strings.HasPrefix(trimmed, "### ") {
			lines[i] = "<h3>" + strings.TrimPrefix(trimmed, "### ") + "</h3>"
		} else if strings.HasPrefix(trimmed, "* ") {
			lines[i] = "<li>" + strings.TrimPrefix(trimmed, "* ") + "</li>"
		} else if strings.HasPrefix(trimmed, "- ") {
			lines[i] = "<li>" + strings.TrimPrefix(trimmed, "- ") + "</li>"
		}
	}
	return strings.Join(lines, "\n")
}

// formatContent transforms plain-text content into proper HTML with paragraphs,
// headings, and lists. It runs after cleanMarkdown in the pipeline.
func formatContent(content string) string {
	// If already has HTML paragraph tags, skip formatting
	if strings.Contains(content, "<p>") {
		return content
	}

	// Remove the title line (first line is extracted separately)
	if idx := strings.Index(content, "\n"); idx != -1 {
		content = strings.TrimSpace(content[idx+1:])
	} else {
		// Content is just the title with no body
		return ""
	}

	if content == "" {
		return ""
	}

	// Split into blocks on double newlines
	blocks := regexp.MustCompile(`\n\s*\n`).Split(content, -1)

	// Filter out empty blocks first
	var cleanBlocks []string
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block != "" {
			cleanBlocks = append(cleanBlocks, block)
		}
	}

	var result []string
	for i, block := range cleanBlocks {
		lines := strings.Split(block, "\n")

		// Check if this block is an unordered list (all lines are <li> items or start with * or -)
		if isUnorderedList(lines) {
			result = append(result, "<ul>")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "<li>") {
					result = append(result, line)
				} else if strings.HasPrefix(line, "* ") {
					result = append(result, "<li>"+strings.TrimPrefix(line, "* ")+"</li>")
				} else if strings.HasPrefix(line, "- ") {
					result = append(result, "<li>"+strings.TrimPrefix(line, "- ")+"</li>")
				} else if line != "" {
					result = append(result, "<li>"+line+"</li>")
				}
			}
			result = append(result, "</ul>")
			continue
		}

		// Check if this block is an ordered list (lines starting with 1. 2. etc.)
		if isOrderedList(lines) {
			result = append(result, "<ol>")
			olRegex := regexp.MustCompile(`^\d+\.\s*`)
			for _, line := range lines {
				line = strings.TrimSpace(line)
				text := olRegex.ReplaceAllString(line, "")
				if text != "" {
					result = append(result, "<li>"+text+"</li>")
				}
			}
			result = append(result, "</ol>")
			continue
		}

		// Check if this is a single-line heading candidate (must be followed by more content)
		if len(lines) == 1 {
			line := strings.TrimSpace(lines[0])
			// Already an <h3> from cleanMarkdown
			if strings.HasPrefix(line, "<h3>") {
				result = append(result, line)
				continue
			}
			// Short line without period at end looks like a heading,
			// but only if it's followed by more content blocks
			if i < len(cleanBlocks)-1 && isHeadingCandidate(line) {
				result = append(result, "<h3>"+line+"</h3>")
				continue
			}
		}

		// Default: wrap the block in <p> tags
		text := strings.Join(lines, " ")
		text = strings.TrimSpace(text)
		if text != "" {
			// Don't wrap if it's already an HTML block element
			if strings.HasPrefix(text, "<h3>") || strings.HasPrefix(text, "<ul>") || strings.HasPrefix(text, "<ol>") || strings.HasPrefix(text, "<hr") {
				result = append(result, text)
			} else {
				result = append(result, "<p>"+text+"</p>")
			}
		}
	}

	return strings.Join(result, "\n")
}

// isUnorderedList returns true if all non-empty lines in the block are list items.
func isUnorderedList(lines []string) bool {
	count := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "<li>") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "- ") {
			count++
		} else {
			return false
		}
	}
	return count > 0
}

// isOrderedList returns true if all non-empty lines start with a number followed by a period.
func isOrderedList(lines []string) bool {
	olRegex := regexp.MustCompile(`^\d+\.\s`)
	count := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if olRegex.MatchString(line) {
			count++
		} else {
			return false
		}
	}
	return count > 0
}

// isHeadingCandidate returns true if a line looks like a heading:
// short (under 80 chars), doesn't end with a period, and isn't already HTML.
func isHeadingCandidate(line string) bool {
	if len(line) > 80 {
		return false
	}
	if strings.HasSuffix(line, ".") {
		return false
	}
	if strings.HasPrefix(line, "<") {
		return false
	}
	// Must have at least some alphabetic content
	if len(strings.TrimSpace(line)) < 3 {
		return false
	}
	return true
}

// extractTitle returns the first line of the generated content as the title,
// stripping any Markdown or HTML artifacts.
func extractTitle(content string) string {
	title := content
	if idx := strings.Index(content, "\n"); idx != -1 {
		title = content[:idx]
	}
	// Strip Markdown bold markers
	title = strings.ReplaceAll(title, "**", "")
	// Strip Markdown heading markers
	title = strings.TrimLeft(title, "# ")
	// Strip HTML tags
	for {
		start := strings.Index(title, "<")
		if start == -1 {
			break
		}
		end := strings.Index(title[start:], ">")
		if end == -1 {
			break
		}
		title = title[:start] + title[start+end+1:]
	}
	// Truncate overly long titles
	title = strings.TrimSpace(title)
	if len(title) > 150 {
		// Try to cut at last space before 150
		if idx := strings.LastIndex(title[:150], " "); idx > 50 {
			title = title[:idx]
		} else {
			title = title[:150]
		}
	}
	return title
}
