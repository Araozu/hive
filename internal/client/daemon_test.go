package client

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testDaemon(t *testing.T, handler http.HandlerFunc) *DaemonClient {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			_ = json.NewEncoder(w).Encode(HealthResponse{Service: ServiceName, APIVersion: APIVersion})
			return
		}
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	c, err := New(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func u32p(v uint32) *uint32 { return &v }

func TestEventCancellationUnblocksReaderAndClosesConnection(t *testing.T) {
	disconnected := make(chan struct{})
	c := testDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(disconnected)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := c.EventsContext(ctx, "session", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	readDone := make(chan error, 1)
	go func() { _, err := stream.Next(); readDone <- err }()
	cancel()
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("cancelled read succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled read remained blocked")
	}
	select {
	case <-disconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection remained open")
	}
}

func TestArtifactDownloadChecksSizeAndChecksum(t *testing.T) {
	want := "verified artifact"
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(want)))
	for _, content := range []string{want, "corrupt artifact!", want + "extra", "short"} {
		t.Run(content, func(t *testing.T) {
			c := testDaemon(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/content") {
					_, _ = w.Write([]byte(content))
					return
				}
				_ = json.NewEncoder(w).Encode(ArtifactResponse{ID: id, SHA256: id, SizeBytes: uint64(len(want))})
			})
			data, _, err := c.DownloadArtifact(id)
			if content == want {
				if err != nil || string(data) != want {
					t.Fatalf("valid artifact = %q, %v", data, err)
				}
			} else if err == nil || data != nil {
				t.Fatal("unverified artifact was published")
			}
		})
	}
}

func TestDiscoveryReturnsDecodedLists(t *testing.T) {
	c := testDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode([]ModelResponse{{ID: "model", Ready: true}})
		case "/v1/providers":
			_ = json.NewEncoder(w).Encode([]ProviderStatus{{Provider: "opencode-go"}})
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	})
	models, err := c.Models()
	if err != nil || len(models) != 1 || models[0].ID != "model" {
		t.Fatalf("models = %v, %v", models, err)
	}
	providers, err := c.Providers()
	if err != nil || len(providers) != 1 || providers[0].Provider != "opencode-go" {
		t.Fatalf("providers = %v, %v", providers, err)
	}
}

func TestRejectInvalidDaemonOrigin(t *testing.T) {
	for _, endpoint := range []string{"http://", "http:///", "http://example.com?", "http://example.com/#", "http://user@example.com", "http://example.com/path"} {
		t.Run(endpoint, func(t *testing.T) {
			if _, err := New(endpoint); err == nil {
				t.Fatal("accepted invalid origin")
			}
		})
	}
}

func TestHealthDoesNotFollowRedirect(t *testing.T) {
	target := testDaemon(t, func(w http.ResponseWriter, r *http.Request) {})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.endpoint+r.URL.Path, http.StatusFound)
	}))
	defer s.Close()
	c, err := New(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Health(); err == nil {
		t.Fatal("accepted identity supplied by redirected origin")
	}
}

func TestTurnPauseResumeAndSteeringRoutes(t *testing.T) {
	var routes []string
	c := testDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		routes = append(routes, r.Method+" "+r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/pause") || strings.HasSuffix(r.URL.Path, "/resume"):
			var req TurnControlRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CommandID == "" {
				t.Errorf("invalid turn control body: %v", req)
			}
			_ = json.NewEncoder(w).Encode(CommandReceipt{SessionID: "session"})
		case strings.HasSuffix(r.URL.Path, "/instructions"):
			_ = json.NewEncoder(w).Encode(Page[SteeringInstruction]{Items: []SteeringInstruction{{ID: "ins", TurnID: "turn", Delivery: DeliveryNextBoundary, Status: "accepted"}}})
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	})
	if _, err := c.PauseTurn("turn-1", &TurnControlRequest{CommandID: "cmd"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ResumeTurn("turn-1", &TurnControlRequest{CommandID: "cmd"}); err != nil {
		t.Fatal(err)
	}
	p, err := c.SteeringInstructions("session-1", nil, u32p(32))
	if err != nil || len(p.Items) != 1 || p.Items[0].Delivery != DeliveryNextBoundary {
		t.Fatalf("instructions = %v, %v", p, err)
	}
	want := []string{"POST /v1/turns/turn-1/pause", "POST /v1/turns/turn-1/resume", "GET /v1/sessions/session-1/instructions"}
	if fmt.Sprint(routes) != fmt.Sprint(want) {
		t.Fatalf("routes = %v, want %v", routes, want)
	}
}

func TestSendMessageCarriesDeliveryAndModelOverride(t *testing.T) {
	c := testDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		var req SendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("invalid send body: %v", err)
		}
		if req.Delivery == nil || *req.Delivery != DeliveryImmediate {
			t.Errorf("delivery = %v, want immediate", req.Delivery)
		}
		if req.Model == nil || *req.Model != "opencode-zen/model-x" {
			t.Errorf("model = %v, want override", req.Model)
		}
		_ = json.NewEncoder(w).Encode(CommandReceipt{SessionID: "session"})
	})
	d := DeliveryImmediate
	model := "opencode-zen/model-x"
	if _, err := c.SendMessage("session-1", &SendMessageRequest{CommandID: "cmd", Text: "stop", Delivery: &d, Model: &model}); err != nil {
		t.Fatal(err)
	}
}

func TestProviderIDValidatedBeforeSendingKey(t *testing.T) {
	c := testDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid provider caused a request")
		_ = json.NewEncoder(w).Encode(ProviderStatus{})
	})
	for _, id := range []string{"", "../codex", "opencode-go?query=", "opencode-go/extra"} {
		if _, err := c.SetProviderKey(id, &SetAPIKeyRequest{APIKey: "secret"}); err == nil {
			t.Errorf("accepted provider %q", id)
		}
	}
}

func TestCredentialsAreRedactedFromDaemonErrors(t *testing.T) {
	token, key := strings.Repeat("a", 64), "test-private-provider-key"
	c := testDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: token, Message: "Bearer " + token + " " + key})
	})
	c.token, c.hasToken = token, true
	_, err := c.SetProviderKey("opencode-go", &SetAPIKeyRequest{APIKey: key})
	if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), key) {
		t.Fatalf("credential error was not redacted: %v", err)
	}
}

func TestRejectPublicTokenFile(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("Unix permissions")
	}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWithTokenFile("http://localhost:7331", path); err == nil {
		t.Fatal("accepted a world-readable token file")
	}
}
