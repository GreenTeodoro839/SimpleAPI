package pipeline

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/GreenTeodoro839/SimpleAPI/internal/indexes"
	"github.com/GreenTeodoro839/SimpleAPI/internal/modelrewrite"
	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
	"github.com/GreenTeodoro839/SimpleAPI/internal/provider"
	"github.com/GreenTeodoro839/SimpleAPI/internal/translate"
	"github.com/GreenTeodoro839/SimpleAPI/internal/usage"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// attemptNonStream calls the upstream and reads the full body. retryable is true
// for connection errors or retryable HTTP statuses (§9). When pair is non-nil
// and the upstream returned 2xx, the response is translated to the client
// protocol before return.
func (h *Handler) attemptNonStream(ctx context.Context, pe *indexes.ProviderEntry, body []byte, retryCodes []int, pair *translate.Pair) (status int, contentType string, respBytes []byte, retryable bool, errMsg string) {
	resp, err := provider.Do(ctx, pe, body)
	if err != nil {
		h.logger.WithError(err).WithField("provider", pe.Name).Debug("upstream non-stream call failed")
		return 0, "", nil, true, err.Error()
	}
	defer resp.Body.Close()
	respBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", nil, true, err.Error()
	}
	if provider.IsRetryableStatus(resp.StatusCode, retryCodes) {
		return resp.StatusCode, resp.Header.Get("Content-Type"), respBytes, true, extractErrorMessage(resp.StatusCode, respBytes)
	}
	if resp.StatusCode >= 400 {
		return resp.StatusCode, resp.Header.Get("Content-Type"), respBytes, false, extractErrorMessage(resp.StatusCode, respBytes)
	}
	if pair != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if translated, terr := pair.Response(respBytes); terr == nil {
			respBytes = translated
		}
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), respBytes, false, ""
}

// attemptStream calls the upstream and relays the SSE stream. Unlike non-stream,
// it does NOT apply a total request timeout: it uses the client connection
// lifetime (c.Request.Context()) and an idle timeout — if no upstream data
// arrives for idleTimeout, the stream is aborted. Each upstream data event is
// also mined for token usage, so streaming requests get real counts. When pair
// is non-nil each chunk is translated; otherwise the stream passthrough-relays
// with per-chunk model rewriting.
//
// Returns committed (bytes written → cannot retry), cleanComplete, retryable,
// the upstream HTTP status (0 if the call failed before any response), the
// accumulated token counts, and an error reason string (empty on success).
func (h *Handler) attemptStream(c *gin.Context, pe *indexes.ProviderEntry, body []byte, aliasB string, rewrite bool, retryCodes []int, pair *translate.Pair, idleTimeout time.Duration) (committed, cleanComplete, retryable bool, status int, counts usage.Counts, errMsg string) {
	// No total deadline: the upstream call is bound to the client connection so a
	// long-but-active stream is never cut, only a stalled one (idleTimeout).
	resp, err := provider.Do(c.Request.Context(), pe, body)
	if err != nil {
		h.logger.WithError(err).WithField("provider", pe.Name).Debug("upstream stream call failed")
		return false, false, true, 0, usage.Counts{}, err.Error()
	}
	defer resp.Body.Close()
	status = resp.StatusCode

	if provider.IsRetryableStatus(resp.StatusCode, retryCodes) {
		errBody, _ := io.ReadAll(resp.Body)
		return false, false, true, resp.StatusCode, usage.Counts{}, extractErrorMessage(resp.StatusCode, errBody)
	}
	// Non-2xx non-retryable (e.g. 4xx): the upstream returned an error body
	// rather than an SSE stream. Forward it to the client and capture the reason.
	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(resp.Body)
		c.Writer.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		c.Writer.WriteHeader(resp.StatusCode)
		_, _ = c.Writer.Write(errBody)
		if flusher, ok := c.Writer.(http.Flusher); ok {
			flusher.Flush()
		}
		committed = true
		cleanComplete = true
		errMsg = extractErrorMessage(resp.StatusCode, errBody)
		return
	}

	var acc usage.Counts
	defer func() {
		counts = acc
		if !committed {
			retryable = true
		}
	}()

	reader := bufio.NewReader(resp.Body)
	state := &translate.StreamState{OpenToolBlocks: map[int]bool{}}
	flusher, _ := c.Writer.(http.Flusher)

	emit := func(frag []byte) {
		if len(frag) == 0 {
			return
		}
		if rewrite {
			frag = modelrewrite.RewriteSSEFragment(frag, aliasB)
		}
		_, _ = c.Writer.Write(frag)
		if flusher != nil {
			flusher.Flush()
		}
	}

	processLine := func(line []byte) {
		trimmed := bytes.TrimRight(line, "\r\n")
		var payload []byte
		if bytes.HasPrefix(trimmed, []byte("data:")) {
			rest := trimmed[len("data:"):]
			if len(rest) > 0 && rest[0] == ' ' {
				rest = rest[1:]
			}
			payload = bytes.TrimRight(rest, " \t")
		}
		if pair == nil {
			// passthrough: rewrite model fields, then forward the line verbatim
			out := line
			if rewrite {
				out = modelrewrite.RewriteSSEFragment(line, aliasB)
			}
			_, _ = c.Writer.Write(out)
			if flusher != nil {
				flusher.Flush()
			}
			if len(payload) > 0 && gjson.ValidBytes(payload) {
				accumulateUsage(payload, pe.Type, &acc)
			}
			return
		}
		// translation: ignore non-data lines (the transform synthesizes its own)
		if payload == nil || bytes.Equal(payload, []byte("[DONE]")) {
			if payload != nil { // [DONE] terminal
				if frag, ferr := pair.ResponseStream(nil, state); ferr == nil {
					emit(frag)
				}
			}
			return
		}
		if !gjson.ValidBytes(payload) {
			return
		}
		accumulateUsage(payload, pe.Type, &acc)
		if frag, ferr := pair.ResponseStream(payload, state); ferr == nil {
			emit(frag)
		}
	}

	// One long-lived reader goroutine feeds lines; the main loop selects on it,
	// the idle timer, and client disconnect. The channel is buffered so the
	// goroutine never blocks when the main loop has moved on; resp.Body.Close()
	// (deferred) unblocks its final read.
	type lineSig struct {
		line []byte
		err  error
	}
	ch := make(chan lineSig, 1)
	go func() {
		for {
			line, err := reader.ReadBytes('\n')
			ch <- lineSig{line, err}
			if err != nil {
				return
			}
		}
	}()

	var idle *time.Timer
	if idleTimeout > 0 {
		idle = time.NewTimer(idleTimeout)
		defer idle.Stop()
	}

	cleanComplete = true
	for {
		var idleC <-chan time.Time
		if idle != nil {
			idleC = idle.C
		}
		select {
		case sig := <-ch:
			if len(sig.line) > 0 {
				if !committed {
					c.Writer.Header().Set("Content-Type", "text/event-stream")
					c.Writer.WriteHeader(resp.StatusCode)
					if flusher != nil {
						flusher.Flush()
					}
					committed = true
				}
				processLine(sig.line)
				if idle != nil {
					idle.Reset(idleTimeout)
				}
			}
			if sig.err != nil {
				if sig.err != io.EOF {
					cleanComplete = false
					errMsg = sig.err.Error()
				}
				return
			}
		case <-idleC:
			cleanComplete = false
			errMsg = "stream idle timeout"
			h.logger.WithField("provider", pe.Name).Debug("stream idle timeout")
			return
		case <-c.Request.Context().Done():
			cleanComplete = false
			errMsg = "client disconnected"
			return
		}
	}
}

// accumulateUsage reads token counts out of one upstream SSE data payload into
// acc, keeping the maximum seen per counter.
//
// Providers disagree on where the real counts live, so we take the maximum
// value seen for each counter across the relevant events:
//   - Standard Anthropic / DeepSeek: message_start carries the real
//     input_tokens; message_delta carries the final output_tokens.
//   - GLM: message_start.usage is all zeros (a placeholder); the real
//     input_tokens AND output_tokens both arrive in the final message_delta.
//
// Taking the max covers both shapes (and providers that emit running counts):
// the placeholder 0 never overrides a real value, and the largest seen count is
// the final one.
func accumulateUsage(payload []byte, upstreamType string, acc *usage.Counts) {
	node, ok := usageNodeForEvent(payload, upstreamType)
	if !ok || !node.Exists() {
		return
	}
	c := parseUsageNode(node, upstreamType)
	if c.Input > acc.Input {
		acc.Input = c.Input
	}
	if c.Output > acc.Output {
		acc.Output = c.Output
	}
	if c.CacheRead > acc.CacheRead {
		acc.CacheRead = c.CacheRead
	}
	if c.CacheCreation > acc.CacheCreation {
		acc.CacheCreation = c.CacheCreation
	}
	if c.Cached > acc.Cached {
		acc.Cached = c.Cached
	}
	if c.Reasoning > acc.Reasoning {
		acc.Reasoning = c.Reasoning
	}
	if c.Total > acc.Total {
		acc.Total = c.Total
	}
}

// usageNodeForEvent locates the usage JSON object in one SSE data payload for the
// given upstream protocol. For Anthropic the location depends on the event type
// (message_start nests it under message.usage; message_delta has it top-level).
// Returns ok=false when the payload carries no usage (e.g. a content delta).
func usageNodeForEvent(payload []byte, upstreamType string) (gjson.Result, bool) {
	switch upstreamType {
	case protocol.Anthropic:
		switch gjson.GetBytes(payload, "type").String() {
		case "message_start":
			return gjson.GetBytes(payload, "message.usage"), true
		case "message_delta":
			return gjson.GetBytes(payload, "usage"), true
		}
		return gjson.Result{}, false
	case protocol.OpenAICompletion:
		return gjson.GetBytes(payload, "usage"), true
	case protocol.Codex:
		t := gjson.GetBytes(payload, "type").String()
		if t != "response.completed" && t != "response.incomplete" {
			return gjson.Result{}, false
		}
		return gjson.GetBytes(payload, "response.usage"), true
	}
	return gjson.Result{}, false
}

// usageNodeForBody locates the usage JSON object in a full (non-streaming)
// response body for the given upstream protocol.
func usageNodeForBody(body []byte, upstreamType string) (gjson.Result, bool) {
	if upstreamType == protocol.Codex {
		return gjson.GetBytes(body, "response.usage"), true
	}
	return gjson.GetBytes(body, "usage"), true
}

// parseUsageNode extracts token counters from a usage JSON object for the given
// protocol. Mirrors CLIProxyAPI's usage parsers: Anthropic reports
// cache_read_input_tokens / cache_creation_input_tokens; OpenAI/codex report
// prompt_tokens_details.cached_tokens. Missing fields are treated as 0; either
// of a provider's two names for the same counter is accepted.
func parseUsageNode(u gjson.Result, upstreamType string) usage.Counts {
	var c usage.Counts
	if !u.Exists() {
		return c
	}
	switch upstreamType {
	case protocol.Anthropic:
		c.Input = u.Get("input_tokens").Int()
		c.Output = u.Get("output_tokens").Int()
		c.CacheRead = u.Get("cache_read_input_tokens").Int()
		c.CacheCreation = u.Get("cache_creation_input_tokens").Int()
		// Anthropic reports no total; finalizeCounts derives it from the others.
	case protocol.OpenAICompletion:
		c.Input = pick(u.Get("prompt_tokens"), u.Get("input_tokens"))
		c.Output = pick(u.Get("completion_tokens"), u.Get("output_tokens"))
		c.Cached = pick(u.Get("prompt_tokens_details.cached_tokens"), u.Get("input_tokens_details.cached_tokens"))
		c.Reasoning = pick(u.Get("completion_tokens_details.reasoning_tokens"), u.Get("output_tokens_details.reasoning_tokens"))
		c.Total = u.Get("total_tokens").Int()
	case protocol.Codex:
		c.Input = pick(u.Get("input_tokens"), u.Get("prompt_tokens"))
		c.Output = pick(u.Get("output_tokens"), u.Get("completion_tokens"))
		c.Cached = pick(u.Get("input_tokens_details.cached_tokens"), u.Get("prompt_tokens_details.cached_tokens"))
		c.Reasoning = pick(u.Get("output_tokens_details.reasoning_tokens"), u.Get("completion_tokens_details.reasoning_tokens"))
		c.Total = u.Get("total_tokens").Int()
	}
	return c
}

// finalizeCounts fills Total when the upstream did not report one (Anthropic
// never does; some OpenAI/codex responses omit it). For Anthropic the total is
// input+output+cache*; for OpenAI/codex it falls back to input+output. Cached
// (an OpenAI subset of input) and reasoning (a subset of output) are not added
// back in, to avoid double counting.
func finalizeCounts(c *usage.Counts, upstreamType string) {
	if c.Total > 0 {
		return
	}
	if upstreamType == protocol.Anthropic {
		c.Total = c.Input + c.Output + c.CacheRead + c.CacheCreation
	} else {
		c.Total = c.Input + c.Output
	}
}

// pick returns the first non-zero value among the given results (treating a
// missing or zero field as "absent"), or 0 if none is non-zero. This lets us
// accept either name a provider uses (e.g. prompt_tokens vs input_tokens).
func pick(vals ...gjson.Result) int64 {
	for _, v := range vals {
		if v.Exists() && v.Int() != 0 {
			return v.Int()
		}
	}
	return 0
}

// extractErrorMessage pulls a concise human-readable reason out of an upstream
// error body. It tries the common JSON error shapes ({error:{message}},
// {message}, {error:"..."}, {detail}, {msg}) before falling back to a
// truncated raw body, then to "upstream returned HTTP <status>".
func extractErrorMessage(status int, body []byte) string {
	if len(body) > 0 && gjson.ValidBytes(body) {
		for _, p := range []string{"error.message", "message", "error", "detail", "msg"} {
			if v := gjson.GetBytes(body, p); v.Exists() && v.Type == gjson.String {
				return truncateMessage(v.String())
			}
		}
	}
	if s := strings.TrimSpace(string(body)); s != "" {
		return truncateMessage(s)
	}
	if status > 0 {
		return fmt.Sprintf("upstream returned HTTP %d", status)
	}
	return "upstream call failed"
}

func truncateMessage(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		return s[:500] + "..."
	}
	return s
}
