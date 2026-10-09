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
		http:    &http.Client{Timeout: 120 * time.Second},
		baseURL: BaseURL,
		apiKey:  apiKey,
	}, nil
}

// ListModels fetches GET /models advertised ids.
func (c *Client) ListModels() ([]string, error) {
	req, _ := http.NewRequest("GET", c.baseURL+"/models", nil)
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set(SessionHeader, "hive-model-discovery")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
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
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("provider opencode-go returned an unrecognized response")
	}
	out := make([]string, 0, len(v.Data))
	for _, d := range v.Data {
		out = append(out, d.ID)
	}
	return out, nil
}

func openaiMessages(prompt string) []any {
	return []any{map[string]any{"role": "user", "content": prompt}}
}

func (c *Client) post(wire Wire, sessionID string, payload any) (map[string]any, http.Header, error) {
	raw, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", c.baseURL+wire.Path(), bytes.NewReader(raw))
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
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
			if f, ok := u[k].(float64); ok {
				v := uint64(f)
				return &v
			}
		}
		return nil
	}
	return Usage{Input: num("input_tokens", "prompt_tokens"), Output: num("output_tokens", "completion_tokens"), Total: num("total_tokens")}
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
	switch wire {
	case WireChat:
		v, _, err := c.post(wire, sessionID, map[string]any{
			"model": model, "messages": openaiMessages(prompt),
			"max_tokens": maxTokens,
		})
		if err != nil {
			return nil, err
		}
		text, outcome := parseChat(v)
		return &Result{Model: model, ResolvedModel: str(v, "model"), Text: text, Usage: usageOf(v), Wire: wire, Outcome: outcome}, nil
	case WireResponses:
		v, _, err := c.post(wire, sessionID, map[string]any{
			"model": model, "input": prompt, "max_output_tokens": maxTokens,
		})
		if err != nil {
			return nil, err
		}
		text, outcome := parseResponses(v)
		return &Result{Model: model, ResolvedModel: str(v, "model"), Text: text, Usage: usageOf(v), Wire: wire, Outcome: outcome}, nil
	default:
		payload := map[string]any{
			"model": model, "max_tokens": maxTokens,
			"messages": []any{map[string]any{"role": "user", "content": prompt}},
		}
		v, _, err := c.post(wire, sessionID, payload)
		if err != nil {
			return nil, err
		}
		text, outcome := parseMessages(v)
		return &Result{Model: model, ResolvedModel: str(v, "model"), Text: text, Usage: usageOf(v), Wire: wire, Outcome: outcome}, nil
	}
}

// CompleteStreaming runs one SSE turn, calling onDelta per text delta.
func (c *Client) CompleteStreaming(model, prompt string, maxTokens uint32, sessionID string, onDelta func(text string)) (*Result, error) {
	wire, err := WireFor(model)
	if err != nil {
		return nil, err
	}
	if sessionID == "" {
		sessionID = "hive-direct"
	}
	var payload any
	switch wire {
	case WireChat:
		payload = map[string]any{"model": model, "messages": openaiMessages(prompt), "max_tokens": maxTokens, "stream": true}
	case WireResponses:
		payload = map[string]any{"model": model, "input": prompt, "max_output_tokens": maxTokens, "stream": true}
	default:
		payload = map[string]any{"model": model, "max_tokens": maxTokens,
			"messages": []any{map[string]any{"role": "user", "content": prompt}}, "stream": true}
	}
	raw, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", c.baseURL+wire.Path(), bytes.NewReader(raw))
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
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		// Gateway ignored stream:true; parse as blocking JSON.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		var v map[string]any
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, fmt.Errorf("provider returned an unrecognized response")
		}
		var text, outcome string
		switch wire {
		case WireChat:
			text, outcome = parseChat(v)
		case WireResponses:
			text, outcome = parseResponses(v)
		default:
			text, outcome = parseMessages(v)
		}
		if text != "" && onDelta != nil {
			onDelta(text)
		}
		return &Result{Model: model, ResolvedModel: str(v, "model"), Text: text, Usage: usageOf(v), Wire: wire, Outcome: outcome}, nil
	}
	var assembled strings.Builder
	var resolved string
	var usage Usage
	var outcome string
	var sawTerminal bool
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 256*1024), 256*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			if wire == WireChat && sawTerminal {
				break
			}
			if wire == WireChat {
				continue
			}
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(data), &v); err != nil {
			continue
		}
		if m, _ := v["model"].(string); m != "" {
			resolved = m
		}
		if u, ok := v["usage"].(map[string]any); ok {
			usage = usageOf(map[string]any{"usage": u})
		}
		switch wire {
		case WireChat:
			if ch, ok := v["choices"].([]any); ok && len(ch) > 0 {
				if cm, ok := ch[0].(map[string]any); ok {
					if d, ok := cm["delta"].(map[string]any); ok {
						if t, _ := d["content"].(string); t != "" {
							assembled.WriteString(t)
							if onDelta != nil {
								onDelta(t)
							}
						}
					}
					if fr, _ := cm["finish_reason"].(string); fr != "" {
						sawTerminal = true
						outcome = chatOutcome(fr)
					}
				}
			}
		case WireMessages:
			if typ, _ := v["type"].(string); typ == "content_block_delta" {
				if d, ok := v["delta"].(map[string]any); ok {
					if t, _ := d["text"].(string); t != "" {
						assembled.WriteString(t)
						if onDelta != nil {
							onDelta(t)
						}
					}
				}
			} else if typ, _ := v["type"].(string); typ == "message_stop" {
				sawTerminal = true
				outcome = "completed"
			}
			if msg, ok := v["message"].(map[string]any); ok {
				if u, ok := msg["usage"].(map[string]any); ok {
					usage = usageOf(map[string]any{"usage": u})
				}
			}
		case WireResponses:
			if typ, _ := v["type"].(string); typ == "response.output_text.delta" {
				if t, _ := v["delta"].(string); t != "" {
					assembled.WriteString(t)
					if onDelta != nil {
						onDelta(t)
					}
				}
			} else if typ, _ := v["type"].(string); typ == "response.completed" || typ == "response.incomplete" {
				sawTerminal = true
				if r, ok := v["response"].(map[string]any); ok {
					t, o := parseResponses(r)
					assembled.Reset()
					assembled.WriteString(t)
					usage = usageOf(r)
					outcome = o
				}
			}
		}
	}
	if !sawTerminal && outcome == "" {
		// Some gateways close after deltas without an explicit terminal
		// event when the turn completed; accept assembled text.
		if assembled.Len() > 0 {
			outcome = "completed"
		} else {
			return nil, fmt.Errorf("stream ended before terminal event")
		}
	}
	if outcome == "" {
		outcome = "completed"
	}
	return &Result{Model: model, ResolvedModel: resolved, Text: assembled.String(), Usage: usage, Wire: wire, Outcome: outcome}, nil
}

func chatOutcome(fr string) string {
	switch fr {
	case "stop":
		return "completed"
	case "length":
		return "incomplete:max_tokens"
	case "content_filter":
		return "incomplete:content_filter"
	default:
		return "completed"
	}
}

func parseChat(v map[string]any) (string, string) {
	ch, _ := v["choices"].([]any)
	if len(ch) == 0 {
		return "", "completed"
	}
	cm, _ := ch[0].(map[string]any)
	msg, _ := cm["message"].(map[string]any)
	t, _ := msg["content"].(string)
	fr, _ := cm["finish_reason"].(string)
	if fr == "" {
		return t, "completed"
	}
	return t, chatOutcome(fr)
}

func parseResponses(v map[string]any) (string, string) {
	status, _ := v["status"].(string)
	var sb strings.Builder
	if out, ok := v["output"].([]any); ok {
		for _, it := range out {
			im, _ := it.(map[string]any)
			if typ, _ := im["type"].(string); typ != "message" {
				continue
			}
			if content, ok := im["content"].([]any); ok {
				for _, p := range content {
					pm, _ := p.(map[string]any)
					if typ, _ := pm["type"].(string); typ != "output_text" {
						continue
					}
					if t, _ := pm["text"].(string); t != "" {
						sb.WriteString(t)
					}
				}
			}
		}
	}
	if status == "incomplete" {
		return sb.String(), "incomplete:max_output_tokens"
	}
	return sb.String(), "completed"
}

func parseMessages(v map[string]any) (string, string) {
	var sb strings.Builder
	if content, ok := v["content"].([]any); ok {
		for _, b := range content {
			bm, _ := b.(map[string]any)
			if typ, _ := bm["type"].(string); typ != "text" {
				continue
			}
			if t, _ := bm["text"].(string); t != "" {
				sb.WriteString(t)
			}
		}
	}
	if sr, _ := v["stop_reason"].(string); sr == "max_tokens" {
		return sb.String(), "incomplete:max_tokens"
	}
	return sb.String(), "completed"
}
