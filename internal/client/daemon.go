// Daemon HTTP transport: health, node, models, sessions, turns,
// artifacts, providers, and NDJSON event streams.
package client

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	maxTokenFileBytes = 66
	maxResponseBytes  = 64 * 1024
	maxJSONBytes      = 16 * 1024 * 1024
	maxArtifactBytes  = 64 * 1024 * 1024
)

// APIError is a structured daemon error.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("daemon HTTP %d (%s): %s", e.Status, e.Code, e.Message)
}

// Unauthorized reports 401s separately.
type Unauthorized struct{ APIError }

func (e *Unauthorized) Error() string { return e.APIError.Error() }

// DaemonClient talks to one slopconductor daemon origin.
type DaemonClient struct {
	http     *http.Client
	stream   *http.Client
	endpoint string
	token    string
	hasToken bool
}

// New builds an anonymous client (health only is useful without a token).
func New(endpoint string) (*DaemonClient, error) {
	return withToken(endpoint, "", false)
}

// NewWithTokenFile reads a private 64-hex token file.
func NewWithTokenFile(endpoint, path string) (*DaemonClient, error) {
	tok, err := readTokenFile(path)
	if err != nil {
		return nil, err
	}
	return withToken(endpoint, tok, true)
}

func withToken(endpoint, token string, has bool) (*DaemonClient, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid daemon URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("daemon URL must use http or https")
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("daemon URL must be an origin without path/query/fragment/credentials")
	}
	base := strings.TrimSuffix(endpoint, "/")
	return &DaemonClient{
		http:     &http.Client{Timeout: 10 * time.Second},
		stream:   &http.Client{},
		endpoint: base,
		token:    token,
		hasToken: has,
	}, nil
}

func (c *DaemonClient) auth(req *http.Request) {
	if c.hasToken {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func readBounded(resp *http.Response, limit int) ([]byte, error) {
	defer resp.Body.Close()
	var buf bytes.Buffer
	lr := io.LimitReader(resp.Body, int64(limit)+1)
	if _, err := io.Copy(&buf, lr); err != nil {
		return nil, fmt.Errorf("daemon request failed: %w", err)
	}
	if buf.Len() > limit {
		return nil, fmt.Errorf("daemon returned an invalid or oversized response")
	}
	return buf.Bytes(), nil
}

func apiError(status int, body []byte) error {
	var e ErrorResponse
	if err := json.Unmarshal(body, &e); err != nil {
		e = ErrorResponse{Code: "http_error", Message: "daemon request failed"}
	}
	if status == 401 {
		return &Unauthorized{APIError{Status: status, Code: e.Code, Message: e.Message}}
	}
	return &APIError{Status: status, Code: e.Code, Message: e.Message}
}

func checkID(id string) error {
	if id == "" {
		return fmt.Errorf("identifier must not be empty")
	}
	for _, b := range []byte(id) {
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_') {
			return fmt.Errorf("identifier must contain only ASCII letters, digits, hyphens, or underscores")
		}
	}
	return nil
}

// NewCommandID generates one 16-byte hex command ID per intended mutation.
func NewCommandID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("could not generate a unique command ID")
	}
	return hex.EncodeToString(b[:]), nil
}

func readTokenFile(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("could not read token file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("token file must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("could not read token file: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return "", fmt.Errorf("token file must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxTokenFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("could not read token file: %w", err)
	}
	if len(data) > maxTokenFileBytes {
		return "", fmt.Errorf("token file must contain exactly 64 hexadecimal characters and an optional newline")
	}
	s := string(data)
	s = strings.TrimSuffix(s, "\r\n")
	s = strings.TrimSuffix(s, "\n")
	if len(s) != 64 {
		return "", fmt.Errorf("token file must contain exactly 64 hexadecimal characters and an optional newline")
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return "", fmt.Errorf("token file must contain exactly 64 hexadecimal characters and an optional newline")
		}
	}
	return s, nil
}

// Health is anonymous; it also validates service + API version.
func (c *DaemonClient) Health() (*HealthResponse, error) {
	resp, err := c.http.Get(c.endpoint + "/v1/health")
	if err != nil {
		return nil, fmt.Errorf("daemon request failed: %w", err)
	}
	body, err := readBounded(resp, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, apiError(resp.StatusCode, body)
	}
	var h HealthResponse
	if err := json.Unmarshal(body, &h); err != nil {
		return nil, fmt.Errorf("daemon returned an invalid or oversized response")
	}
	if h.Service != ServiceName {
		return nil, fmt.Errorf("unexpected daemon service: %s", h.Service)
	}
	if h.APIVersion != APIVersion {
		return nil, fmt.Errorf("incompatible daemon API: expected %d, received %d", APIVersion, h.APIVersion)
	}
	return &h, nil
}

func (c *DaemonClient) getJSON(path string, out any) error {
	if _, err := c.Health(); err != nil {
		return err
	}
	req, err := http.NewRequest("GET", c.endpoint+path, nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("daemon request failed: %w", err)
	}
	body, err := readBounded(resp, maxJSONBytes)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiError(resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("daemon returned an invalid or oversized response")
	}
	return nil
}

func (c *DaemonClient) mutateJSON(method, path string, in, out any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("daemon returned an invalid or oversized response")
	}
	if len(raw) > maxJSONBytes {
		return fmt.Errorf("daemon request body exceeds the configured size limit")
	}
	if _, err := c.Health(); err != nil {
		return err
	}
	req, err := http.NewRequest(method, c.endpoint+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.auth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("mutation delivery outcome is unknown: %v", err)
	}
	body, err := readBounded(resp, maxJSONBytes)
	if err != nil {
		return fmt.Errorf("mutation delivery outcome is unknown: %v", err)
	}
	if resp.StatusCode != 200 && resp.StatusCode != 202 && resp.StatusCode != 201 {
		// Daemon uses 202 for accepted mutations and 200 for reads/key set.
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return apiError(resp.StatusCode, body)
		}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("mutation delivery outcome is unknown: daemon accepted a mutation but returned an invalid response")
	}
	return nil
}

// Node queries the authenticated identity (health first so no token leaks).
func (c *DaemonClient) Node() (*NodeResponse, error) {
	var n NodeResponse
	return &n, c.getJSON("/v1/node", &n)
}

func (c *DaemonClient) Models() ([]ModelResponse, error) {
	var m []ModelResponse
	return m, c.getJSON("/v1/models", &m)
}

func (c *DaemonClient) Capabilities() (*CapabilitiesResponse, error) {
	var r CapabilitiesResponse
	return &r, c.getJSON("/v1/capabilities", &r)
}

func (c *DaemonClient) ListSessions(after *uint64, limit *uint32) (*Page[SessionResponse], error) {
	var p Page[SessionResponse]
	return &p, c.getJSON("/v1/sessions"+pageQuery(after, limit), &p)
}

func (c *DaemonClient) Session(id string) (*SessionResponse, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	var s SessionResponse
	return &s, c.getJSON("/v1/sessions/"+id, &s)
}

func (c *DaemonClient) CreateSession(req *CreateSessionRequest) (*CommandReceipt, error) {
	var r CommandReceipt
	return &r, c.mutateJSON("POST", "/v1/sessions", req, &r)
}

func (c *DaemonClient) History(id string, after *uint64, limit *uint32) (*Page[MessageResponse], error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	var p Page[MessageResponse]
	return &p, c.getJSON("/v1/sessions/"+id+"/messages"+pageQuery(after, limit), &p)
}

func (c *DaemonClient) SendMessage(id string, req *SendMessageRequest) (*CommandReceipt, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	var r CommandReceipt
	return &r, c.mutateJSON("POST", "/v1/sessions/"+id+"/messages", req, &r)
}

func (c *DaemonClient) Turn(id string) (*TurnResponse, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	var t TurnResponse
	return &t, c.getJSON("/v1/turns/"+id, &t)
}

func (c *DaemonClient) CancelTurn(id string, req *CancelTurnRequest) (*CommandReceipt, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	var r CommandReceipt
	return &r, c.mutateJSON("POST", "/v1/turns/"+id+"/cancel", req, &r)
}

func (c *DaemonClient) ModelRequests(turn string, after *uint64, limit *uint32) (*Page[ModelRequestResponse], error) {
	if err := checkID(turn); err != nil {
		return nil, err
	}
	var p Page[ModelRequestResponse]
	return &p, c.getJSON("/v1/turns/"+turn+"/requests"+pageQuery(after, limit), &p)
}

func (c *DaemonClient) ToolInvocations(turn string, after *uint64, limit *uint32) (*Page[ToolInvocationResponse], error) {
	if err := checkID(turn); err != nil {
		return nil, err
	}
	var p Page[ToolInvocationResponse]
	return &p, c.getJSON("/v1/turns/"+turn+"/tools"+pageQuery(after, limit), &p)
}

func (c *DaemonClient) ToolInvocation(id string) (*ToolInvocationResponse, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	var t ToolInvocationResponse
	return &t, c.getJSON("/v1/tools/"+id, &t)
}

func (c *DaemonClient) Message(id string) (*MessageResponse, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	var m MessageResponse
	return &m, c.getJSON("/v1/messages/"+id, &m)
}

func (c *DaemonClient) Artifact(id string) (*ArtifactResponse, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	var a ArtifactResponse
	return &a, c.getJSON("/v1/artifacts/"+id, &a)
}

// DownloadArtifact streams bytes and verifies size + SHA-256 at EOF.
func (c *DaemonClient) DownloadArtifact(id string) ([]byte, *ArtifactResponse, error) {
	meta, err := c.Artifact(id)
	if err != nil {
		return nil, nil, err
	}
	if meta.ID != id || meta.SHA256 != id || meta.SizeBytes > maxArtifactBytes {
		return nil, nil, fmt.Errorf("daemon returned an invalid or oversized response")
	}
	if _, err := c.Health(); err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequest("GET", c.endpoint+"/v1/artifacts/"+id+"/content", nil)
	if err != nil {
		return nil, nil, err
	}
	c.auth(req)
	resp, err := c.stream.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("daemon request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		return nil, nil, apiError(resp.StatusCode, body)
	}
	h := sha256.New()
	var buf bytes.Buffer
	var received uint64
	chunk := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(chunk)
		if n > 0 {
			received += uint64(n)
			if received > meta.SizeBytes {
				return nil, nil, fmt.Errorf("artifact data does not match its committed size and checksum")
			}
			h.Write(chunk[:n])
			buf.Write(chunk[:n])
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, nil, fmt.Errorf("daemon request failed: %w", rerr)
		}
	}
	if received != meta.SizeBytes || hex.EncodeToString(h.Sum(nil)) != meta.SHA256 {
		return nil, nil, fmt.Errorf("artifact data does not match its committed size and checksum")
	}
	return buf.Bytes(), meta, nil
}

func (c *DaemonClient) Providers() ([]ProviderStatus, error) {
	var p []ProviderStatus
	return p, c.getJSON("/v1/providers", &p)
}

func (c *DaemonClient) SetProviderKey(provider string, req *SetAPIKeyRequest) (*ProviderStatus, error) {
	if err := checkID(provider); err != nil {
		// provider ids contain hyphens only; reuse check with dashes allowed.
		if provider == "" {
			return nil, err
		}
	}
	var s ProviderStatus
	return &s, c.mutateJSON("PUT", "/v1/providers/"+provider+"/api-key", req, &s)
}

func (c *DaemonClient) StartCodexLogin(req *StartLoginRequest) (*LoginResponse, error) {
	var r LoginResponse
	return &r, c.mutateJSON("POST", "/v1/providers/codex/login", req, &r)
}

func (c *DaemonClient) CodexLoginStatus(loginID string) (*LoginStatus, error) {
	if err := checkID(loginID); err != nil {
		return nil, err
	}
	var s LoginStatus
	return &s, c.getJSON("/v1/providers/codex/login/"+loginID, &s)
}

func pageQuery(after *uint64, limit *uint32) string {
	v := url.Values{}
	if after != nil {
		v.Set("after", fmt.Sprint(*after))
	}
	if limit != nil {
		v.Set("limit", fmt.Sprint(*limit))
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

// EventStream reads the NDJSON events endpoint line by line.
type EventStream struct {
	resp    *http.Response
	scanner *bufio.Scanner
}

// Events opens GET /v1/sessions/{id}/events?after=&follow=.
func (c *DaemonClient) Events(sessionID string, after uint64, follow bool) (*EventStream, error) {
	if err := checkID(sessionID); err != nil {
		return nil, err
	}
	if _, err := c.Health(); err != nil {
		return nil, err
	}
	u := fmt.Sprintf("%s/v1/sessions/%s/events?after=%d&follow=%t", c.endpoint, sessionID, after, follow)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.stream.Do(req)
	if err != nil {
		return nil, fmt.Errorf("daemon request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		resp.Body.Close()
		return nil, apiError(resp.StatusCode, body)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 2*1024*1024), 2*1024*1024)
	return &EventStream{resp: resp, scanner: sc}, nil
}

// Next blocks for the next frame; io.EOF means the stream ended (follow=false).
func (s *EventStream) Next() (*EventFrame, error) {
	for s.scanner.Scan() {
		line := bytes.TrimSpace(s.scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var f EventFrame
		if err := json.Unmarshal(line, &f); err != nil {
			return nil, fmt.Errorf("daemon event stream returned an invalid or oversized frame")
		}
		return &f, nil
	}
	if err := s.scanner.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

// Close releases the stream.
func (s *EventStream) Close() error { return s.resp.Body.Close() }
