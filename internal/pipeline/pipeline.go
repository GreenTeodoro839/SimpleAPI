// Package pipeline orchestrates a single proxied request: protocol/model
// authorization, aliasB→internal-model routing with failover, web-search
// forwarding, outbound model rewrite, passthrough vs translation, payload
// rules, the upstream call, response model rewrite, and usage recording.
// Steps are numbered per DEVELOPMENT.md §8.
package pipeline

import (
	"context"
	"io"
	"net/http"
	"time"

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

	// (6) aliasB lookup
	aliasB := gjson.GetBytes(rawBody, "model").String()
	if aliasB == "" {
		web.WriteError(c, http.StatusBadRequest, "invalid_request",
			"model is required", map[string]interface{}{"field": "model"})
		return
	}
	candidates := kc.Routing[aliasB]
	if len(candidates) == 0 {
		web.WriteError(c, http.StatusNotFound, "model_not_found",
			"model '"+aliasB+"' is not available for this API key", nil)
		return
	}

	// stream detection (read once, before body mutation)
	isStream := gjson.GetBytes(rawBody, "stream").Bool()

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

		if isStream {
			idleTimeout := time.Duration(snap.Config.Server.StreamIdleTimeout()) * time.Second
			_, cleanComplete, retryable, counts := h.attemptStream(c, pe, body, aliasB, rewrite, retryCodes, pair, idleTimeout)
			streamStatus := 0
			if !retryable {
				streamStatus = 200
			}
			h.recordUsage(snap, rm, sourceProto, streamStatus, nil, counts, retryable || !cleanComplete)
			if retryable {
				fo.OnFailure(kc.Name, cand.InternalID)
				h.logger.WithField("model", cand.InternalID).Debug("stream candidate failed; trying next")
				continue
			}
			if cleanComplete {
				fo.OnSuccess(kc.Name, cand.InternalID)
			}
			return // committed (success or mid-stream after commit): cannot retry
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		status, ct, respBytes, retryable := h.attemptNonStream(ctx, pe, body, retryCodes, pair)
		cancel()
		h.recordUsage(snap, rm, sourceProto, status, respBytes, usage.Counts{}, retryable)
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
		return
	}

	// All candidates exhausted.
	web.WriteError(c, http.StatusBadGateway, "no_available_upstream",
		"no upstream candidate succeeded for model '"+aliasB+"'", nil)
}

// recordUsage adds one upstream-attempt row to the in-memory usage aggregate,
// keyed by internal model dimensions (never aliasB). No-op when disabled. For
// streaming attempts body is nil and mined holds the counts parsed from the SSE
// events; for non-stream attempts body is parsed for usage (mined is ignored).
func (h *Handler) recordUsage(snap runtime.Snapshot, rm *indexes.ResolvedModel, sourceProto string, status int, body []byte, mined usage.Counts, failed bool) {
	if !snap.Config.Proxy.UsageEnabled() {
		return
	}
	counts := mined
	if body != nil {
		if node, ok := usageNodeForBody(body, rm.ProviderType); ok && node.Exists() {
			counts = parseUsageNode(node, rm.ProviderType)
		}
	}
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
