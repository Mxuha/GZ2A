// sse.go — stateful Z.AI SSE parser.
// Port of orig GLM-Free-API streamSSEResponse: edit_content+edit_index
// replacement, <details> reasoning split, inline errors, delta diffs.
package app

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// utf16IndexToByteIndex converts a UTF-16 code-unit offset — the indexing
// semantics of JavaScript strings used by the Z.AI web frontend
// (content.substring(0, edit_index)) — into a byte offset within s.
// Clamps at the end of s and never returns an offset inside a multi-byte
// rune: an index landing between the units of a surrogate pair clamps to
// the start of that rune. Ported from the upstream garble fix (#23).
func utf16IndexToByteIndex(s string, utf16Idx int) int {
	if utf16Idx <= 0 {
		return 0
	}
	byteIdx, units := 0, 0
	for byteIdx < len(s) {
		if units == utf16Idx {
			return byteIdx
		}
		r, size := utf8.DecodeRuneInString(s[byteIdx:])
		ru := utf16.RuneLen(r)
		if ru < 0 {
			ru = 1 // invalid byte: JS would also see one unit here
		}
		if units+ru > utf16Idx {
			return byteIdx // inside a surrogate pair — clamp to rune start
		}
		units += ru
		byteIdx += size
	}
	return len(s)
}

// sseParser accumulates Z.AI SSE chunks into a full text and emits
// content/reasoning deltas relative to what has already been sent.
type sseParser struct {
	fullText         strings.Builder
	reasoningAcc     strings.Builder // phase-tagged reasoning (thinking events)
	imagesMD         strings.Builder // markdown images extracted from tool results
	lateImagesMD     strings.Builder // workspace images fetched post-stream (fallback)
	seenImages       map[string]bool // dedupe (tool results can repeat)
	wsPaths          map[string]bool // workspace image file paths seen in the stream
	wsEmbedded       map[string]bool // workspace paths successfully embedded
	curToolID        string          // current tool_call_id (tool response buffer key)
	curToolBuf       string          // accumulated tool_response text for curToolID
	onWsPath         func(fp string) // live hook: fetch a workspace image mid-stream
	contentEmitter   sseEmitter
	reasoningEmitter sseEmitter
	logLevel         string
}

func newSSEParser(logLevel string) *sseParser {
	return &sseParser{
		logLevel:   logLevel,
		seenImages: make(map[string]bool),
		wsPaths:    make(map[string]bool),
		wsEmbedded: make(map[string]bool),
	}
}

// split divides accumulated raw text into (content, reasoning) by the
// <details ...>...</details> marker (handles an unclosed tag mid-stream).
func (p *sseParser) split() (string, string) {
	raw := p.fullText.String()
	idx := strings.Index(raw, "<details")
	if idx < 0 {
		return raw, ""
	}
	tagEnd := strings.Index(raw[idx:], ">")
	if tagEnd < 0 {
		return raw, "" // tag not complete yet
	}
	afterTag := raw[idx+tagEnd+1:]
	if closeIdx := strings.Index(afterTag, "</details>"); closeIdx >= 0 {
		reasoning := stripDetailsTags(afterTag[:closeIdx])
		content := raw[:idx] + afterTag[closeIdx+len("</details>"):]
		return content, reasoning
	}
	// <details> opened but not yet closed
	return raw[:idx], stripDetailsTags(afterTag)
}

// content returns the accumulated content text (reasoning excluded) with
// extracted images appended after the answer (they stream after it too —
// end-of-stream emission never splits an in-flight sentence). TrimSpace here
// is safe: this is the FINAL authoritative value, not a streaming snapshot.
func (p *sseParser) content() string {
	c, _ := p.split()
	c = strings.TrimSpace(c)
	imgs := p.imageMarkdown()
	if imgs == "" {
		return c
	}
	if c == "" {
		return imgs
	}
	return c + "\n\n" + imgs
}

// imageMarkdown returns the extracted image markdown (URL images + workspace
// data-URI images) in recorded order, or "". Images the model already
// embedded inline in the answer markdown are dropped (no duplicates).
func (p *sseParser) imageMarkdown() string {
	ft := p.fullText.String()
	var parts []string
	for _, bucket := range []string{p.imagesMD.String(), p.lateImagesMD.String()} {
		bucket = strings.TrimSpace(bucket)
		if bucket == "" {
			continue
		}
		for _, block := range strings.Split(bucket, "\n\n") {
			if u := mdImageURL(block); u != "" && strings.Contains(ft, "]("+u+")") {
				continue // model already embedded this URL inline
			}
			parts = append(parts, block)
		}
	}
	return strings.Join(parts, "\n\n")
}

// mdImageURL extracts the URL from a `![alt](url)` markdown line ("" when the
// line is not a single image markdown block).
func mdImageURL(block string) string {
	block = strings.TrimSpace(block)
	if !strings.HasPrefix(block, "![") {
		return ""
	}
	i := strings.Index(block, "](")
	if i < 0 {
		return ""
	}
	rest := block[i+2:]
	j := strings.LastIndex(rest, ")")
	if j <= 0 {
		return ""
	}
	return rest[:j]
}

// contentLen returns the byte length of the accumulated content.
func (p *sseParser) contentLen() int {
	return len(p.content())
}

// reasoning returns the accumulated reasoning text (<details> blocks).
func (p *sseParser) reasoning() string {
	_, r := p.split()
	return strings.TrimSpace(r)
}

// stripDetailsTags removes <details ...>/</details> and <summary>/</summary>
// wrappers plus leading "> " markdown-quote prefixes from each line.
// NOTE: no TrimSpace here — the streaming emitter requires the stripped
// snapshot sequence to grow monotonically; trailing-whitespace trimming
// oscillates the target by ±1 char (holdback + re-sync then duplicate
// chars). Final consumers (content()/reasoning()) trim instead.
func stripDetailsTags(s string) string {
	if idx := strings.Index(s, "<details"); idx >= 0 {
		if end := strings.Index(s[idx:], ">"); end >= 0 {
			s = s[:idx] + s[idx+end+1:]
		}
	}
	if idx := strings.Index(s, "<summary"); idx >= 0 {
		if end := strings.Index(s[idx:], ">"); end >= 0 {
			s = s[:idx] + s[idx+end+1:]
		}
	}
	s = strings.ReplaceAll(s, "</details>", "")
	s = strings.ReplaceAll(s, "</summary>", "")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimPrefix(l, "> ")
	}
	return strings.Join(lines, "\n")
}

// applyEdit applies an edit_content chunk. Semantics mirror the official
// Z.AI frontend: content = content.substring(0, edit_index) + edit_content,
// where edit_index counts UTF-16 code units. editIndex < 0 (field absent in
// the event) keeps the bridge's legacy append behavior — deliberately
// different from the frontend's default-0 full replacement to avoid a
// simultaneous behavioral flip; revisit if Z.AI is observed omitting the
// field. Indexes beyond the current length truncate at len(s) — matching
// substring(), i.e. plain concatenation, not space padding.
func (p *sseParser) applyEdit(ec string, editIndex int) {
	current := p.fullText.String()
	if editIndex < 0 {
		current += ec
	} else {
		byteIdx := utf16IndexToByteIndex(current, editIndex)
		current = current[:byteIdx] + ec
	}
	p.fullText.Reset()
	p.fullText.WriteString(current)
}

// holdbackRunes — how many trailing runes stay unemitted while a stream is
// live, so trailing edit_content backtracks (Z.AI rewrites the tail) are
// absorbed invisibly instead of leaking then vanishing on the client.
// Mirrors upstream STREAM_HOLDBACK default.
var holdbackRunes = 24

// holdBackTail trims up to n runes from the end of s (rune-safe).
func holdBackTail(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	i, count := len(s), 0
	for i > 0 && count < n {
		_, size := utf8.DecodeLastRuneInString(s[:i])
		i -= size
		count++
	}
	return s[:i]
}

// holdBackPartialDetailsTag trims a trailing fragment that could still be
// the beginning of a <details> tag whose completion has not arrived yet,
// so a tag streamed character by character never leaks to the client.
// A COMPLETE "</details>" literal is kept (legitimate text); a complete
// "<details" is held (waiting for its ">" to decide whether it is a tag).
func holdBackPartialDetailsTag(s string) string {
	i := strings.LastIndex(s, "<")
	if i < 0 {
		return s
	}
	suffix := s[i:]
	// A partial OPEN tag is held regardless (its ">" decides nothing yet);
	// a partial CLOSE tag is held too; complete literals pass through.
	for _, open := range []string{"<details", "<summary"} {
		if len(suffix) <= len(open) && strings.HasPrefix(open, suffix) {
			return s[:i]
		}
	}
	for _, close := range []string{"</details>", "</summary>"} {
		if len(suffix) < len(close) && strings.HasPrefix(close, suffix) {
			return s[:i]
		}
	}
	return s
}

// holdBackPartialQuoteMarker trims a trailing ">" that forms the entire
// last line of s. Lines inside a <details> reasoning body are markdown-
// quoted ("> ..."), and while the body streams in one character at a
// time a new line's quote marker is transiently present as a bare ">"
// — which stripDetailsTags cannot strip yet (TrimPrefix needs the
// space) but strips one character later when the "> " completes.
// Forwarding that transient ">" makes the stripped-reasoning snapshot
// sequence non-monotonic: the client keeps the stray ">" forever.
// Holding the marker back keeps the snapshot sequence monotonic; the
// final flush releases it if the text really ends there.
// (Port of the upstream issue #26 fix.)
func holdBackPartialQuoteMarker(s string) string {
	if !strings.HasSuffix(s, ">") {
		return s
	}
	body := s[:len(s)-1]
	if body == "" || strings.HasSuffix(body, "\n") {
		return body
	}
	return s
}

// sseEmitter is a prefix-aware delta emitter (upstream issue #26 fix).
// delta(target) returns what the client has not seen yet and re-syncs the
// tracked view to target — after a rewrite shrinks the snapshot, the old
// offset model would rewind and later RE-EMIT everything after the rewind
// point (growing-prefix duplication on the client). With re-sync semantics
// the emitter only ever forwards the common-prefix remainder; a shrunken
// snapshot simply yields an empty delta (the small holdback window absorbs
// the realistic rewind cases before they ever reach the client).
type sseEmitter struct {
	clientView string
}

// commonPrefixLen returns the byte length of the longest common prefix of
// a and b. Always on a rune boundary.
func commonPrefixLen(a, b string) int {
	i := 0
	for i < len(a) && i < len(b) {
		ra, sa := utf8.DecodeRuneInString(a[i:])
		rb, _ := utf8.DecodeRuneInString(b[i:])
		if ra != rb {
			break
		}
		i += sa
	}
	return i
}

func (e *sseEmitter) delta(target string) string {
	// Monotonic view: when the (holdback-trimmed) target is shorter than
	// what the client already received, emit nothing and KEEP the view —
	// rewinding it here made the final flush re-emit everything after the
	// rewind point (the very duplication issue #26 is about).
	if len(target) < len(e.clientView) {
		return ""
	}
	cp := commonPrefixLen(e.clientView, target)
	delta := target[cp:]
	e.clientView = target
	return delta
}

// flush emits content/reasoning deltas relative to already-sent offsets.
// While the stream is live (final=false) a small tail is held back so
// trailing edit_content backtracks are absorbed invisibly, partial
// <details>/</details> tag fragments never leak, and transient bare ">"
// quote markers (reasoning) don't reach the client. FlushFinal releases
// everything.
func (p *sseParser) flush(onContent, onReasoning func(string)) {
	p.emit(onContent, onReasoning, false)
}

// FlushFinal releases all held-back tail content at end of stream.
func (p *sseParser) FlushFinal(onContent, onReasoning func(string)) {
	p.emit(onContent, onReasoning, true)
}

func (p *sseParser) emit(onContent, onReasoning func(string), final bool) {
	content, reasoning := p.split()
	if p.reasoningAcc.Len() > 0 {
		reasoning = p.reasoningAcc.String()
	}

	if onReasoning != nil {
		if !final {
			reasoning = holdBackTail(reasoning, holdbackRunes)
			reasoning = holdBackPartialDetailsTag(reasoning)
			reasoning = holdBackPartialQuoteMarker(reasoning)
		}
		if delta := p.reasoningEmitter.delta(reasoning); delta != "" {
			onReasoning(delta)
		}
	}
	if onContent != nil {
		if !final {
			content = holdBackTail(content, holdbackRunes)
			content = holdBackPartialDetailsTag(content)
		}
		if delta := p.contentEmitter.delta(content); delta != "" {
			onContent(delta)
		}
	}
}

// extractZAIError pulls an inline error detail from an SSE JSON object
// (HTTP 200 with an error in the body).
func extractZAIError(j map[string]interface{}) string {
	if data, ok := j["data"].(map[string]interface{}); ok {
		if errObj, ok := data["error"].(map[string]interface{}); ok {
			if detail, _ := errObj["detail"].(string); detail != "" {
				return detail
			}
			if msg, _ := errObj["message"].(string); msg != "" {
				return msg
			}
		}
	}
	if errObj, ok := j["error"].(map[string]interface{}); ok {
		if detail, _ := errObj["detail"].(string); detail != "" {
			return detail
		}
		if msg, _ := errObj["message"].(string); msg != "" {
			return msg
		}
	}
	return ""
}

// ── Tool-result image extraction ──
//
// Agent-mode tool_response events carry generated/fetched images either in
// metadata (zai_dr_visit_img → metadata.images[], several zai_* tools →
// metadata.img_url) or as JSON content parts ([{type:"image_url",...},...]).
// Extracted URLs become markdown images that stream to the client as content
// and prefix the authoritative final content — SillyTavern renders them.

// recordImage adds url to the image markdown (deduped). Unconfident URLs
// (generic keys like url/src) must carry an image extension; confident ones
// (image_url parts, img_url, images[]) are recorded as-is — Z.AI image CDN
// URLs frequently have no extension.
func (p *sseParser) recordImage(url string, confident bool) string {
	url = strings.TrimSpace(url)
	if url == "" || !strings.Contains(url, "://") {
		return ""
	}
	if !confident && !looksLikeImageURL(url) {
		return ""
	}
	if p.seenImages[url] {
		return ""
	}
	p.seenImages[url] = true
	md := "![image](" + url + ")"
	if p.imagesMD.Len() > 0 {
		p.imagesMD.WriteString("\n\n")
	}
	p.imagesMD.WriteString(md)
	return md
}

var imageExtRegex = regexp.MustCompile(`(?i)\.(png|jpe?g|webp|gif|bmp|avif|heic|tiff)(\?|$)`)

func looksLikeImageURL(u string) bool { return imageExtRegex.MatchString(u) }

// wsPathRegex matches absolute sandbox file paths of generated images, e.g.
// /home/z/my-project/download/cat.png — the agent-mode image generation
// (Skill → Bash → z-ai CLI) saves files into the workspace; the proxy fetches
// them after the stream and embeds them as data URIs.
var wsPathRegex = regexp.MustCompile(`(/home/[A-Za-z0-9_./-]+\.(?:png|jpe?g|webp|gif|bmp|avif))`)

// noteWorkspacePaths records image file paths found in a text chunk (tool
// outputs or answer text mentioning saved files). New paths fire the live
// onWsPath hook (mid-stream fetch while the sandbox session is active).
func (p *sseParser) noteWorkspacePaths(text string) {
	for _, m := range wsPathRegex.FindAllString(text, -1) {
		m = strings.TrimSpace(m)
		if m == "" || p.wsPaths[m] {
			continue
		}
		p.wsPaths[m] = true
		if p.onWsPath != nil && !p.wsEmbedded[m] {
			p.onWsPath(m)
		}
	}
}

// workspacePaths returns the deduped list of sandbox image paths that have
// NOT been embedded yet (candidates for the post-stream fallback fetch).
func (p *sseParser) workspacePaths() []string {
	out := make([]string, 0, len(p.wsPaths))
	for k := range p.wsPaths {
		if !p.wsEmbedded[k] {
			out = append(out, k)
		}
	}
	return out
}

// appendEarlyImage records a data-URI markdown image in the early bucket
// (recorded mid-stream; emitted at end-of-stream like every image).
func (p *sseParser) appendEarlyImage(dataURI string) {
	md := "![image](" + dataURI + ")"
	if p.imagesMD.Len() > 0 {
		p.imagesMD.WriteString("\n\n")
	}
	p.imagesMD.WriteString(md)
}

// markWsEmbedded marks a workspace path as embedded (dedupe + excludes it
// from the post-stream fallback list).
func (p *sseParser) markWsEmbedded(fp string) { p.wsEmbedded[fp] = true }

// appendWorkspaceImage records a fetched workspace image (data URI) as a
// late markdown image — appended after the answer text, matching the
// post-stream emission order.
func (p *sseParser) appendWorkspaceImage(dataURI string) {
	md := "![image](" + dataURI + ")"
	if p.lateImagesMD.Len() > 0 {
		p.lateImagesMD.WriteString("\n\n")
	}
	p.lateImagesMD.WriteString(md)
}

// walkImages walks a decoded JSON value and records image URLs:
//   - {type:"image_url", image_url:{url}} parts (confident)
//   - img_url / image_url string fields (confident)
//   - objects inside images[] arrays: their url/src/original_url (confident)
//   - any other *url* key with an image-extension value (extension-gated)
func (p *sseParser) walkImages(v interface{}, inImages bool) {
	switch t := v.(type) {
	case map[string]interface{}:
		// {type:"image_url", image_url:{url:...}} part
		if ty, _ := t["type"].(string); ty == "image_url" {
			if nested, ok := t["image_url"].(map[string]interface{}); ok {
				if u, _ := nested["url"].(string); u != "" {
					p.recordImage(u, true)
				}
			}
			return
		}
		for k, val := range t {
			if s, ok := val.(string); ok {
				lk := strings.ToLower(k)
				switch {
				case lk == "img_url" || lk == "image_url":
					p.recordImage(s, true)
				case strings.Contains(lk, "url") || lk == "src" || lk == "image" || lk == "thumbnail" || lk == "original":
					p.recordImage(s, inImages)
				}
				continue
			}
			switch k {
			case "images", "image_urls":
				p.walkImages(val, true)
			case "results", "content_items", "content", "data", "result":
				p.walkImages(val, false)
			}
		}
	case []interface{}:
		for _, item := range t {
			p.walkImages(item, inImages)
		}
	}
}

// handleToolResponse processes one tool_response event: extracts images from
// metadata (images[]/img_url — complete per event) and from the event's text
// (CLI output + JSON results stream in chunks, so the per-tool_call buffer is
// re-scanned whole on every chunk; dedupe absorbs repeats). Images are only
// RECORDED here — emission is deferred to end-of-stream (imageMarkdown) so
// they never split an in-flight answer sentence.
func (p *sseParser) handleToolResponse(data map[string]interface{}) {
	if md, ok := data["metadata"].(map[string]interface{}); ok {
		p.walkImages(md, false)
	}
	if dc, ok := data["delta_content"].(string); ok && dc != "" {
		key := ""
		if md, ok := data["metadata"].(map[string]interface{}); ok {
			if id, ok := md["tool_call_id"].(string); ok {
				key = id
			}
		}
		p.scanImageURLs(p.toolBuf(key, dc))
	}
}

// toolBuf appends chunk to the per-tool_call accumulation buffer and returns
// the whole buffer (capped). A new tool_call_id resets the buffer.
func (p *sseParser) toolBuf(key, chunk string) string {
	if key != p.curToolID {
		p.curToolID = key
		p.curToolBuf = chunk
	} else {
		p.curToolBuf += chunk
		if len(p.curToolBuf) > 128*1024 {
			p.curToolBuf = p.curToolBuf[len(p.curToolBuf)-64*1024:]
		}
	}
	return p.curToolBuf
}

// imageURLTextRegex matches URLs with an image extension inside free text
// (CLI output, JSON results, markdown). Delimiters stop the URL; placeholder
// URLs like https://host/x/<hash>.png cannot match (< is excluded).
var imageURLTextRegex = regexp.MustCompile(`https?://[^\s"'<>\)\]]+\.(?:png|jpe?g|webp|gif|bmp|avif)(?:\?[^\s"'<>\)\]]*)?`)

// scanImageURLs records every image URL found in free text (unconfident —
// extension-gated by the regex itself).
func (p *sseParser) scanImageURLs(text string) {
	for _, u := range imageURLTextRegex.FindAllString(text, -1) {
		p.recordImage(u, false)
	}
}

// feedLine parses one SSE line, accumulates content and emits deltas.
// Returns io.EOF on [DONE] / phase done, or an error when Z.AI reports
// an inline error.
func (p *sseParser) feedLine(line string, onContent, onReasoning func(string)) error {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || !strings.HasPrefix(trimmed, "data: ") {
		return nil
	}
	dataStr := trimmed[6:]
	if dataStr == "[DONE]" {
		return io.EOF
	}

	var j map[string]interface{}
	if err := json.Unmarshal([]byte(dataStr), &j); err != nil {
		if p.logLevel == "debug" {
			log.Printf("[DEBUG] Z.AI failed to parse SSE: %s", dataStr)
		}
		return nil
	}

	// Inline errors (HTTP 200 with error in body)
	if detail := extractZAIError(j); detail != "" {
		if p.logLevel == "debug" {
			log.Printf("[DEBUG] Z.AI inline SSE error: %s", detail)
		}
		return fmt.Errorf("Z.AI: %s", detail)
	}

	if data, ok := j["data"].(map[string]interface{}); ok {
		phase, _ := data["phase"].(string)
		if phase == "done" {
			return io.EOF
		}
		// Thinking → reasoning buffer (streams live to client)
		if phase == "thinking" {
			if dc, ok := data["delta_content"].(string); ok && dc != "" {
				p.reasoningAcc.WriteString(dc)
			}
			p.flush(onContent, onReasoning)
			return nil
		}
		// Tool invocation (search query, agent tools) → skip text, harvest
		// images from tool RESPONSES only.
		if phase == "tool_call" {
			return nil
		}
		// Tool response (search results, fetched pages, generated images)
		// → extract images, skip the rest of the payload.
		if phase == "tool_response" {
			if dc, ok := data["delta_content"].(string); ok {
				p.noteWorkspacePaths(dc)
			}
			p.handleToolResponse(data)
			return nil
		}
		// Model's answer → content
		if phase == "answer" {
			if dc, ok := data["delta_content"].(string); ok && dc != "" {
				p.fullText.WriteString(dc)
				p.noteWorkspacePaths(dc)
			} else if ec, ok := data["edit_content"].(string); ok && ec != "" {
				p.fullText.WriteString(ec)
				p.noteWorkspacePaths(ec)
			}
			p.flush(onContent, onReasoning)
			return nil
		}
		// Fallback: unphased events (edit_content, delta_content, content)
		if ec, ok := data["edit_content"].(string); ok && ec != "" {
			editIndex := -1
			if ei, ok := data["edit_index"].(float64); ok {
				editIndex = int(ei)
			}
			p.applyEdit(ec, editIndex)
		} else if dc, ok := data["delta_content"].(string); ok && dc != "" {
			p.fullText.WriteString(dc)
		} else if tc, ok := data["content"].(string); ok && tc != "" {
			p.fullText.WriteString(tc)
		} else if choices, ok := j["choices"].([]interface{}); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]interface{}); ok {
				if delta, ok := choice["delta"].(map[string]interface{}); ok {
					if content, ok := delta["content"].(string); ok && content != "" {
						p.fullText.WriteString(content)
					}
				}
			}
		}
	}
	p.flush(onContent, onReasoning)
	return nil
}
