// Package pipeline orchestrates a single proxied request: protocol/model
// authorization, aliasB→internal-model routing with failover, web-search
// forwarding, outbound model rewrite, passthrough vs translation, payload
// rules, the upstream call, response model rewrite, and usage recording.
// Steps are numbered per DEVELOPMENT.md §8.
package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/GreenTeodoro839/SimpleAPI/internal/archive"
	"github.com/GreenTeodoro839/SimpleAPI/internal/calllog"
	"github.com/GreenTeodoro839/SimpleAPI/internal/indexes"
	"github.com/GreenTeodoro839/SimpleAPI/internal/modelrewrite"
	"github.com/GreenTeodoro839/SimpleAPI/internal/payload"
	"github.com/GreenTeodoro839/SimpleAPI/internal/protocol"
	"github.com/GreenTeodoro839/SimpleAPI/internal/reqctx"
	"github.com/GreenTeodoro839/SimpleAPI/internal/runtime"
	"github.com/GreenTeodoro839/SimpleAPI/internal/translate"
	"github.com/GreenTeodoro839/SimpleAPI/internal/usage"
	"github.com/GreenTeodoro839/SimpleAPI/internal/web"
	"github.com/GreenTeodoro839/SimpleAPI/internal/websearch"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Handler serves the three proxy routes and /v1/models.
type Handler struct {
	rt     *runtime.Runtime
	logger *logrus.Logger
}

func NewHandler(rt *runtime.Runtime, logger *logrus.Logger) *Handler {
	return &Handler{rt: rt, logger: logger}
}

// reqMeta carries per-client-request context into recordUsage for call-log
// entries. One is created at the start of ServeProxy and shared across failover
// attempts (same RequestID) so a single client call can be correlated even when
// it retries across candidates.
type reqMeta struct {
	requestID  string
	endpoint   string // "POST /v1/messages"
	apiKeyName string
	aliasB     string
	start      time.Time
}

// statusServerBusy is Anthropic's non-standard 529 "Overloaded". Go has no
// http.Status constant for it.
const statusServerBusy = 529

var reqCounter uint64

func newRequestID() string {
	return fmt.Sprintf("req-%d", atomic.AddUint64(&reqCounter, 1))
}

// ServeModels handles GET /v1/models: the aliasB list visible to this key.
func (h *Handler) ServeModels(c *gin.Context) {
	kc := reqctx.KeyContext(c)
	data := make([]gin.H, 0, len(kc.AliasBs))
	for _, a := range kc.AliasBs {
		data = append(data, gin.H{"id": a, "object": "model"})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

// ServeProxy handles POST /v1/messages, /v1/chat/completions, /v1/responses.
func (h *Handler) ServeProxy(c *gin.Context) {
	snap := h.rt.Snapshot()
	kc := reqctx.KeyContext(c)
	sourceProto := reqctx.Protocol(c)

	// (4) protocol authorization
	if _, allowed := kc.AllowedProtocols[sourceProto]; !allowed {
		web.WriteError(c, http.StatusForbidden, "protocol_not_allowed",
			"this API key is not allowed to use protocol: "+sourceProto, nil)
		return
	}

	// (5) read body once
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		web.WriteError(c, http.StatusBadRequest, "invalid_request", "could not read request body", nil)
		return
	}

	// (6) aliasB lookup: exact match wins; otherwise the first wildcard aliasB
	// (containing '*', in config declaration order) whose glob matches. aliasB
	// stays the client-sent model string for all downstream rewrite/log usage.
	aliasB := gjson.GetBytes(rawBody, "model").String()
	if aliasB == "" {
		web.WriteError(c, http.StatusBadRequest, "invalid_request",
			"model is required", map[string]interface{}{"field": "model"})
		return
	}
	candidates := kc.ResolveCandidates(aliasB)
	if len(candidates) == 0 {
		web.WriteError(c, http.StatusNotFound, "model_not_found",
			"model '"+aliasB+"' is not available for this API key", nil)
		return
	}

	// stream detection (read once, before body mutation)
	isStream := gjson.GetBytes(rawBody, "stream").Bool()

	meta := reqMeta{
		requestID:  newRequestID(),
		endpoint:   c.Request.Method + " " + c.Request.URL.Path,
		apiKeyName: kc.Name,
		aliasB:     aliasB,
		start:      time.Now(),
	}

	// Does any candidate speak the client's protocol or have a translator?
	anyUsable := false
	for _, cand := range candidates {
		rm := snap.Indexes.Models[cand.InternalID]
		if rm == nil {
			continue
		}
		if rm.ProviderType == sourceProto {
			anyUsable = true
			break
		}
		if _, ok := translate.Get(translate.Protocol(sourceProto), translate.Protocol(rm.ProviderType)); ok {
			anyUsable = true
			break
		}
	}
	if !anyUsable {
		web.WriteError(c, http.StatusNotImplemented, "translation_not_supported",
			"no same-protocol candidate or translator available for model '"+aliasB+"'", nil)
		return
	}

	// (7)+(9)+(12)+(13)+(14) candidate loop with failover.
	maxFail := snap.Config.Proxy.MaxFailures()
	resetSec := snap.Config.Proxy.FailureReset()
	rewrite := snap.Config.Proxy.RewriteModel()
	retryCodes := snap.Config.Proxy.RetryCodes()
	timeout := time.Duration(snap.Config.Server.RequestTimeout()) * time.Second
	fo := h.rt.Failover()
	lim := h.rt.Limiter()
	// Request-archive sink: when request_archive.dir is set (non-empty) the
	// streaming path accumulates the RAW UPSTREAM lines — the remote's original
	// response, before translation or model rewriting. Shared across failover
	// attempts, which is safe because a retryable attempt never commits a
	// single byte (stream.go forces retryable when nothing was committed). nil
	// when archiving is off keeps the hot path allocation-free.
	var archiveSink *bytes.Buffer
	if snap.Config.RequestArchive.Directory() != "" {
		archiveSink = &bytes.Buffer{}
	}
	// Set when a candidate was skipped only because its provider was at its
	// concurrency limit. It outranks a plain upstream failure in the final
	// error: "busy, retry" is the useful thing to tell the client.
	busy := false

	for _, cand := range candidates {
		if fo.ShouldSkip(kc.Name, cand.InternalID, maxFail, resetSec) {
			continue
		}
		rm := snap.Indexes.Models[cand.InternalID]
		pe := snap.Indexes.Providers[rm.ProviderName]
		if rm == nil || pe == nil {
			continue
		}

		// (8) anthropic web_search forwarding: reroute to the configured target.
		// Body is unchanged; only the selected model/provider changes.
		if sourceProto == protocol.Anthropic {
			if tgt := websearch.ResolveForward(rm, rawBody, snap.Indexes.Models); tgt != nil {
				h.logger.WithField("from", rm.InternalID).WithField("to", tgt.InternalID).Debug("web_search forward")
				rm = tgt
				pe = snap.Indexes.Providers[rm.ProviderName]
			}
		}

		// (10) passthrough vs translation: resolve a translator for cross-protocol.
		var pair *translate.Pair
		if rm.ProviderType != sourceProto {
			p, ok := translate.Get(translate.Protocol(sourceProto), translate.Protocol(rm.ProviderType))
			if !ok {
				continue // no translator registered for this direction
			}
			pair = p
		}

		// (9) outbound model rewrite for this candidate.
		body, _ := sjson.SetBytes(rawBody, "model", rm.UpstreamModel)
		// (10 cont.) translate the request body to the upstream protocol.
		if pair != nil {
			tb, terr := pair.Request(body)
			if terr != nil {
				web.WriteError(c, http.StatusBadRequest, "translation_not_supported",
					"request translation failed: "+terr.Error(), nil)
				return
			}
			body = tb
		}
		// (11) payload rules (§11): default → default-raw → override → override-raw → filter.
		engine := payload.NewEngine(&snap.Config.Payload)
		body = engine.Apply(body, payload.MatchContext{
			InternalID:    rm.InternalID,
			AliasA:        rm.AliasA,
			UpstreamModel: rm.UpstreamModel,
			Protocol:      rm.ProviderType,
			FromProtocol:  sourceProto,
			Headers:       c.Request.Header,
		})
		h.logger.Debugf("outbound body [%s]: %s", rm.InternalID, string(body))

		// (12) provider concurrency cap. A saturated provider is skipped like a
		// missing translator, not recorded as a failure: being busy does not
		// mean it is broken, so it must not trip the failover counter.
		release, gotSlot := lim.Acquire(pe.Name, pe.MaxConcurrency)
		if !gotSlot {
			busy = true
			h.logger.WithField("provider", pe.Name).WithField("model", cand.InternalID).
				Debug("provider at concurrency limit; trying next candidate")
			continue
		}

		if isStream {
			idleTimeout := time.Duration(snap.Config.Server.StreamIdleTimeout()) * time.Second
			_, cleanComplete, retryable, upStatus, counts, errMsg := h.attemptStream(c, pe, body, aliasB, rewrite, retryCodes, pair, idleTimeout, archiveSink)
			release()
			finalCounts := h.recordUsage(snap, rm, sourceProto, upStatus, nil, counts, retryable || !cleanComplete, meta, errMsg)
			if retryable {
				fo.OnFailure(kc.Name, cand.InternalID)
				h.logger.WithField("model", cand.InternalID).Debug("stream candidate failed; trying next")
				continue
			}
			if cleanComplete {
				fo.OnSuccess(kc.Name, cand.InternalID)
			}
			// Archive only a 2xx committed stream: reaching here with
			// retryable=false implies committed (stream.go forces retryable
			// when nothing was committed). The >=400 relay is a relayed
			// failure — no file. A 2xx stream that broke midway (client
			// disconnect, read error, idle timeout) still archives the partial
			// content the client received.
			if upStatus >= 200 && upStatus < 300 {
				h.archiveRequest(snap, meta, rm, finalCounts, rawBody, nil, archiveSink)
			}
			return // committed (success or mid-stream after commit): cannot retry
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		status, ct, respBytes, rawUpstream, retryable, errMsg := h.attemptNonStream(ctx, pe, body, retryCodes, pair)
		cancel()
		release()
		finalCounts := h.recordUsage(snap, rm, sourceProto, status, respBytes, usage.Counts{}, retryable, meta, errMsg)
		if retryable {
			fo.OnFailure(kc.Name, cand.InternalID)
			h.logger.WithField("model", cand.InternalID).Debug("non-stream candidate failed; trying next")
			continue
		}
		if status >= 200 && status < 300 {
			fo.OnSuccess(kc.Name, cand.InternalID)
		}
		if rewrite {
			respBytes = modelrewrite.NonStream(respBytes, aliasB)
		}
		if ct == "" {
			ct = "application/json"
		}
		c.Data(status, ct, respBytes)
		// Archive the remote's original response (pre-translation,
		// pre-model-rewrite), only after the 2xx body reached the client.
		if status >= 200 && status < 300 {
			h.archiveRequest(snap, meta, rm, finalCounts, rawBody, rawUpstream, nil)
		}
		return
	}

	// All candidates exhausted.
	if busy {
		// 529 is what Anthropic clients read as "overloaded, back off and
		// retry", which is exactly the intent of a provider concurrency cap.
		h.logger.WithField("alias", aliasB).WithField("api_key", kc.Name).
			Warn("all candidate providers at their concurrency limit")
		web.WriteError(c, statusServerBusy, "server_busy",
			"all upstream providers for model '"+aliasB+"' are at their concurrency limit", nil)
		return
	}
	web.WriteError(c, http.StatusBadGateway, "no_available_upstream",
		"no upstream candidate succeeded for model '"+aliasB+"'", nil)
}

// archiveRequest enqueues the conversation for on-disk archiving: the user's
// original request body plus the remote's original response. Called exactly
// once per client request, only after a successful (2xx) response was
// committed to the client: response is the upstream's exact non-stream body —
// pre-translation, pre-model-rewrite (responseStream nil) — or responseStream
// holds the accumulated raw upstream SSE text (response nil). It self-checks
// request_archive.dir — a request that started with archiving off stays off
// for its whole lifetime, even across reloads — and never blocks; the writer
// drops on a full queue or write error. The byte slices are retained by the
// writer goroutine after ServeProxy returns, which is safe because nothing
// mutates them afterwards.
func (h *Handler) archiveRequest(snap runtime.Snapshot, meta reqMeta, rm *indexes.ResolvedModel, counts usage.Counts, request, response []byte, responseStream *bytes.Buffer) {
	dir := snap.Config.RequestArchive.Directory()
	if dir == "" {
		return
	}
	rec := archive.Record{
		Dir:           dir,
		Timestamp:     meta.start,
		RequestID:     meta.requestID,
		APIKey:        meta.apiKeyName,
		Model:         meta.aliasB,
		InternalModel: rm.InternalID,
		ProviderType:  rm.ProviderType,
		Tokens: archive.Tokens{
			InputTokens:         counts.Input,
			OutputTokens:        counts.Output,
			CacheReadTokens:     counts.CacheRead,
			CacheCreationTokens: counts.CacheCreation,
			CachedTokens:        counts.Cached,
			ReasoningTokens:     counts.Reasoning,
			TotalTokens:         counts.Total,
		},
		Request: json.RawMessage(request),
	}
	if responseStream != nil {
		s := responseStream.String() // one copy per archived stream; keeps the record self-owned
		rec.ResponseStream = &s
	} else {
		// An empty body (204 No Content, or a 200 with no payload) archives as
		// JSON null: an empty non-nil json.RawMessage instead fails MarshalIndent
		// ("unexpected end of JSON input") and drops the whole record.
		if len(response) == 0 {
			response = nil
		}
		rec.Response = json.RawMessage(response)
	}
	h.rt.Archive().Record(rec)
}

// recordUsage records one upstream attempt: into the usage aggregate (when
// enabled) and into the call-log ring buffer (when enabled). The two are
// independent. For streaming attempts body is nil and mined holds the counts
// parsed from SSE events; for non-stream attempts body is parsed for usage.
// It returns the finalized counts — the same numbers the call-log entry
// records — so callers such as the request archive reuse identical values.
func (h *Handler) recordUsage(snap runtime.Snapshot, rm *indexes.ResolvedModel, sourceProto string, status int, body []byte, mined usage.Counts, failed bool, meta reqMeta, errMsg string) usage.Counts {
	counts := mined
	if body != nil {
		if node, ok := usageNodeForBody(body, rm.ProviderType); ok && node.Exists() {
			counts = parseUsageNode(node, rm.ProviderType)
		}
	}
	finalizeCounts(&counts, rm.ProviderType)

	if snap.Config.Proxy.UsageEnabled() {
		h.rt.Usage().Record(usage.Key{
			Provider:           rm.ProviderName,
			ProviderType:       rm.ProviderType,
			AliasA:             rm.AliasA,
			UpstreamModel:      rm.UpstreamModel,
			InternalModelID:    rm.InternalID,
			SourceProtocol:     sourceProto,
			TargetProviderType: rm.ProviderType,
			HTTPStatus:         status,
		}, counts, failed)
	}

	if cl := h.rt.CallLog(); cl.Enabled() {
		cl.Record(calllog.Entry{
			RequestID:      meta.requestID,
			Timestamp:      meta.start,
			Endpoint:       meta.endpoint,
			APIKey:         meta.apiKeyName,
			SourceProtocol: sourceProto,
			Alias:          meta.aliasB,
			Provider:       rm.ProviderName,
			ProviderType:   rm.ProviderType,
			Model:          rm.UpstreamModel,
			InternalModel:  rm.InternalID,
			HTTPStatus:     status,
			LatencyMS:      time.Since(meta.start).Milliseconds(),
			Failed:         failed || errMsg != "",
			Error:          errMsg,
			Tokens: calllog.Tokens{
				InputTokens:         counts.Input,
				OutputTokens:        counts.Output,
				CacheReadTokens:     counts.CacheRead,
				CacheCreationTokens: counts.CacheCreation,
				CachedTokens:        counts.Cached,
				ReasoningTokens:     counts.Reasoning,
				TotalTokens:         counts.Total,
			},
		})
	}
	return counts
}
