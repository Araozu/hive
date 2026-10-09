package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"hive/internal/client"
	"hive/internal/config"
	"hive/internal/provider"
)

func testModel(t *testing.T, handler http.HandlerFunc) Model {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			_ = json.NewEncoder(w).Encode(client.HealthResponse{Service: client.ServiceName, APIVersion: client.APIVersion})
			return
		}
		if handler != nil {
			handler(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(s.Close)
	m, err := New(config.Config{DaemonURL: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.stopFollow() })
	return m
}

func TestLatestHistoryResponseWins(t *testing.T) {
	m := testModel(t, nil)
	m.selectChat("session")
	m.cmdHistory()
	oldRequest := m.chatHistoryRequest
	m.cmdHistory()
	m, _ = updateModel(m, historyMsg{session: "session", generation: m.chatGeneration, request: m.chatHistoryRequest,
		p: &client.Page[client.MessageResponse]{Items: []client.MessageResponse{{Role: "assistant", Text: "latest"}}}})
	m, _ = updateModel(m, historyMsg{session: "session", generation: m.chatGeneration, request: oldRequest,
		p: &client.Page[client.MessageResponse]{Items: []client.MessageResponse{{Role: "assistant", Text: "stale"}}}})
	if !strings.Contains(m.chatText, "latest") || strings.Contains(m.chatText, "stale") {
		t.Fatal("out-of-order history response overwrote canonical text")
	}
}

func TestArtifactPublishDoesNotFollowOutputSymlink(t *testing.T) {
	dir := t.TempDir()
	target, out := filepath.Join(dir, "target"), filepath.Join(dir, "artifact.bin")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, out); err != nil {
		t.Skip(err)
	}
	if err := writeArtifact(out, []byte("verified")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "original" {
		t.Fatal("download overwrote symlink target")
	}
	data, err = os.ReadFile(out)
	if err != nil || string(data) != "verified" {
		t.Fatal("verified artifact was not published")
	}
}

func updateModel(m Model, msg tea.Msg) (Model, tea.Cmd) {
	model, cmd := m.Update(msg)
	return model.(Model), cmd
}

func press(m Model, key tea.KeyMsg) Model {
	m, _ = updateModel(m, key)
	return m
}

func TestInvalidConfigurationReturnsError(t *testing.T) {
	if _, err := New(config.Config{DaemonURL: "invalid"}); err == nil {
		t.Fatal("invalid config would panic on Init")
	}
}

func TestFocusNavigationWhileEditing(t *testing.T) {
	m := testModel(t, nil)
	m.setTab(TabProviders)
	m = press(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]")})
	if !m.keyValue.Focused() || m.keyProvider.Focused() || m.focusField != "keyval" {
		t.Fatal("provider focus did not reach key field")
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyF3})
	if !m.loginID.Focused() || m.keyValue.Focused() {
		t.Fatal("F3 did not select login field")
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.tab != TabGoDirect || m.isEditing() {
		t.Fatal("tab did not leave focused provider input")
	}
	m.setTab(TabChat)
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.chatBox.Focused() || m.chatSession.Focused() {
		t.Fatal("session Enter did not focus the message editor")
	}
	m.setTab(TabTurns)
	m = press(m, tea.KeyMsg{Type: tea.KeyF2})
	if !m.inspectInput.Focused() || m.turnInput.Focused() {
		t.Fatal("inspect input is unreachable")
	}
	m.setTab(TabArtifacts)
	m = press(m, tea.KeyMsg{Type: tea.KeyEsc})
	m = press(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if !m.artInput.Focused() {
		t.Fatal("artifact input cannot be edited again after blur")
	}
}

func TestChangingSessionResetsObserverAndRejectsStaleHistory(t *testing.T) {
	m := testModel(t, nil)
	m.selectChat("old")
	m.chatCursor, m.chatTurn, m.chatActive = 100, "turn-old", true
	m.appendChat("old history")
	oldGeneration := m.chatGeneration
	cancelled := false
	m.followCtx = func() { cancelled = true }
	m.selectChat("new")
	if !cancelled || m.chatCursor != 0 || m.chatTurn != "" || m.chatActive || m.chatText != "" {
		t.Fatal("new session retained old observation state")
	}
	m, _ = updateModel(m, historyMsg{session: "old", generation: oldGeneration,
		p: &client.Page[client.MessageResponse]{Items: []client.MessageResponse{{Text: "stale"}}}})
	if m.chatText != "" {
		t.Fatal("old session history overwrote new session")
	}
}

func TestChatCreateSendRetainsSessionAndDraftOnFailure(t *testing.T) {
	for _, sendFails := range []bool{false, true} {
		t.Run(fmt.Sprint(sendFails), func(t *testing.T) {
			m := testModel(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/sessions":
					_ = json.NewEncoder(w).Encode(client.CommandReceipt{SessionID: "created"})
				case "/v1/sessions/created/messages":
					if sendFails {
						w.WriteHeader(http.StatusConflict)
						_ = json.NewEncoder(w).Encode(client.ErrorResponse{Code: "conflict", Message: "busy"})
						return
					}
					_ = json.NewEncoder(w).Encode(client.CommandReceipt{SessionID: "created"})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			})
			m.chatBox.SetValue("hello")
			msg := m.cmdChatSend("hello")()
			m, _ = updateModel(m, msg)
			if m.chatID != "created" || m.chatSession.Value() != "created" || m.chatSending {
				t.Fatal("created session was lost")
			}
			if sendFails && m.chatBox.Value() != "hello" {
				t.Fatal("failed send lost the draft")
			}
			if !sendFails && m.chatBox.Value() != "" {
				t.Fatal("successful send retained submitted draft")
			}
		})
	}
}

func TestReceiptStartsFollowingReturnedSession(t *testing.T) {
	m := testModel(t, nil)
	turn := "turn"
	m, _ = updateModel(m, receiptMsg{r: &client.CommandReceipt{SessionID: "new", TurnID: &turn, EventSequence: 4}})
	defer m.stopFollow()
	if m.chatID != "new" || !m.following || !m.chatActive || m.chatCursor != 4 {
		t.Fatal("receipt did not start observing its session")
	}
}

func TestTranscriptSurvivesScrollingAndCanonicalCompletion(t *testing.T) {
	m := testModel(t, nil)
	m.chatView.Height = 2
	m.selectChat("session")
	m.appendChat("first\nsecond\nthird\n")
	m.chatView.GotoBottom()
	m.appendChat("fourth")
	if !strings.HasPrefix(m.chatText, "first\nsecond") {
		t.Fatal("appending visible viewport discarded earlier lines")
	}
	m.chatChunks = make(map[string]uint64)
	delta := &client.EventFrame{Type: "delta", SessionID: "session", StreamID: "stream", ChunkIndex: 1, Text: " delta"}
	m.applyFrame(delta)
	m.applyFrame(delta)
	if strings.Count(m.chatText, " delta") != 1 {
		t.Fatal("duplicate chunk rendered twice")
	}
	cmd := m.applyFrame(&client.EventFrame{Type: "durable", Event: &client.EventResponse{SessionID: "session", Sequence: 1, Kind: "turn_completed"}})
	if cmd == nil {
		t.Fatal("terminal event did not reconcile canonical history")
	}
	m.goRunPrefix = "previous\nrequest\n"
	m.appendGo(m.goRunPrefix + "provisional")
	m, _ = updateModel(m, goResultMsg{r: &provider.Result{Text: "canonical", Outcome: "completed"}})
	if m.goText != m.goRunPrefix+"canonical\n" {
		t.Fatalf("canonical result not displayed: %q", m.goText)
	}
}

func TestHistoryFollowsPagination(t *testing.T) {
	m := testModel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/session/messages" {
			t.Errorf("unexpected route %s", r.URL.Path)
		}
		next := uint64(1)
		page := client.Page[client.MessageResponse]{Items: []client.MessageResponse{{Role: "user", Text: "first"}}, NextAfter: &next}
		if r.URL.Query().Get("after") == "1" {
			page = client.Page[client.MessageResponse]{Items: []client.MessageResponse{{Role: "assistant", Text: "latest"}}}
		}
		_ = json.NewEncoder(w).Encode(page)
	})
	m.selectChat("session")
	m, _ = updateModel(m, m.cmdHistory()())
	if len(m.history) != 2 || !strings.Contains(m.chatText, "latest") {
		t.Fatal("history stopped at first page")
	}
}

func TestDirectOutputDrainsWithoutChat(t *testing.T) {
	m := testModel(t, nil)
	m.goWorking = true
	m.goCh <- "hello"
	m, cmd := updateModel(m, frameDrainMsg{})
	if m.goText != "hello" || cmd == nil {
		t.Fatal("direct stream did not drain or schedule next tick")
	}
	m, _ = updateModel(m, goModelsMsg{err: errors.New("discovery failed")})
	if !m.goWorking {
		t.Fatal("model discovery cleared active request guard")
	}
}

func TestQuitClosesObserver(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune("q")}, {Type: tea.KeyCtrlC}} {
		m := testModel(t, nil)
		cancelled := false
		m.followCtx = func() { cancelled = true }
		m = press(m, key)
		if !cancelled {
			t.Fatal("quit left observation stream open")
		}
	}
}

func TestTruncatePreservesUTF8(t *testing.T) {
	if text := truncate("a☃b", 2); text != "a…" || !utf8.ValidString(text) {
		t.Fatalf("invalid Unicode truncation: %q", text)
	}
}

func TestViewsFitTerminalAndExposeDirectPrompt(t *testing.T) {
	m := testModel(t, nil)
	m.gopro, _ = provider.New("fixture-key")
	for _, size := range []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 120, Height: 40}} {
		m, _ = updateModel(m, size)
		for tab := range TabCount {
			m.setTab(tab)
			view := m.View()
			if lipgloss.Height(view) > size.Height || lipgloss.Width(view) > size.Width {
				t.Errorf("%s exceeded %dx%d: %dx%d", tabNames[tab], size.Width, size.Height, lipgloss.Width(view), lipgloss.Height(view))
			}
			if tab == TabGoDirect && !strings.Contains(view, "prompt:") {
				t.Error("catalog pushed prompt off screen")
			}
		}
	}
}
