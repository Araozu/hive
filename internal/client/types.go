// Package client speaks the slopconductor daemon public API (v1).
// It mirrors slop-client semantics: anonymous health first, bearer auth
// on protected routes, API version check, bounded bodies, id validation,
// random command IDs, and SHA-256 verified artifact downloads.
package client

import "encoding/json"

// Wire constants shared with slop-protocol.
const (
	APIVersion       = 1
	ServiceName      = "slopconductor-daemon"
	DefaultDaemonURL = "http://127.0.0.1:7331"
)

type HealthResponse struct {
	Service      string   `json:"service"`
	Version      string   `json:"version"`
	APIVersion   uint32   `json:"api_version"`
	Capabilities []string `json:"capabilities"`
}

type NodeResponse struct {
	NodeID string `json:"node_id"`
	Name   string `json:"name"`
	OS     string `json:"os"`
}

type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type GenerationSettings struct {
	MaxOutputTokens *uint32 `json:"max_output_tokens,omitempty"`
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
}

type WorkspacePolicy struct {
	Root             string   `json:"root"`
	AllowedTools     []string `json:"allowed_tools"`
	ShellTimeoutMs   uint64   `json:"shell_timeout_ms"`
	MaxOutputBytes   int      `json:"max_output_bytes"`
	MaxToolCalls     uint32   `json:"max_tool_calls"`
	MaxModelRequests uint32   `json:"max_model_requests"`
}

type SessionResponse struct {
	ID           string             `json:"id"`
	OwnerNodeID  string             `json:"owner_node_id"`
	Title        *string            `json:"title,omitempty"`
	Provider     string             `json:"provider"`
	Model        string             `json:"model"`
	MaxTokens    *uint32            `json:"max_tokens,omitempty"`
	Revision     uint64             `json:"revision"`
	LastEventSeq uint64             `json:"last_event_sequence"`
	Settings     GenerationSettings `json:"settings"`
	Execution    *WorkspacePolicy   `json:"execution,omitempty"`
}

type CreateSessionRequest struct {
	CommandID string              `json:"command_id"`
	Title     *string             `json:"title,omitempty"`
	Provider  string              `json:"provider"`
	Model     string              `json:"model"`
	MaxTokens *uint32             `json:"max_tokens,omitempty"`
	Settings  *GenerationSettings `json:"settings,omitempty"`
	Execution *WorkspacePolicy    `json:"execution,omitempty"`
}

type SendMessageRequest struct {
	CommandID        string              `json:"command_id"`
	Text             string              `json:"text"`
	ExpectedRevision *uint64             `json:"expected_revision,omitempty"`
	Model            *string             `json:"model,omitempty"`
	Settings         *GenerationSettings `json:"settings,omitempty"`
}

type CancelTurnRequest struct {
	CommandID string `json:"command_id"`
}

type CommandReceipt struct {
	CommandID     string  `json:"command_id"`
	SessionID     string  `json:"session_id"`
	TurnID        *string `json:"turn_id,omitempty"`
	MessageID     *string `json:"message_id,omitempty"`
	Revision      uint64  `json:"revision"`
	EventSequence uint64  `json:"event_sequence"`
}

type ContentBlock struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Text    string          `json:"text,omitempty"`
	CallID  string          `json:"call_id,omitempty"`
	Name    string          `json:"name,omitempty"`
	Args    json.RawMessage `json:"arguments,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
	Output  string          `json:"output,omitempty"`
}

type MessageResponse struct {
	ID        string         `json:"id"`
	SessionID string         `json:"session_id"`
	TurnID    string         `json:"turn_id"`
	Role      string         `json:"role"`
	Text      string         `json:"text"`
	Status    string         `json:"status"`
	Blocks    []ContentBlock `json:"blocks"`
	RequestID *string        `json:"request_id,omitempty"`
}

type UsageResponse struct {
	InputTokens  *uint64 `json:"input_tokens,omitempty"`
	OutputTokens *uint64 `json:"output_tokens,omitempty"`
	TotalTokens  *uint64 `json:"total_tokens,omitempty"`
	TotalSource  *string `json:"total_source,omitempty"`
}

type TurnResponse struct {
	ID                 string             `json:"id"`
	SessionID          string             `json:"session_id"`
	UserMessageID      string             `json:"user_message_id"`
	AssistantMessageID *string            `json:"assistant_message_id,omitempty"`
	Status             string             `json:"status"`
	RequestedModel     string             `json:"requested_model"`
	ResolvedModel      *string            `json:"resolved_model,omitempty"`
	Usage              *UsageResponse     `json:"usage,omitempty"`
	ErrorCode          *string            `json:"error_code,omitempty"`
	ErrorMessage       *string            `json:"error_message,omitempty"`
	Settings           GenerationSettings `json:"settings"`
	ModelRequestIDs    []string           `json:"model_request_ids"`
	ToolInvocationIDs  []string           `json:"tool_invocation_ids"`
}

type Page[T any] struct {
	Items     []T     `json:"items"`
	NextAfter *uint64 `json:"next_after,omitempty"`
}

type EventResponse struct {
	SessionID    string  `json:"session_id"`
	Sequence     uint64  `json:"sequence"`
	Kind         string  `json:"kind"`
	TurnID       *string `json:"turn_id,omitempty"`
	MessageID    *string `json:"message_id,omitempty"`
	Revision     uint64  `json:"revision"`
	RequestID    *string `json:"request_id,omitempty"`
	InvocationID *string `json:"invocation_id,omitempty"`
}

// EventFrame is the NDJSON envelope: durable / delta / heartbeat.
type EventFrame struct {
	Type       string         `json:"type"`
	Event      *EventResponse `json:"event,omitempty"`
	SessionID  string         `json:"session_id,omitempty"`
	TurnID     string         `json:"turn_id,omitempty"`
	Text       string         `json:"text,omitempty"`
	MessageID  *string        `json:"message_id,omitempty"`
	BlockID    *string        `json:"block_id,omitempty"`
	RequestID  *string        `json:"request_id,omitempty"`
	InvocID    *string        `json:"invocation_id,omitempty"`
	StreamID   string         `json:"stream_id,omitempty"`
	ChunkIndex uint64         `json:"chunk_index,omitempty"`
	Kind       string         `json:"kind,omitempty"`
}

type ModelCapabilities struct {
	Tools             bool     `json:"tools"`
	ReasoningEfforts  []string `json:"reasoning_efforts"`
	IncrementalStream bool     `json:"incremental_streaming"`
	MaxOutputTokens   uint32   `json:"max_output_tokens"`
}

type ModelResponse struct {
	ID           string            `json:"id"`
	Provider     string            `json:"provider"`
	Model        string            `json:"model"`
	DisplayName  string            `json:"display_name"`
	Ready        bool              `json:"ready"`
	IsDefault    bool              `json:"is_default"`
	Reason       *string           `json:"reason,omitempty"`
	Capabilities ModelCapabilities `json:"capabilities"`
}

type ToolDescriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	SideEffects string          `json:"side_effects"`
}

type CapabilitiesResponse struct {
	StructuredMessages    bool             `json:"structured_messages"`
	PerTurnModelSelection bool             `json:"per_turn_model_selection"`
	PerTurnSettings       bool             `json:"per_turn_settings"`
	Tools                 []ToolDescriptor `json:"tools"`
	MaxModelRequests      uint32           `json:"max_model_requests_per_turn"`
	MaxToolCalls          uint32           `json:"max_tool_calls_per_turn"`
}

type ModelRequestResponse struct {
	ID                string             `json:"id"`
	TurnID            string             `json:"turn_id"`
	MessageID         string             `json:"message_id"`
	Ordinal           uint32             `json:"ordinal"`
	RequestedModel    string             `json:"requested_model"`
	ResolvedModel     *string            `json:"resolved_model,omitempty"`
	RequestedSettings GenerationSettings `json:"requested_settings"`
	EffectiveSettings GenerationSettings `json:"effective_settings"`
	Status            string             `json:"status"`
	FinishReason      *string            `json:"finish_reason,omitempty"`
	Usage             *UsageResponse     `json:"usage,omitempty"`
	ErrorCode         *string            `json:"error_code,omitempty"`
}

type ToolInvocationResponse struct {
	ID             string          `json:"id"`
	TurnID         string          `json:"turn_id"`
	RequestID      string          `json:"request_id"`
	CallID         string          `json:"call_id"`
	Name           string          `json:"name"`
	Arguments      json.RawMessage `json:"arguments"`
	Status         string          `json:"status"`
	Output         *string         `json:"output,omitempty"`
	ArtifactIDs    []string        `json:"artifact_ids"`
	ErrorCode      *string         `json:"error_code,omitempty"`
	EffectsUnknown bool            `json:"effects_unknown"`
}

type ArtifactResponse struct {
	ID        string `json:"id"`
	SHA256    string `json:"sha256"`
	SizeBytes uint64 `json:"size_bytes"`
	MediaType string `json:"media_type"`
}

type ProviderStatus struct {
	Provider           string `json:"provider"`
	APIKeyConfigured   bool   `json:"api_key_configured"`
	ChatGPTConfigured  bool   `json:"chatgpt_configured"`
	ExecutionSupported bool   `json:"execution_supported"`
}

type SetAPIKeyRequest struct {
	APIKey string `json:"api_key"`
}

type StartLoginRequest struct {
	CommandID string `json:"command_id"`
}

type LoginResponse struct {
	LoginID          string `json:"login_id"`
	AuthorizationURL string `json:"authorization_url"`
}

type LoginStatus struct {
	LoginID   string  `json:"login_id"`
	Status    string  `json:"status"`
	ErrorCode *string `json:"error_code,omitempty"`
}

// IsTerminal mirrors slop-cli follow.rs.
func IsTerminal(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "canceled",
		"interrupted", "incomplete", "rejected":
		return true
	}
	return false
}
