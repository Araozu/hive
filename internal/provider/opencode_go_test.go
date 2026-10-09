package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fixtureClient(t *testing.T, contentType, body string) *Client {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(s.Close)
	c, err := New("fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	c.baseURL = s.URL
	return c
}

func TestNativeWireRequestsAndBlockingStreamFallback(t *testing.T) {
	for _, tc := range []struct{ model, path, body string }{
		{"glm-5.3-flash", "/chat/completions", `{"model":"resolved","choices":[{"finish_reason":"stop","message":{"content":"ok"}}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`},
		{"gpt-6-luna", "/responses", `{"model":"resolved","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":7,"output_tokens":3}}`},
		{"claude-haiku-5-5", "/messages", `{"model":"resolved","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":7,"output_tokens":3}}`},
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.model, streaming), func(t *testing.T) {
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "POST" || r.URL.Path != tc.path || r.Header.Get(SessionHeader) != "session" {
						t.Error("wrong native wire request")
					}
					if tc.path == "/messages" {
						if r.Header.Get("x-api-key") != "fixture-key" || r.Header.Get("anthropic-version") != AnthropicVersion || r.Header.Get("Authorization") != "" {
							t.Error("incorrect Messages authentication")
						}
					} else if r.Header.Get("Authorization") != "Bearer fixture-key" {
						t.Error("missing bearer authentication")
					}
					var payload map[string]any
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
					}
					if payload["model"] != tc.model {
						t.Error("wrong model")
					}
					if tc.path == "/responses" {
						if payload["input"] != "hello" || payload["max_output_tokens"] != float64(32) {
							t.Error("wrong Responses payload")
						}
					} else if payload["messages"] == nil || payload["max_tokens"] != float64(32) {
						t.Error("missing native Messages/Chat payload")
					}
					if streaming && payload["stream"] != true {
						t.Error("stream flag missing")
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, tc.body)
				}))
				defer s.Close()
				c, _ := New("fixture-key")
				c.baseURL = s.URL
				var r *Result
				var err error
				var deltas string
				if streaming {
					r, err = c.CompleteStreaming(tc.model, "hello", 32, "session", func(text string) { deltas += text })
				} else {
					r, err = c.Complete(tc.model, "hello", 32, "session")
				}
				if err != nil || r.Text != "ok" || r.Outcome != "completed" || r.ResolvedModel != "resolved" {
					t.Fatalf("result = %+v, %v", r, err)
				}
				if r.Usage.Total == nil || *r.Usage.Total != 10 {
					t.Fatal("usage total was lost")
				}
				if streaming && deltas != "ok" {
					t.Fatal("JSON stream fallback did not publish text")
				}
			})
		}
	}
}

func TestStreamingRequiresTerminalEvidence(t *testing.T) {
	for _, tc := range []struct{ name, model, body string }{
		{"chat truncated", "glm-5.3-flash", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"},
		{"responses truncated", "gpt-6-luna", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"},
		{"messages truncated", "claude-haiku-5-5", "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"partial\"}}\n\n"},
		{"malformed frame", "glm-5.3-flash", "data: not-json\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"},
		{"scanner error", "glm-5.3-flash", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n" + strings.Repeat("x", 300*1024)},
		{"response failed", "gpt-6-luna", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":\"fixture-key\"}}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixtureClient(t, "text/event-stream", tc.body)
			result, err := c.CompleteStreaming(tc.model, "hello", 32, "session", nil)
			if err == nil || result != nil {
				t.Fatalf("invalid stream succeeded: %+v, %v", result, err)
			}
			if strings.Contains(err.Error(), "fixture-key") {
				t.Fatal("provider error leaked credentials")
			}
		})
	}
}

func TestMessagesStreamingPreservesStopReasonAndUsage(t *testing.T) {
	body := "data: {\"type\":\"message_start\",\"message\":{\"model\":\"resolved\",\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	c := fixtureClient(t, "text/event-stream", body)
	r, err := c.CompleteStreaming("claude-haiku-5-5", "hello", 32, "session", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "partial" || r.Outcome != "incomplete:max_tokens" || r.ResolvedModel != "resolved" {
		t.Fatalf("wrong messages result: %+v", r)
	}
	if r.Usage.Input == nil || *r.Usage.Input != 7 || r.Usage.Output == nil || *r.Usage.Output != 3 || r.Usage.Total == nil || *r.Usage.Total != 10 {
		t.Fatalf("lost incremental usage: %+v", r.Usage)
	}
}

func TestResponsesUsesCanonicalTerminalText(t *testing.T) {
	body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"provisional\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"model\":\"resolved\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"canonical\"}]}]}}\n\n"
	c := fixtureClient(t, "text/event-stream", body)
	r, err := c.CompleteStreaming("gpt-6-luna", "hello", 32, "session", nil)
	if err != nil || r.Text != "canonical" || r.ResolvedModel != "resolved" {
		t.Fatalf("terminal result = %+v, %v", r, err)
	}
}

func TestSSEMultilineData(t *testing.T) {
	body := "data: {\"choices\":\n" +
		"data: [{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	c := fixtureClient(t, "text/event-stream", body)
	r, err := c.CompleteStreaming("glm-5.3-flash", "hello", 32, "session", nil)
	if err != nil || r.Text != "ok" || r.Outcome != "completed" {
		t.Fatalf("multiline result = %+v, %v", r, err)
	}
}

func TestMalformedNonStreamingResponseRejected(t *testing.T) {
	for _, tc := range []struct{ model, body string }{
		{"glm-5.3-flash", `{}`},
		{"glm-5.3-flash", `{"choices":[{"finish_reason":"stop","message":{"content":123}}]}`},
		{"gpt-6-luna", `{"status":"failed","output":[]}`},
		{"claude-haiku-5-5", `{"content":[],"stop_reason":"unknown"}`},
	} {
		t.Run(tc.model+tc.body, func(t *testing.T) {
			c := fixtureClient(t, "application/json", tc.body)
			if result, err := c.Complete(tc.model, "hello", 32, "session"); err == nil || result != nil {
				t.Fatalf("malformed response succeeded: %+v, %v", result, err)
			}
		})
	}
}
