package pipeline

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	"github.com/GreenTeodoro839/SimpleAPI/internal/indexes"
	"github.com/GreenTeodoro839/SimpleAPI/internal/modelrewrite"
	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
	"github.com/GreenTeodoro839/SimpleAPI/internal/provider"
	"github.com/GreenTeodoro839/SimpleAPI/internal/translate"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// attemptNonStream calls the upstream and reads the full body. retryable is true
// for connection errors or retryable HTTP statuses (§9). When pair is non-nil
// and the upstream returned 2xx, the response is translated to the client
// protocol before return.
func (h *Handler) attemptNonStream(ctx context.Context, pe *indexes.ProviderEntry, body []byte, retryCodes []int, pair *translate.Pair) (status int, contentType string, respBytes []byte, retryable bool) {
	resp, err := provider.Do(ctx, pe, body)
	if err != nil {
		h.logger.WithError(err).WithField("provider", pe.Name).Debug("upstream non-stream call failed")
		return 0, "", nil, true
	}
	defer resp.Body.Close()
	respBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", nil, true
	}
	if provider.IsRetryableStatus(resp.StatusCode, retryCodes) {
		return resp.StatusCode, resp.Header.Get("Content-Type"), respBytes, true
	}
	if pair != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if translated, terr := pair.Response(respBytes); terr == nil {
			respBytes = translated
		}
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), respBytes, false
}

// streamUsage accumulates input/output tokens parsed from upstream SSE events.
type streamUsage struct {
	input  int64
	output int64
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
// and the accumulated token counts.
func (h *Handler) attemptStream(c *gin.Context, pe *indexes.ProviderEntry, body []byte, aliasB string, rewrite bool, retryCodes []int, pair *translate.Pair, idleTimeout time.Duration) (committed, cleanComplete, retryable bool, inTok, outTok int64) {
	// No total deadline: the upstream call is bound to the client connection so a
	// long-but-active stream is never cut, only a stalled one (idleTimeout).
	resp, err := provider.Do(c.Request.Context(), pe, body)
	if err != nil {
		h.logger.WithError(err).WithField("provider", pe.Name).Debug("upstream stream call failed")
		return false, false, true, 0, 0
	}
	defer resp.Body.Close()
	if provider.IsRetryableStatus(resp.StatusCode, retryCodes) {
		return false, false, true, 0, 0
	}

	var acc streamUsage
	defer func() {
		inTok, outTok = acc.input, acc.output
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
				}
				return
			}
		case <-idleC:
			cleanComplete = false
			h.logger.WithField("provider", pe.Name).Debug("stream idle timeout")
			return
		case <-c.Request.Context().Done():
			cleanComplete = false
			return
		}
	}
}

// accumulateUsage reads token counts out of one upstream SSE data payload.
func accumulateUsage(payload []byte, upstreamType string, acc *streamUsage) {
	switch upstreamType {
	case protocol.Anthropic:
		switch gjson.GetBytes(payload, "type").String() {
		case "message_start":
			if v := gjson.GetBytes(payload, "message.usage.input_tokens"); v.Exists() {
				acc.input = v.Int()
			}
		case "message_delta":
			if v := gjson.GetBytes(payload, "usage.output_tokens"); v.Exists() {
				acc.output = v.Int()
			}
		}
	case protocol.OpenAICompletion:
		if u := gjson.GetBytes(payload, "usage"); u.Exists() {
			acc.input = u.Get("prompt_tokens").Int()
			acc.output = u.Get("completion_tokens").Int()
		}
	case protocol.Codex:
		t := gjson.GetBytes(payload, "type").String()
		if t == "response.completed" || t == "response.incomplete" {
			if u := gjson.GetBytes(payload, "response.usage"); u.Exists() {
				acc.input = u.Get("input_tokens").Int()
				acc.output = u.Get("output_tokens").Int()
			}
		}
	}
}
