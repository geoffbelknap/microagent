package hostworker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Mode string

const (
	// ModeForward splices bytes without interpreting requests or making policy decisions.
	ModeForward     Mode = "forward"
	ModePassthrough Mode = "passthrough"
	ModeLocalAllow  Mode = "local-allow"
	ModePolicy      Mode = "policy"
)

const (
	DefaultCapability      = "model.openai"
	defaultMaxRequestBytes = 32 << 20
	defaultUpstreamTimeout = 180 * time.Second
	defaultPolicyTimeout   = 2 * time.Second
	requestIDHeader        = "X-Microagent-Mediation-Request-ID"
	defaultListenHost      = "127.0.0.1"
	decisionAllow          = "allow"
	decisionDeny           = "deny"
	decisionError          = "error"
)

var hopByHopHeaders = map[string]struct{}{
	"connection":          {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

type Options struct {
	TargetBaseURL   string
	BindHost        string
	BindPort        int
	Mode            Mode
	PolicyURL       string
	PolicyFile      string
	PolicyTimeout   time.Duration
	WorkspaceID     string
	Capability      string
	WorkerID        string
	UpstreamTimeout time.Duration
	MaxRequestBytes int64
	Logger          Logger
	Ready           io.Writer
	// ResolveUpstreamHost, when set, returns the current "host:port" of the
	// mediated worker. It is consulted before every proxied request so a worker
	// restart — which moves the worker to a new port — does not strand the
	// workspaces mediated to it. Returning "" keeps the start-time target.
	ResolveUpstreamHost func() string
}

type Logger interface {
	Log(event string, fields map[string]any)
}

type NopLogger struct{}

func (NopLogger) Log(string, map[string]any) {}

type Handler struct {
	targetBaseURL    *url.URL
	targetBasePath   string
	resolveUpstream  func() string
	upstreamMu       sync.Mutex
	upstreamHost     string
	mode             Mode
	policyURL        *url.URL
	filePolicy       *FilePolicy
	filePolicySource policyFileSource
	policyTimeout    time.Duration
	workspaceID      string
	capability       string
	workerID         string
	upstreamTimeout  time.Duration
	maxRequestBytes  int64
	logger           Logger
	client           *http.Client
}

type DecisionEnvelope struct {
	SchemaVersion int               `json:"schema_version"`
	RequestID     string            `json:"request_id"`
	Workspace     DecisionWorkspace `json:"workspace"`
	Capability    string            `json:"capability"`
	Worker        DecisionWorker    `json:"worker"`
	Request       DecisionRequest   `json:"request"`
	Limits        DecisionLimits    `json:"limits"`
	DeadlineEpoch float64           `json:"deadline_epoch"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type DecisionWorkspace struct {
	ID string `json:"id"`
}

type DecisionWorker struct {
	ID             string `json:"id"`
	Protocol       string `json:"protocol"`
	TargetBasePath string `json:"target_base_path"`
}

type DecisionRequest struct {
	Method     string               `json:"method"`
	Path       string               `json:"path"`
	Query      string               `json:"query,omitempty"`
	Upstream   string               `json:"upstream_path"`
	Bytes      int64                `json:"bytes"`
	BodySHA256 string               `json:"body_sha256"`
	Body       *DecisionRequestBody `json:"body,omitempty"`
}

type DecisionLimits struct {
	UpstreamTimeoutSeconds float64 `json:"upstream_timeout_seconds"`
	PolicyTimeoutSeconds   float64 `json:"policy_timeout_seconds"`
}

type decisionResult struct {
	Decision     string
	Reason       string
	HTTPStatus   int
	AuditEventID string
	Error        string
	PolicySource string
	PolicyRuleID string
	PolicySHA256 string
}

type policyResponse struct {
	Decision     string `json:"decision"`
	Result       string `json:"result"`
	Reason       string `json:"reason"`
	AuditEventID string `json:"audit_event_id"`
}

func NewHandler(opts Options) (*Handler, error) {
	mode := opts.Mode
	if mode == "" {
		mode = ModePassthrough
	}
	switch mode {
	case ModePassthrough, ModeLocalAllow, ModePolicy:
	default:
		return nil, fmt.Errorf("unsupported mediation mode %q", mode)
	}
	target, err := parseEndpointURL(opts.TargetBaseURL, "target")
	if err != nil {
		return nil, err
	}
	var policy *url.URL
	if strings.TrimSpace(opts.PolicyURL) != "" {
		policy, err = parseEndpointURL(opts.PolicyURL, "policy")
		if err != nil {
			return nil, err
		}
	}
	var filePolicy *FilePolicy
	var filePolicySource policyFileSource
	if strings.TrimSpace(opts.PolicyFile) != "" {
		filePolicy, filePolicySource, err = LoadFilePolicy(opts.PolicyFile)
		if err != nil {
			return nil, err
		}
	}
	if mode == ModePolicy && policy != nil && filePolicy != nil {
		return nil, fmt.Errorf("policy URL and policy file are mutually exclusive")
	}
	if mode == ModePolicy && policy == nil && filePolicy == nil {
		return nil, fmt.Errorf("policy URL or policy file is required for policy mediation")
	}
	policyTimeout := opts.PolicyTimeout
	if policyTimeout <= 0 {
		policyTimeout = defaultPolicyTimeout
	}
	upstreamTimeout := opts.UpstreamTimeout
	if upstreamTimeout <= 0 {
		upstreamTimeout = defaultUpstreamTimeout
	}
	maxRequestBytes := opts.MaxRequestBytes
	if maxRequestBytes <= 0 {
		maxRequestBytes = defaultMaxRequestBytes
	}
	logger := opts.Logger
	if logger == nil {
		logger = NopLogger{}
	}
	capability := strings.TrimSpace(opts.Capability)
	if capability == "" {
		capability = DefaultCapability
	}
	workerID := strings.TrimSpace(opts.WorkerID)
	if workerID == "" {
		workerID = target.Host + normalizedBasePath(target)
	}
	return &Handler{
		targetBaseURL:    target,
		targetBasePath:   normalizedBasePath(target),
		resolveUpstream:  opts.ResolveUpstreamHost,
		upstreamHost:     target.Host,
		mode:             mode,
		policyURL:        policy,
		filePolicy:       filePolicy,
		filePolicySource: filePolicySource,
		policyTimeout:    policyTimeout,
		workspaceID:      strings.TrimSpace(opts.WorkspaceID),
		capability:       capability,
		workerID:         workerID,
		upstreamTimeout:  upstreamTimeout,
		maxRequestBytes:  maxRequestBytes,
		logger:           logger,
		client:           &http.Client{Timeout: upstreamTimeout},
	}, nil
}

func Run(ctx context.Context, opts Options) error {
	if opts.Mode == ModeForward {
		return runForward(ctx, opts)
	}
	host := strings.TrimSpace(opts.BindHost)
	if host == "" {
		host = defaultListenHost
	}
	handler, err := NewHandler(opts)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, opts.BindPort))
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
	}()
	server := &http.Server{Handler: handler}
	handler.logger.Log("mediator_start", map[string]any{
		"listen_host":      host,
		"listen_port":      listener.Addr().(*net.TCPAddr).Port,
		"mediation_mode":   handler.mode,
		"target_base_url":  handler.targetBaseURL.String(),
		"target_base_path": handler.targetBasePath,
		"worker_id":        handler.workerID,
		"workspace_id":     handler.workspaceID,
		"policy_source":    handler.policySource(),
		"policy_file":      handler.filePolicySource.Path,
		"policy_sha256":    handler.filePolicySource.SHA256,
	})
	if opts.Ready != nil {
		fmt.Fprintf(opts.Ready, "ready %s:%d\n", host, listener.Addr().(*net.TCPAddr).Port)
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		handler.logger.Log("mediator_stop", map[string]any{})
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK\n"))
		return
	}
	start := time.Now()
	requestID := strings.TrimSpace(r.Header.Get(requestIDHeader))
	if requestID == "" {
		requestID = newRequestID()
	}
	h.log("request_accept", map[string]any{
		"request_id": requestID,
		"method":     r.Method,
		"path":       r.URL.Path,
		"query":      r.URL.RawQuery,
	})
	body, err := readRequestBody(r, h.maxRequestBytes)
	if err != nil {
		h.writeError(w, requestID, http.StatusRequestEntityTooLarge, "request too large", err.Error())
		return
	}
	requestBytes := int64(len(body))
	bodySHA := sha256Hex(body)
	bodyMeta := InspectDecisionRequestBody(r.Header.Get("Content-Type"), body)
	upstreamPath := h.upstreamPath(r.URL)
	h.log("request_body_read", map[string]any{
		"request_id":          requestID,
		"method":              r.Method,
		"path":                r.URL.Path,
		"request_bytes":       requestBytes,
		"request_body_sha256": bodySHA,
		"elapsed_ms":          elapsedMS(start),
	})
	decision := h.evaluateDecision(r.Context(), requestID, r, upstreamPath, requestBytes, bodySHA, bodyMeta, start)
	if decision.Decision != decisionAllow {
		status := http.StatusServiceUnavailable
		if decision.Decision == decisionDeny {
			status = http.StatusForbidden
		}
		payload := map[string]any{
			"error": map[string]any{
				"message":    "mediation denied",
				"reason":     decision.Reason,
				"request_id": requestID,
			},
		}
		data, _ := json.Marshal(payload)
		data = append(data, '\n')
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.Header().Set("Connection", "close")
		w.Header().Set(requestIDHeader, requestID)
		w.WriteHeader(status)
		_, _ = w.Write(data)
		h.log("request_denied", map[string]any{
			"request_id":               requestID,
			"method":                   r.Method,
			"path":                     r.URL.Path,
			"status":                   status,
			"mediation_mode":           h.mode,
			"mediation_result":         decision.Decision,
			"mediation_reason":         decision.Reason,
			"mediation_policy_status":  decision.HTTPStatus,
			"mediation_policy_source":  decision.PolicySource,
			"mediation_policy_rule_id": decision.PolicyRuleID,
			"mediation_policy_sha256":  decision.PolicySHA256,
			"audit_event_id":           decision.AuditEventID,
			"duration_ms":              elapsedMS(start),
		})
		h.logRequestEnd(requestID, r, requestBytes, bodySHA, int64(len(data)), status, decision, start)
		return
	}
	h.proxyUpstream(w, r, body, requestID, upstreamPath, requestBytes, bodySHA, decision, start)
}

func (h *Handler) evaluateDecision(ctx context.Context, requestID string, r *http.Request, upstreamPath string, requestBytes int64, bodySHA string, bodyMeta *DecisionRequestBody, start time.Time) decisionResult {
	if h.mode == ModePassthrough {
		h.log("mediation_bypass", map[string]any{
			"request_id":       requestID,
			"method":           r.Method,
			"path":             r.URL.Path,
			"mediation_mode":   h.mode,
			"mediation_result": decisionAllow,
			"elapsed_ms":       elapsedMS(start),
		})
		return decisionResult{Decision: decisionAllow, Reason: "passthrough"}
	}
	envelope := h.decisionEnvelope(requestID, r, upstreamPath, requestBytes, bodySHA, bodyMeta)
	decisionStart := time.Now()
	requestFields := map[string]any{
		"request_id":          requestID,
		"method":              r.Method,
		"path":                r.URL.Path,
		"mediation_mode":      h.mode,
		"workspace_id":        h.workspaceID,
		"capability":          h.capability,
		"worker_id":           h.workerID,
		"request_bytes":       requestBytes,
		"request_body_sha256": bodySHA,
		"policy_source":       h.policySource(),
		"elapsed_ms":          elapsedMS(start),
	}
	addRequestBodyLogFields(requestFields, bodyMeta)
	h.log("mediation_decision_request", requestFields)
	decision := decisionResult{Decision: decisionAllow, Reason: "local_allow", AuditEventID: "local:" + requestID}
	if h.mode == ModePolicy {
		decision = h.policyDecision(ctx, requestID, envelope)
	}
	event := "mediation_decision_error"
	switch decision.Decision {
	case decisionAllow:
		event = "mediation_decision_allow"
	case decisionDeny:
		event = "mediation_decision_deny"
	}
	h.log(event, map[string]any{
		"request_id":               requestID,
		"method":                   r.Method,
		"path":                     r.URL.Path,
		"mediation_mode":           h.mode,
		"mediation_result":         decision.Decision,
		"mediation_reason":         decision.Reason,
		"mediation_decision_ms":    elapsedMS(decisionStart),
		"mediation_policy_status":  decision.HTTPStatus,
		"mediation_policy_source":  decision.PolicySource,
		"mediation_policy_rule_id": decision.PolicyRuleID,
		"mediation_policy_sha256":  decision.PolicySHA256,
		"audit_event_id":           decision.AuditEventID,
		"elapsed_ms":               elapsedMS(start),
	})
	return decision
}

func (h *Handler) policyDecision(ctx context.Context, requestID string, envelope DecisionEnvelope) decisionResult {
	if h.filePolicy != nil {
		return h.filePolicy.Decide(envelope, h.filePolicySource, requestID)
	}
	if h.policyURL == nil {
		return decisionResult{Decision: decisionError, Reason: "policy_unconfigured"}
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return decisionResult{Decision: decisionError, Reason: "policy_envelope_error", Error: err.Error(), PolicySource: "url"}
	}
	reqCtx, cancel := context.WithTimeout(ctx, h.policyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, h.policyURL.String(), bytes.NewReader(payload))
	if err != nil {
		return decisionResult{Decision: decisionError, Reason: "policy_request_error", Error: err.Error(), PolicySource: "url"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(requestIDHeader, requestID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return decisionResult{Decision: decisionError, Reason: "policy_unavailable", Error: err.Error(), PolicySource: "url"}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return decisionResult{Decision: decisionError, Reason: fmt.Sprintf("policy_http_%d", resp.StatusCode), HTTPStatus: resp.StatusCode, PolicySource: "url"}
	}
	var doc policyResponse
	if err := json.Unmarshal(data, &doc); err != nil {
		return decisionResult{Decision: decisionError, Reason: "policy_invalid_json", HTTPStatus: resp.StatusCode, Error: err.Error(), PolicySource: "url"}
	}
	decision := strings.ToLower(strings.TrimSpace(doc.Decision))
	if decision == "" {
		decision = strings.ToLower(strings.TrimSpace(doc.Result))
	}
	if decision != decisionAllow && decision != decisionDeny {
		return decisionResult{Decision: decisionError, Reason: "policy_invalid_decision", HTTPStatus: resp.StatusCode, PolicySource: "url"}
	}
	reason := strings.TrimSpace(doc.Reason)
	if reason == "" {
		reason = "policy_" + decision
	}
	return decisionResult{Decision: decision, Reason: reason, HTTPStatus: resp.StatusCode, AuditEventID: doc.AuditEventID, PolicySource: "url"}
}

func (h *Handler) proxyUpstream(w http.ResponseWriter, r *http.Request, body []byte, requestID, upstreamPath string, requestBytes int64, bodySHA string, decision decisionResult, start time.Time) {
	upstreamURL := *h.targetBaseURL
	upstreamURL.Host = h.currentUpstreamHost(requestID)
	upstreamURL.Path = upstreamPathOnly(upstreamPath)
	upstreamURL.RawQuery = upstreamQueryOnly(upstreamPath)
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL.String(), bytes.NewReader(body))
	if err != nil {
		h.writeError(w, requestID, http.StatusBadGateway, "upstream request error", err.Error())
		return
	}
	copyHeaders(req.Header, r.Header)
	req.Header.Set(requestIDHeader, requestID)
	h.log("upstream_request_start", map[string]any{
		"request_id":       requestID,
		"method":           r.Method,
		"path":             r.URL.Path,
		"upstream_path":    upstreamPath,
		"mediation_mode":   h.mode,
		"mediation_result": decision.Decision,
		"elapsed_ms":       elapsedMS(start),
	})
	resp, err := h.client.Do(req)
	if err != nil {
		h.writeError(w, requestID, http.StatusBadGateway, "upstream error", err.Error())
		return
	}
	defer resp.Body.Close()
	h.log("upstream_headers", map[string]any{
		"request_id":       requestID,
		"method":           r.Method,
		"path":             r.URL.Path,
		"status":           resp.StatusCode,
		"mediation_mode":   h.mode,
		"mediation_result": decision.Decision,
		"elapsed_ms":       elapsedMS(start),
	})
	copyHeaders(w.Header(), resp.Header)
	w.Header().Set(requestIDHeader, requestID)
	w.WriteHeader(resp.StatusCode)
	var responseBytes int64
	firstByte := false
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if !firstByte {
				firstByte = true
				h.log("upstream_first_body_byte", map[string]any{
					"request_id":       requestID,
					"method":           r.Method,
					"path":             r.URL.Path,
					"mediation_mode":   h.mode,
					"mediation_result": decision.Decision,
					"elapsed_ms":       elapsedMS(start),
				})
			}
			wrote, writeErr := w.Write(buf[:n])
			responseBytes += int64(wrote)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			if writeErr != nil {
				h.log("request_error", map[string]any{
					"request_id":       requestID,
					"method":           r.Method,
					"path":             r.URL.Path,
					"status":           resp.StatusCode,
					"mediation_mode":   h.mode,
					"mediation_result": decision.Decision,
					"error":            writeErr.Error(),
					"duration_ms":      elapsedMS(start),
				})
				return
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			h.log("request_error", map[string]any{
				"request_id":       requestID,
				"method":           r.Method,
				"path":             r.URL.Path,
				"status":           resp.StatusCode,
				"mediation_mode":   h.mode,
				"mediation_result": decision.Decision,
				"error":            readErr.Error(),
				"duration_ms":      elapsedMS(start),
			})
			return
		}
	}
	h.logRequestEnd(requestID, r, requestBytes, bodySHA, responseBytes, resp.StatusCode, decision, start)
}

// currentUpstreamHost returns the "host:port" this request should be proxied
// to. With no resolver configured it is the start-time target. With one — the
// mediator fronts a paired model runner — the current address wins, so a runner
// restart is absorbed here rather than by routing the guest around the
// mediator. A move is logged once, when it changes.
func (h *Handler) currentUpstreamHost(requestID string) string {
	if h.resolveUpstream == nil {
		return h.targetBaseURL.Host
	}
	resolved := strings.TrimSpace(h.resolveUpstream())
	h.upstreamMu.Lock()
	defer h.upstreamMu.Unlock()
	if resolved == "" || resolved == h.upstreamHost {
		return h.upstreamHost
	}
	previous := h.upstreamHost
	h.upstreamHost = resolved
	h.log("upstream_target_changed", map[string]any{
		"request_id":        requestID,
		"previous_upstream": previous,
		"upstream":          resolved,
		"mediation_mode":    h.mode,
		"worker_id":         h.workerID,
		"workspace_id":      h.workspaceID,
	})
	return resolved
}

func (h *Handler) logRequestEnd(requestID string, r *http.Request, requestBytes int64, bodySHA string, responseBytes int64, status int, decision decisionResult, start time.Time) {
	h.log("request_end", map[string]any{
		"request_id":               requestID,
		"method":                   r.Method,
		"path":                     r.URL.Path,
		"request_bytes":            requestBytes,
		"request_body_sha256":      bodySHA,
		"response_bytes":           responseBytes,
		"status":                   status,
		"duration_ms":              elapsedMS(start),
		"mediation_mode":           h.mode,
		"mediation_result":         decision.Decision,
		"mediation_reason":         decision.Reason,
		"mediation_policy_status":  decision.HTTPStatus,
		"mediation_policy_source":  decision.PolicySource,
		"mediation_policy_rule_id": decision.PolicyRuleID,
		"mediation_policy_sha256":  decision.PolicySHA256,
		"audit_event_id":           decision.AuditEventID,
	})
}

func (h *Handler) writeError(w http.ResponseWriter, requestID string, status int, message, reason string) {
	payload := map[string]any{"error": map[string]any{"message": message, "reason": reason, "request_id": requestID}}
	data, _ := json.Marshal(payload)
	data = append(data, '\n')
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Header().Set(requestIDHeader, requestID)
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func (h *Handler) decisionEnvelope(requestID string, r *http.Request, upstreamPath string, requestBytes int64, bodySHA string, bodyMeta *DecisionRequestBody) DecisionEnvelope {
	return DecisionEnvelope{
		SchemaVersion: 1,
		RequestID:     requestID,
		Workspace:     DecisionWorkspace{ID: h.workspaceID},
		Capability:    h.capability,
		Worker: DecisionWorker{
			ID:             h.workerID,
			Protocol:       "openai-compatible",
			TargetBasePath: h.targetBasePath,
		},
		Request: DecisionRequest{
			Method:     r.Method,
			Path:       r.URL.Path,
			Query:      r.URL.RawQuery,
			Upstream:   upstreamPath,
			Bytes:      requestBytes,
			BodySHA256: bodySHA,
			Body:       bodyMeta,
		},
		Limits: DecisionLimits{
			UpstreamTimeoutSeconds: h.upstreamTimeout.Seconds(),
			PolicyTimeoutSeconds:   h.policyTimeout.Seconds(),
		},
		DeadlineEpoch: float64(time.Now().Add(h.upstreamTimeout).UnixNano()) / 1e9,
	}
}

func (h *Handler) policySource() string {
	if h.mode != ModePolicy {
		return ""
	}
	if h.filePolicy != nil {
		return "file"
	}
	if h.policyURL != nil {
		return "url"
	}
	return ""
}

func (h *Handler) upstreamPath(incoming *url.URL) string {
	path := incoming.EscapedPath()
	if path == "" {
		path = "/"
	}
	base := h.targetBasePath
	if base != "/" && path != base && !strings.HasPrefix(path, base+"/") {
		path = strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
	}
	if incoming.RawQuery != "" {
		path += "?" + incoming.RawQuery
	}
	return path
}

func (h *Handler) log(event string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	h.logger.Log(event, fields)
}

func parseEndpointURL(raw, name string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%s URL is required", name)
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	parsed, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse %s URL: %w", name, err)
	}
	if parsed.Scheme != "http" {
		return nil, fmt.Errorf("%s URL must use http://", name)
	}
	if parsed.Hostname() == "" {
		return nil, fmt.Errorf("%s URL must include a host", name)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("%s URL must not include credentials", name)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" && parsed.Path == "" {
		return nil, fmt.Errorf("%s URL must not include query or fragment", name)
	}
	return parsed, nil
}

func normalizedBasePath(u *url.URL) string {
	path := strings.TrimRight(u.EscapedPath(), "/")
	if path == "" {
		return "/v1"
	}
	return path
}

func readRequestBody(r *http.Request, maxBytes int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	return io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBytes))
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		if _, skip := hopByHopHeaders[strings.ToLower(key)]; skip || strings.EqualFold(key, "host") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func newRequestID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", data[0:4], data[4:6], data[6:8], data[8:10], data[10:16])
}

func elapsedMS(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000
}

func upstreamPathOnly(path string) string {
	left, _, _ := strings.Cut(path, "?")
	if left == "" {
		return "/"
	}
	return left
}

func upstreamQueryOnly(path string) string {
	_, right, ok := strings.Cut(path, "?")
	if !ok {
		return ""
	}
	return right
}

type BufferLogger struct {
	mu     sync.Mutex
	Events []map[string]any
}

func (b *BufferLogger) Log(event string, fields map[string]any) {
	row := map[string]any{"event": event}
	for key, value := range fields {
		row[key] = value
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Events = append(b.Events, row)
}
