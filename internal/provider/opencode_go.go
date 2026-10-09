// Package provider implements a direct OpenCode Go client covering all
// three wire shapes (Chat Completions, Responses, Messages) with
// non-streaming and SSE streaming turns. It mirrors the Rust
// slop-runtime opencode_go adapter request/response mapping.
package provider

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

const (
	BaseURL          = "https://opencode.ai/zen/go/v1"
	AnthropicVersion = "2023-06-01"
	SessionHeader    = "x-opencode-session"
	UserAgent        = "hive/0.1.0"
)

// Wire is one of the three gateway shapes.
type Wire string

const (
	WireChat      Wire = "openai-chat-completions"
	WireResponses Wire = "openai-responses"
	WireMessages  Wire = "anthropic-messages"
)

func (w Wire) Path() string {
	switch w {
	case WireResponses:
		return "/responses"
	case WireMessages:
		return "/messages"
	default:
		return "/chat/completions"
	}
}

// Model is one catalog entry: id + display + wire.
type Model struct {
	ID          string
	DisplayName string
	Wire        Wire
}

// Catalog is the 32-model documented Go table from opencode_go.rs.
var Catalog = []Model{
	{"grok-4.7", "Grok 4.7", WireResponses},
	{"grok-4.6", "Grok 4.6", WireResponses},
	{"gpt-6-luna", "GPT 6 Luna", WireResponses},
	{"gpt-5.6-luna", "GPT 5.6 Luna", WireResponses},
	{"muse-spark-1.3-contributor", "Muse Spark 1.3 Contributor", WireResponses},
	{"muse-spark-1.2-contributor", "Muse Spark 1.2 Contributor", WireResponses},
	{"claude-haiku-5-5", "Claude Haiku 5.5", WireMessages},
	{"minimax-m3", "MiniMax M3", WireMessages},
	{"minimax-m2.7", "MiniMax M2.7", WireMessages},
	{"qwen3.8-max", "Qwen3.8 Max", WireMessages},
	{"qwen3.8-flash", "Qwen3.8 Flash", WireMessages},
	{"qwen3.7-plus", "Qwen3.7 Plus", WireMessages},
	{"glm-5.3-flash", "GLM-5.3-Flash", WireChat},
	{"glm-5.3", "GLM-5.3", WireChat},
	{"glm-5.2", "GLM-5.2", WireChat},
	{"kimi-k3", "Kimi K3", WireChat},
	{"kimi-k2.7-code", "Kimi K2.7 Code", WireChat},
	{"kimi-k2.6", "Kimi K2.6", WireChat},
	{"longcat-2.0", "LongCat-2.0", WireChat},
	{"longcat-2.5-preview-free", "LongCat 2.5 Preview Free", WireChat},
	{"step-5-preview-free", "Step 5 Preview Free", WireChat},
	{"deepseek-v4.1-flash", "DeepSeek V4.1 Flash", WireChat},
	{"deepseek-v4-pro", "DeepSeek V4 Pro", WireChat},
	{"deepseek-v4-flash", "DeepSeek V4 Flash", WireChat},
	{"deepseek-v4-flash-vision-exp", "DeepSeek V4 Flash Vision Exp", WireChat},
	{"mimo-v2.6-flash", "MiMo-V2.6-Flash", WireChat},
	{"mimo-v2.6-pro", "MiMo-V2.6-Pro", WireChat},
	{"mimo-v2.5", "MiMo-V2.5", WireChat},
	{"mimo-v2.5-pro", "MiMo-V2.5-Pro", WireChat},
	{"hy4-preview", "Hy4 preview", WireChat},
	{"hy3", "Hy3", WireChat},
	{"space-bunny", "Space Bunny", WireChat},
}

// WireFor resolves a model id to its wire shape, failing closed.
func WireFor(model string) (Wire, error) {
	for _, m := range Catalog {
		if m.ID == model {
			return m.Wire, nil
		}
	}
	return "", fmt.Errorf("unsupported model for opencode-go: %s", model)
}

// Usage mirrors the provider-neutral counters.
type Usage struct {
	Input  *uint64
	Output *uint64
	Total  *uint64
}

// Result is one completed turn.
type Result struct {
	Model         string
	ResolvedModel string
	Text          string
	Usage         Usage
	Wire          Wire
	Outcome       string // completed | incomplete:<reason>
}

// Client is an authenticated direct gateway client.
type Client struct {
	http    *http.Client
	baseURL string
	apiKey  string
}

// New validates the key and builds the client.
func New(apiKey string) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("missing API key: set OPENCODE_GO_API_KEY")
	}
	return &Client{
		http:    &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		baseURL: BaseURL,
		apiKey:  strings.TrimSpace(apiKey),
	}, nil
}

// ListModels fetches GET /models advertised ids.
func (c *Client) ListModels() ([]string, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set(SessionHeader, "hive-model-discovery")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := readBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("provider opencode-go returned HTTP %d", resp.StatusCode)
	}
	var v struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &v); err != nil || v.Data == nil {
		return nil, fmt.Errorf("provider opencode-go returned an unrecognized response")
	}
	out := make([]string, 0, len(v.Data))
	for _, d := range v.Data {
		if d.ID == "" {
			return nil, fmt.Errorf("provider returned an invalid model id")
		}
		out = append(out, d.ID)
	}
	return out, nil
}

func openaiMessages(prompt string) []any {
	return []any{map[string]any{"role": "user", "content": prompt}}
}

func (c *Client) post(wire Wire, sessionID string, payload any) (map[string]any, http.Header, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequest("POST", c.baseURL+wire.Path(), bytes.NewReader(raw))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set(SessionHeader, sessionID)
	if wire == WireMessages {
		req.Header.Set("x-api-key", c.apiKey)
		req.Header.Set("anthropic-version", AnthropicVersion)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := readBody(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("provider opencode-go returned HTTP %d", resp.StatusCode)
	}
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, nil, fmt.Errorf("provider opencode-go returned an unrecognized response")
	}
	return v, resp.Header, nil
}

func str(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	s, _ := cur.(string)
	return s
}

func usageOf(m map[string]any) Usage {
	u, _ := m["usage"].(map[string]any)
	if u == nil {
		return Usage{}
	}
	num := func(keys ...string) *uint64 {
		for _, k := range keys {
			if f, ok := u[k].(float64); ok && f >= 0 && f < math.Exp2(64) && math.Trunc(f) == f {
				v := uint64(f)
				return &v
			}
		}
		return nil
	}
	return Usage{Input: num("input_tokens", "prompt_tokens"), Output: num("output_tokens", "completion_tokens"), Total: num("total_tokens")}
}

const maxProviderBytes = 8 << 20

func readBody(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxProviderBytes+1))
	if err != nil {
		return nil, fmt.Errorf("provider response read failed: %w", err)
	}
	if len(data) > maxProviderBytes {
		return nil, fmt.Errorf("provider response exceeded size limit")
	}
	return data, nil
}

func mergeUsage(current, update Usage) Usage {
	if update.Input != nil {
		current.Input = update.Input
	}
	if update.Output != nil {
		current.Output = update.Output
	}
	if update.Total != nil {
		current.Total = update.Total
	}
	return current
}

func totalUsage(u Usage) Usage {
	if u.Total == nil && u.Input != nil && u.Output != nil && *u.Input <= math.MaxUint64-*u.Output {
		total := *u.Input + *u.Output
		u.Total = &total
	}
	return u
}

func payloadFor(wire Wire, model, prompt string, maxTokens uint32, stream bool) map[string]any {
	payload := map[string]any{"model": model}
	switch wire {
	case WireResponses:
		payload["input"], payload["max_output_tokens"] = prompt, maxTokens
	default:
		payload["messages"], payload["max_tokens"] = openaiMessages(prompt), maxTokens
	}
	if stream {
		payload["stream"] = true
	}
	return payload
}

func parseResult(wire Wire, model string, v map[string]any) (*Result, error) {
	var text, outcome string
	var err error
	switch wire {
	case WireChat:
		text, outcome, err = parseChat(v)
	case WireResponses:
		text, outcome, err = parseResponses(v)
	case WireMessages:
		text, outcome, err = parseMessages(v)
	}
	if err != nil {
		return nil, err
	}
	return &Result{Model: model, ResolvedModel: str(v, "model"), Text: text, Usage: totalUsage(usageOf(v)), Wire: wire, Outcome: outcome}, nil
}

// Complete runs one non-streaming turn on the model's native wire.
func (c *Client) Complete(model, prompt string, maxTokens uint32, sessionID string) (*Result, error) {
	wire, err := WireFor(model)
	if err != nil {
		return nil, err
	}
	if sessionID == "" {
		sessionID = "hive-direct"
	}
	v, _, err := c.post(wire, sessionID, payloadFor(wire, model, prompt, maxTokens, false))
	if err != nil {
		return nil, err
	}
	return parseResult(wire, model, v)
}

// CompleteStreaming folds complete SSE events and requires an explicit terminal
// outcome. Deltas are provisional; the returned text is authoritative.
func (c *Client) CompleteStreaming(model, prompt string, maxTokens uint32, sessionID string, onDelta func(text string)) (*Result, error) {
	wire, err := WireFor(model)
	if err != nil {
		return nil, err
	}
	if sessionID == "" {
		sessionID = "hive-direct"
	}
	raw, err := json.Marshal(payloadFor(wire, model, prompt, maxTokens, true))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", c.baseURL+wire.Path(), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set(SessionHeader, sessionID)
	req.Header.Set("Accept", "text/event-stream")
	if wire == WireMessages {
		req.Header.Set("x-api-key", c.apiKey)
		req.Header.Set("anthropic-version", AnthropicVersion)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("provider opencode-go returned HTTP %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		body, err := readBody(resp.Body)
		if err != nil {
			return nil, err
		}
		var v map[string]any
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, fmt.Errorf("provider returned an unrecognized response")
		}
		r, err := parseResult(wire, model, v)
		if err == nil && r.Text != "" && onDelta != nil {
			onDelta(r.Text)
		}
		return r, err
	}
	var assembled strings.Builder
	var resolved, outcome string
	var usage Usage
	var sawTerminal bool
	appendDelta := func(text string) error {
		if sawTerminal {
			return fmt.Errorf("provider sent content after terminal outcome")
		}
		if assembled.Len()+len(text) > maxProviderBytes {
			return fmt.Errorf("provider stream exceeded size limit")
		}
		assembled.WriteString(text)
		if onDelta != nil {
			onDelta(text)
		}
		return nil
	}
	process := func(data string) (bool, error) {
		if data == "[DONE]" {
			if !sawTerminal {
				return false, fmt.Errorf("stream ended before terminal event")
			}
			return true, nil
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(data), &v); err != nil || v == nil {
			return false, fmt.Errorf("provider returned an invalid stream event")
		}
		typ := str(v, "type")
		if v["error"] != nil || typ == "error" || typ == "response.failed" || typ == "response.error" {
			return false, fmt.Errorf("provider reported a failed stream")
		}
		if m := str(v, "model"); m != "" {
			resolved = m
		}
		usage = mergeUsage(usage, usageOf(v))
		switch wire {
		case WireChat:
			if choices, ok := v["choices"].([]any); ok && len(choices) > 0 {
				cm, ok := choices[0].(map[string]any)
				if !ok {
					return false, fmt.Errorf("invalid chat stream choice")
				}
				if delta, ok := cm["delta"].(map[string]any); ok && delta["content"] != nil {
					text, ok := delta["content"].(string)
					if !ok {
						return false, fmt.Errorf("invalid chat stream content")
					}
					if text != "" {
						if err := appendDelta(text); err != nil {
							return false, err
						}
					}
				}
				if fr := str(cm, "finish_reason"); fr != "" {
					if sawTerminal {
						return false, fmt.Errorf("multiple terminal outcomes")
					}
					outcome, err = chatOutcome(fr)
					if err != nil {
						return false, err
					}
					sawTerminal = true
				}
			}
		case WireMessages:
			switch typ {
			case "message_start":
				if msg, ok := v["message"].(map[string]any); ok {
					resolved = str(msg, "model")
					usage = mergeUsage(usage, usageOf(msg))
				}
			case "content_block_start":
				if text := str(v, "content_block", "text"); text != "" {
					if err := appendDelta(text); err != nil {
						return false, err
					}
				}
			case "content_block_delta":
				if delta, ok := v["delta"].(map[string]any); ok && delta["text"] != nil {
					text, ok := delta["text"].(string)
					if !ok {
						return false, fmt.Errorf("invalid messages stream content")
					}
					if err := appendDelta(text); err != nil {
						return false, err
					}
				}
			case "message_delta":
				if reason := str(v, "delta", "stop_reason"); reason != "" {
					outcome, err = messagesOutcome(reason)
					if err != nil {
						return false, err
					}
				}
			case "message_stop":
				if outcome == "" {
					return false, fmt.Errorf("messages stream ended without stop reason")
				}
				sawTerminal = true
				return true, nil
			}
		case WireResponses:
			switch typ {
			case "response.output_text.delta":
				text, ok := v["delta"].(string)
				if !ok {
					return false, fmt.Errorf("invalid responses stream content")
				}
				if err := appendDelta(text); err != nil {
					return false, err
				}
			case "response.completed", "response.incomplete":
				response, ok := v["response"].(map[string]any)
				if !ok {
					return false, fmt.Errorf("terminal event lacks response")
				}
				r, err := parseResult(wire, model, response)
				if err != nil {
					return false, err
				}
				if (typ == "response.completed") != (r.Outcome == "completed") {
					return false, fmt.Errorf("response status contradicts terminal event")
				}
				assembled.Reset()
				assembled.WriteString(r.Text)
				resolved, usage, outcome = r.ResolvedModel, r.Usage, r.Outcome
				sawTerminal = true
				return true, nil
			}
		}
		return false, nil
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 256*1024)
	var data []string
	var frameBytes int
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(data) == 0 {
				continue
			}
			done, err := process(strings.Join(data, "\n"))
			data, frameBytes = nil, 0
			if err != nil {
				return nil, err
			}
			if done {
				break
			}
		} else if strings.HasPrefix(line, "data:") {
			part := strings.TrimPrefix(line, "data:")
			part = strings.TrimPrefix(part, " ")
			frameBytes += len(part) + 1
			if frameBytes > 256*1024 {
				return nil, fmt.Errorf("provider stream event exceeded size limit")
			}
			data = append(data, part)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("provider stream read failed: %w", err)
	}
	if len(data) > 0 {
		if _, err := process(strings.Join(data, "\n")); err != nil {
			return nil, err
		}
	}
	if !sawTerminal {
		return nil, fmt.Errorf("stream ended before terminal event")
	}
	return &Result{Model: model, ResolvedModel: resolved, Text: assembled.String(), Usage: totalUsage(usage), Wire: wire, Outcome: outcome}, nil
}

func chatOutcome(reason string) (string, error) {
	switch reason {
	case "stop":
		return "completed", nil
	case "length":
		return "incomplete:max_tokens", nil
	case "content_filter":
		return "incomplete:content_filter", nil
	case "tool_calls":
		return "incomplete:tool_calls", nil
	default:
		return "", fmt.Errorf("unknown chat finish reason")
	}
}

func messagesOutcome(reason string) (string, error) {
	switch reason {
	case "end_turn", "stop_sequence":
		return "completed", nil
	case "max_tokens":
		return "incomplete:max_tokens", nil
	case "tool_use":
		return "incomplete:tool_calls", nil
	case "refusal":
		return "incomplete:refusal", nil
	default:
		return "", fmt.Errorf("unknown messages stop reason")
	}
}

func parseChat(v map[string]any) (string, string, error) {
	choices, ok := v["choices"].([]any)
	if !ok || len(choices) != 1 {
		return "", "", fmt.Errorf("provider returned an invalid chat response")
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return "", "", fmt.Errorf("provider returned an invalid chat choice")
	}
	msg, ok := choice["message"].(map[string]any)
	if !ok {
		return "", "", fmt.Errorf("provider returned no assistant message")
	}
	var text string
	if msg["content"] != nil {
		text, ok = msg["content"].(string)
		if !ok {
			return "", "", fmt.Errorf("provider returned invalid chat content")
		}
	}
	if refusal := str(msg, "refusal"); refusal != "" {
		text += refusal
	}
	outcome, err := chatOutcome(str(choice, "finish_reason"))
	return text, outcome, err
}

func parseResponses(v map[string]any) (string, string, error) {
	status := str(v, "status")
	if status != "completed" && status != "incomplete" {
		return "", "", fmt.Errorf("provider returned no successful terminal response")
	}
	output, ok := v["output"].([]any)
	if !ok {
		return "", "", fmt.Errorf("provider returned invalid responses output")
	}
	var text strings.Builder
	for _, item := range output {
		message, ok := item.(map[string]any)
		if !ok {
			return "", "", fmt.Errorf("provider returned invalid output item")
		}
		if str(message, "type") != "message" {
			continue
		}
		content, ok := message["content"].([]any)
		if !ok {
			return "", "", fmt.Errorf("provider returned invalid message content")
		}
		for _, part := range content {
			p, ok := part.(map[string]any)
			if !ok {
				return "", "", fmt.Errorf("provider returned invalid content part")
			}
			field := "text"
			switch str(p, "type") {
			case "output_text":
			case "refusal":
				field = "refusal"
			default:
				return "", "", fmt.Errorf("provider returned unsupported content part")
			}
			t, ok := p[field].(string)
			if !ok {
				return "", "", fmt.Errorf("provider returned invalid output text")
			}
			text.WriteString(t)
		}
	}
	if status == "incomplete" {
		reason := str(v, "incomplete_details", "reason")
		if reason == "" {
			reason = "unknown"
		}
		return text.String(), "incomplete:" + reason, nil
	}
	return text.String(), "completed", nil
}

func parseMessages(v map[string]any) (string, string, error) {
	content, ok := v["content"].([]any)
	if !ok {
		return "", "", fmt.Errorf("provider returned invalid messages content")
	}
	var text strings.Builder
	for _, block := range content {
		b, ok := block.(map[string]any)
		if !ok {
			return "", "", fmt.Errorf("provider returned invalid content block")
		}
		if str(b, "type") != "text" {
			continue
		}
		t, ok := b["text"].(string)
		if !ok {
			return "", "", fmt.Errorf("provider returned invalid message text")
		}
		text.WriteString(t)
	}
	outcome, err := messagesOutcome(str(v, "stop_reason"))
	return text.String(), outcome, err
}
