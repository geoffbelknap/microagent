package hostworker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const filePolicySchemaVersion = "microagent.model_policy.v1"

type FilePolicy struct {
	SchemaVersion string           `json:"schema_version"`
	Default       string           `json:"default,omitempty"`
	Rules         []FilePolicyRule `json:"rules,omitempty"`
}

type FilePolicyRule struct {
	ID     string           `json:"id"`
	Effect string           `json:"effect"`
	Reason string           `json:"reason,omitempty"`
	Match  FilePolicyMatch  `json:"match,omitempty"`
	Limits FilePolicyLimits `json:"limits,omitempty"`
}

type FilePolicyMatch struct {
	WorkspaceIDs []string `json:"workspace_ids,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	WorkerIDs    []string `json:"worker_ids,omitempty"`
	Methods      []string `json:"methods,omitempty"`
	Paths        []string `json:"paths,omitempty"`
	Models       []string `json:"models,omitempty"`
}

type FilePolicyLimits struct {
	MaxRequestBytes  int64    `json:"max_request_bytes,omitempty"`
	MaxTextBytes     int64    `json:"max_text_bytes,omitempty"`
	MaxMessages      int      `json:"max_messages,omitempty"`
	MaxTokens        int      `json:"max_tokens,omitempty"`
	Stream           *bool    `json:"stream,omitempty"`
	AllowedToolNames []string `json:"allowed_tool_names,omitempty"`
}

type DecisionRequestBody struct {
	ContentType      string   `json:"content_type,omitempty"`
	Model            string   `json:"model,omitempty"`
	Stream           *bool    `json:"stream,omitempty"`
	MaxTokens        *int     `json:"max_tokens,omitempty"`
	MessageCount     int      `json:"message_count,omitempty"`
	MessageTextBytes int64    `json:"message_text_bytes,omitempty"`
	PromptTextBytes  int64    `json:"prompt_text_bytes,omitempty"`
	TextBytes        int64    `json:"text_bytes,omitempty"`
	ToolNames        []string `json:"tool_names,omitempty"`
	ParseError       string   `json:"parse_error,omitempty"`
}

type policyFileSource struct {
	Path   string
	SHA256 string
}

func LoadFilePolicy(path string) (*FilePolicy, policyFileSource, error) {
	source := policyFileSource{}
	rawPath := strings.TrimSpace(path)
	if rawPath == "" {
		return nil, source, fmt.Errorf("policy file path is required")
	}
	absPath, err := filepath.Abs(rawPath)
	if err != nil {
		return nil, source, fmt.Errorf("resolve policy file path: %w", err)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, source, fmt.Errorf("read policy file %s: %w", absPath, err)
	}
	sum := sha256.Sum256(data)
	source = policyFileSource{Path: absPath, SHA256: hex.EncodeToString(sum[:])}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var policy FilePolicy
	if err := dec.Decode(&policy); err != nil {
		return nil, source, fmt.Errorf("parse policy file %s: %w", absPath, err)
	}
	if err := ensureEOF(dec); err != nil {
		return nil, source, fmt.Errorf("parse policy file %s: %w", absPath, err)
	}
	if err := policy.Validate(); err != nil {
		return nil, source, fmt.Errorf("validate policy file %s: %w", absPath, err)
	}
	return &policy, source, nil
}

func (p *FilePolicy) Validate() error {
	if p == nil {
		return fmt.Errorf("policy is nil")
	}
	if strings.TrimSpace(p.SchemaVersion) != filePolicySchemaVersion {
		return fmt.Errorf("schema_version must be %q", filePolicySchemaVersion)
	}
	defaultEffect := normalizedDecision(p.Default)
	if defaultEffect == "" {
		defaultEffect = decisionDeny
	}
	if defaultEffect != decisionAllow && defaultEffect != decisionDeny {
		return fmt.Errorf("default must be allow or deny")
	}
	seen := map[string]struct{}{}
	for i, rule := range p.Rules {
		id := strings.TrimSpace(rule.ID)
		if id == "" {
			return fmt.Errorf("rules[%d].id is required", i)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("rules[%d].id %q is duplicated", i, id)
		}
		seen[id] = struct{}{}
		effect := normalizedDecision(rule.Effect)
		if effect != decisionAllow && effect != decisionDeny {
			return fmt.Errorf("rules[%d].effect must be allow or deny", i)
		}
		if err := rule.Limits.validate(i); err != nil {
			return err
		}
	}
	return nil
}

func (p *FilePolicy) Decide(envelope DecisionEnvelope, source policyFileSource, requestID string) decisionResult {
	for _, rule := range p.Rules {
		if !rule.Match.matches(envelope) {
			continue
		}
		effect := normalizedDecision(rule.Effect)
		if effect == decisionDeny {
			return filePolicyDecision(effect, ruleReason(rule, effect), rule.ID, source, requestID)
		}
		if reason := rule.Limits.exceeded(envelope); reason != "" {
			return filePolicyDecision(decisionDeny, reason, rule.ID, source, requestID)
		}
		return filePolicyDecision(decisionAllow, ruleReason(rule, effect), rule.ID, source, requestID)
	}
	effect := normalizedDecision(p.Default)
	if effect == "" {
		effect = decisionDeny
	}
	return filePolicyDecision(effect, "file_policy_default_"+effect, "", source, requestID)
}

func InspectDecisionRequestBody(contentType string, body []byte) *DecisionRequestBody {
	cleanContentType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	trimmed := bytes.TrimSpace(body)
	if cleanContentType == "" && len(trimmed) == 0 {
		return nil
	}
	meta := &DecisionRequestBody{ContentType: cleanContentType}
	if len(trimmed) == 0 {
		return meta
	}
	if !isJSONContent(cleanContentType, trimmed) {
		return meta
	}
	var doc map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		meta.ParseError = "invalid_json"
		return meta
	}
	if err := ensureEOF(dec); err != nil {
		meta.ParseError = "invalid_json"
		return meta
	}
	meta.Model = stringFromRaw(doc["model"])
	meta.Stream = boolPtrFromRaw(doc["stream"])
	if maxTokens := intPtrFromRaw(doc["max_tokens"]); maxTokens != nil {
		meta.MaxTokens = maxTokens
	} else {
		meta.MaxTokens = intPtrFromRaw(doc["max_completion_tokens"])
	}
	meta.MessageCount, meta.MessageTextBytes = inspectMessages(doc["messages"])
	meta.PromptTextBytes = inspectPromptBytes(doc["prompt"])
	meta.TextBytes = meta.MessageTextBytes + meta.PromptTextBytes
	meta.ToolNames = inspectToolNames(doc["tools"])
	return meta
}

func addRequestBodyLogFields(fields map[string]any, body *DecisionRequestBody) {
	if body == nil {
		return
	}
	if body.ContentType != "" {
		fields["request_content_type"] = body.ContentType
	}
	if body.Model != "" {
		fields["request_model"] = body.Model
	}
	if body.Stream != nil {
		fields["request_stream"] = *body.Stream
	}
	if body.MaxTokens != nil {
		fields["request_max_tokens"] = *body.MaxTokens
	}
	if body.MessageCount != 0 {
		fields["request_message_count"] = body.MessageCount
	}
	if body.TextBytes != 0 {
		fields["request_text_bytes"] = body.TextBytes
	}
	if len(body.ToolNames) != 0 {
		fields["request_tool_names"] = strings.Join(body.ToolNames, ",")
	}
	if body.ParseError != "" {
		fields["request_body_parse_error"] = body.ParseError
	}
}

func (l FilePolicyLimits) validate(ruleIndex int) error {
	if l.MaxRequestBytes < 0 {
		return fmt.Errorf("rules[%d].limits.max_request_bytes must be non-negative", ruleIndex)
	}
	if l.MaxTextBytes < 0 {
		return fmt.Errorf("rules[%d].limits.max_text_bytes must be non-negative", ruleIndex)
	}
	if l.MaxMessages < 0 {
		return fmt.Errorf("rules[%d].limits.max_messages must be non-negative", ruleIndex)
	}
	if l.MaxTokens < 0 {
		return fmt.Errorf("rules[%d].limits.max_tokens must be non-negative", ruleIndex)
	}
	return nil
}

func (l FilePolicyLimits) exceeded(envelope DecisionEnvelope) string {
	body := envelope.Request.Body
	if l.MaxRequestBytes > 0 && envelope.Request.Bytes > l.MaxRequestBytes {
		return "file_policy_limit_request_bytes"
	}
	if l.MaxTextBytes > 0 {
		textBytes := int64(0)
		if body != nil {
			textBytes = body.TextBytes
		}
		if textBytes > l.MaxTextBytes {
			return "file_policy_limit_text_bytes"
		}
	}
	if l.MaxMessages > 0 {
		messageCount := 0
		if body != nil {
			messageCount = body.MessageCount
		}
		if messageCount > l.MaxMessages {
			return "file_policy_limit_messages"
		}
	}
	if l.MaxTokens > 0 {
		if body == nil || body.MaxTokens == nil {
			return "file_policy_limit_max_tokens_missing"
		}
		if *body.MaxTokens > l.MaxTokens {
			return "file_policy_limit_max_tokens"
		}
	}
	if l.Stream != nil {
		stream := false
		if body != nil && body.Stream != nil {
			stream = *body.Stream
		}
		if stream != *l.Stream {
			return "file_policy_limit_stream"
		}
	}
	if len(l.AllowedToolNames) != 0 {
		allowed := map[string]struct{}{}
		for _, name := range l.AllowedToolNames {
			clean := strings.TrimSpace(name)
			if clean != "" {
				allowed[clean] = struct{}{}
			}
		}
		if body != nil {
			for _, name := range body.ToolNames {
				if _, ok := allowed[name]; !ok {
					return "file_policy_limit_tool_name"
				}
			}
		}
	}
	return ""
}

func (m FilePolicyMatch) matches(envelope DecisionEnvelope) bool {
	body := envelope.Request.Body
	model := ""
	if body != nil {
		model = body.Model
	}
	return selectorMatches(m.WorkspaceIDs, envelope.Workspace.ID) &&
		selectorMatches(m.Capabilities, envelope.Capability) &&
		selectorMatches(m.WorkerIDs, envelope.Worker.ID) &&
		selectorMatchesFold(m.Methods, envelope.Request.Method) &&
		selectorMatches(m.Paths, envelope.Request.Path) &&
		selectorMatches(m.Models, model)
}

func selectorMatches(selectors []string, value string) bool {
	if len(selectors) == 0 {
		return true
	}
	for _, selector := range selectors {
		if selector == "*" || strings.TrimSpace(selector) == value {
			return true
		}
	}
	return false
}

func selectorMatchesFold(selectors []string, value string) bool {
	if len(selectors) == 0 {
		return true
	}
	for _, selector := range selectors {
		if selector == "*" || strings.EqualFold(strings.TrimSpace(selector), value) {
			return true
		}
	}
	return false
}

func filePolicyDecision(effect, reason, ruleID string, source policyFileSource, requestID string) decisionResult {
	auditEventID := "file:" + requestID
	if ruleID != "" {
		auditEventID += ":" + ruleID
	}
	return decisionResult{
		Decision:     effect,
		Reason:       reason,
		AuditEventID: auditEventID,
		PolicySource: "file",
		PolicyRuleID: strings.TrimSpace(ruleID),
		PolicySHA256: source.SHA256,
	}
}

func ruleReason(rule FilePolicyRule, effect string) string {
	if reason := strings.TrimSpace(rule.Reason); reason != "" {
		return reason
	}
	return "file_policy_" + effect
}

func normalizedDecision(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func isJSONContent(contentType string, body []byte) bool {
	if contentType == "application/json" || strings.HasSuffix(contentType, "+json") {
		return true
	}
	return len(body) != 0 && (body[0] == '{' || body[0] == '[')
}

func ensureEOF(dec *json.Decoder) error {
	var extra json.RawMessage
	if err := dec.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("multiple JSON documents are not allowed")
}

func stringFromRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var out string
	if err := json.Unmarshal(raw, &out); err != nil {
		return ""
	}
	return out
}

func boolPtrFromRaw(raw json.RawMessage) *bool {
	if len(raw) == 0 {
		return nil
	}
	var out bool
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return &out
}

func intPtrFromRaw(raw json.RawMessage) *int {
	if len(raw) == 0 {
		return nil
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil
	}
	number, ok := value.(json.Number)
	if !ok {
		return nil
	}
	parsed, err := strconv.ParseInt(number.String(), 10, 32)
	if err != nil {
		return nil
	}
	out := int(parsed)
	return &out
}

func inspectMessages(raw json.RawMessage) (int, int64) {
	if len(raw) == 0 {
		return 0, 0
	}
	var messages []struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &messages); err != nil {
		return 0, 0
	}
	var textBytes int64
	for _, message := range messages {
		textBytes += inspectContentBytes(message.Content)
	}
	return len(messages), textBytes
}

func inspectPromptBytes(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var prompt string
	if err := json.Unmarshal(raw, &prompt); err == nil {
		return int64(len(prompt))
	}
	var prompts []string
	if err := json.Unmarshal(raw, &prompts); err == nil {
		var total int64
		for _, item := range prompts {
			total += int64(len(item))
		}
		return total
	}
	return inspectContentBytes(raw)
}

func inspectContentBytes(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return int64(len(text))
	}
	var object struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &object); err == nil && object.Text != "" {
		return int64(len(object.Text))
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return 0
	}
	var total int64
	for _, part := range parts {
		total += inspectContentBytes(part)
	}
	return total
}

func inspectToolNames(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil
	}
	names := make([]string, 0, len(tools))
	seen := map[string]struct{}{}
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Function.Name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
