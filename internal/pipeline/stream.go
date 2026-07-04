package pipeline

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/GreenTeodoro839/SimpleAPI/internal/indexes"
	"github.com/GreenTeodoro839/SimpleAPI/internal/modelrewrite"
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

// attemptStream calls the upstream and relays the SSE stream. When pair is
// non-nil each upstream chunk is translated; otherwise the stream is
// passthrough-relayed. In both cases per-chunk model rewriting is applied.
//
// Returns:
//   - committed: true once bytes have been written to the client (cannot retry).
//   - cleanComplete: true if the stream finished without an unexpected error.
//   - retryable: true if the failure happened before commit.
func (h *Handler) attemptStream(c *gin.Context, ctx context.Context, pe *indexes.ProviderEntry, body []byte, aliasB string, rewrite bool, retryCodes []int, pair *translate.Pair) (committed, cleanComplete, retryable bool) {
	resp, err := provider.Do(ctx, pe, body)
	if err != nil {
		h.logger.WithError(err).WithField("provider", pe.Name).Debug("upstream stream call failed")
		return false, false, true
	}
	defer resp.Body.Close()
	if provider.IsRetryableStatus(resp.StatusCode, retryCodes) {
		return false, false, true
	}

	reader := bufio.NewReader(resp.Body)
	firstLine, err := reader.ReadBytes('\n')
	if len(firstLine) == 0 && err != nil {
		return false, false, true
	}

	flusher, _ := c.Writer.(http.Flusher)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.WriteHeader(resp.StatusCode)
	if flusher != nil {
		flusher.Flush()
	}

	state := &translate.StreamState{OpenToolBlocks: map[int]bool{}}
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

	// processLine returns false to signal an in-stream translation error.
	processLine := func(line []byte) bool {
		if pair == nil {
			out := line
			if rewrite {
				out = modelrewrite.RewriteSSEFragment(line, aliasB)
			}
			_, _ = c.Writer.Write(out)
			if flusher != nil {
				flusher.Flush()
			}
			return true
		}
		trimmed := bytes.TrimRight(line, "\r\n")
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			return true // ignore event:/blank/comment lines (the transform synthesizes its own)
		}
		rest := trimmed[len("data:"):]
		if len(rest) > 0 && rest[0] == ' ' {
			rest = rest[1:]
		}
		rest = bytes.TrimRight(rest, " \t")
		if len(rest) == 0 {
			return true
		}
		if bytes.Equal(rest, []byte("[DONE]")) {
			if frag, ferr := pair.ResponseStream(nil, state); ferr == nil {
				emit(frag)
			}
			return true
		}
		if !gjson.ValidBytes(rest) {
			return true
		}
		frag, ferr := pair.ResponseStream(rest, state)
		if ferr != nil {
			return false
		}
		emit(frag)
		return true
	}

	cleanComplete = true
	if len(firstLine) > 0 {
		if !processLine(firstLine) {
			cleanComplete = false
		}
	}
	for {
		line, rerr := reader.ReadBytes('\n')
		if len(line) > 0 {
			if !processLine(line) {
				cleanComplete = false
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				cleanComplete = false
			}
			break
		}
	}
	// Flush any terminal events for translation streams on a clean EOF.
	if pair != nil && cleanComplete {
		if frag, ferr := pair.ResponseStream(nil, state); ferr == nil {
			emit(frag)
		}
	}
	return true, cleanComplete, false
}
