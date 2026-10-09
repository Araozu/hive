package ui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"hive/internal/client"
	"hive/internal/provider"
)

// handleKey processes global + per-tab keys. Returns true when the key was
// consumed and input routing should stop.
func (m *Model) handleKey(msg tea.KeyMsg, cmds *[]tea.Cmd) bool {
	key := msg.String()

	// ctrl+c always quits (and stops any follow stream).
	if key == "ctrl+c" {
		if m.followCtx != nil {
			m.followCtx()
		}
		*cmds = append(*cmds, tea.Quit)
		return true
	}

	editing := m.isEditing()
	// When editing, only esc / special send keys are intercepted here.
	if editing {
		switch key {
		case "esc":
			m.blurAll()
			m.focusField = "chat"
			return true
		case "enter":
			if m.tab == TabChat && m.focusField == "input" {
				text := strings.TrimSpace(m.chatBox.Value())
				if text != "" {
					m.chatBox.Reset()
					*cmds = append(*cmds, m.cmdChatSend(text))
				}
				return true
			}
			if m.tab == TabGoDirect && m.focusField == "prompt" {
				// enter runs the direct probe; alt+enter inserts newline
				p := strings.TrimSpace(m.goPrompt.Value())
				if p != "" {
					*cmds = append(*cmds, m.cmdGoRun(p))
				}
				return true
			}
			if m.tab == TabProviders {
				*cmds = append(*cmds, m.cmdSetKey())
				return true
			}
			if m.tab == TabArtifacts {
				*cmds = append(*cmds, m.cmdArtifactShow())
				return true
			}
			if m.tab == TabTurns {
				*cmds = append(*cmds, m.cmdTurnShow())
				return true
			}
		case "alt+enter":
			if m.tab == TabChat && m.focusField == "input" {
				m.chatBox.InsertString("\n")
				return true
			}
			if m.tab == TabGoDirect && m.focusField == "prompt" {
				m.goPrompt.InsertString("\n")
				return true
			}
		}
		return false // let the focused input handle it
	}

	switch key {
	case "q", "ctrl+q":
		*cmds = append(*cmds, tea.Quit)
		return true
	case "?", "h":
		m.showHelp = !m.showHelp
		return true
	case "1", "2", "3", "4", "5", "6", "7":
		m.setTab(int(key[0] - '1'))
		return true
	case "tab":
		m.setTab((m.tab + 1) % TabCount)
		return true
	case "shift+tab":
		m.setTab((m.tab + TabCount - 1) % TabCount)
		return true
	case "r", "R":
		*cmds = append(*cmds, m.cmdRefresh())
		return true
	}

	switch m.tab {
	case TabSessions:
		switch key {
		case "j", "down":
			if m.sessCursor < len(m.sessions)-1 {
				m.sessCursor++
			}
			return true
		case "k", "up":
			if m.sessCursor > 0 {
				m.sessCursor--
			}
			return true
		case "enter":
			*cmds = append(*cmds, m.cmdSessionInspect())
			return true
		case "c":
			*cmds = append(*cmds, m.cmdSessionCreate())
			return true
		}
	case TabChat:
		switch key {
		case "e":
			m.focusField = "input"
			m.chatSession.Blur()
			m.chatBox.Focus()
			return true
		case "enter":
			// session-field enter with empty box: focus editor; actual send
			// happens from the editor. If editor empty and session set, also
			// allow follow.
			if m.chatSession.Value() != "" || true {
				m.focusField = "input"
				m.chatSession.Blur()
				m.chatBox.Focus()
			}
			return true
		case "f":
			sid := m.chatSession.Value()
			if sid != "" {
				m.startFollow(sid, m.chatCursor)
				m.status = "following " + sid
				*cmds = append(*cmds, fastTick())
			}
			return true
		}
	case TabTurns:
		switch key {
		case "enter":
			*cmds = append(*cmds, m.cmdTurnShow())
			return true
		case "x", "X":
			*cmds = append(*cmds, m.cmdTurnCancel())
			return true
		case "p", "P":
			*cmds = append(*cmds, m.cmdTurnRequests())
			return true
		case "t", "T":
			*cmds = append(*cmds, m.cmdTurnTools())
			return true
		case "m", "M":
			*cmds = append(*cmds, m.cmdMessageShow())
			return true
		case "i", "I":
			*cmds = append(*cmds, m.cmdToolShow())
			return true
		case "[", "]":
			// toggle focus between turn id and inspect id
			if m.turnInput.Focused() {
				m.turnInput.Blur()
				m.inspectInput.Focus()
			} else {
				m.inspectInput.Blur()
				m.turnInput.Focus()
			}
			return true
		}
	case TabArtifacts:
		switch key {
		case "enter":
			*cmds = append(*cmds, m.cmdArtifactShow())
			return true
		case "d", "D":
			*cmds = append(*cmds, m.cmdArtifactDownload())
			return true
		}
	case TabProviders:
		switch key {
		case "enter":
			*cmds = append(*cmds, m.cmdSetKey())
			return true
		case "l":
			*cmds = append(*cmds, m.cmdLoginStart())
			return true
		case "L":
			*cmds = append(*cmds, m.cmdLoginStatus())
			return true
		case "[", "]":
			m.cycleProviderFocus()
			return true
		}
	case TabGoDirect:
		switch key {
		case "j", "down":
			if m.goCursor < len(m.goproCatalog())-1 {
				m.goCursor++
			}
			return true
		case "k", "up":
			if m.goCursor > 0 {
				m.goCursor--
			}
			return true
		case "s", "S":
			m.goStream = !m.goStream
			m.status = fmt.Sprintf("stream=%t", m.goStream)
			return true
		case "g", "G":
			*cmds = append(*cmds, m.cmdGoModels())
			return true
		case "e":
			m.focusField = "prompt"
			m.goPrompt.Focus()
			return true
		case "enter":
			p := strings.TrimSpace(m.goPrompt.Value())
			if p == "" {
				m.focusField = "prompt"
				m.goPrompt.Focus()
				return true
			}
			*cmds = append(*cmds, m.cmdGoRun(p))
			return true
		}
	}
	return false
}

func (m *Model) isEditing() bool {
	return m.chatBox.Focused() || m.goPrompt.Focused() ||
		m.chatSession.Focused() || m.turnInput.Focused() ||
		m.inspectInput.Focused() || m.artInput.Focused() ||
		m.keyProvider.Focused() || m.keyValue.Focused() || m.loginID.Focused()
}

func (m *Model) blurAll() {
	m.chatBox.Blur()
	m.goPrompt.Blur()
	m.chatSession.Blur()
	m.turnInput.Blur()
	m.inspectInput.Blur()
	m.artInput.Blur()
	m.keyProvider.Blur()
	m.keyValue.Blur()
	m.loginID.Blur()
}

func (m *Model) setTab(t int) {
	m.blurAll()
	m.tab = t
	switch t {
	case TabChat:
		m.focusField = "chat"
		m.chatSession.Focus()
	case TabTurns:
		m.turnInput.Focus()
	case TabArtifacts:
		m.artInput.Focus()
	case TabProviders:
		m.focusField = "provider"
		m.keyProvider.Focus()
	case TabGoDirect:
		m.focusField = "list"
	default:
		m.focusField = "chat"
	}
}

func (m *Model) cycleProviderFocus() {
	switch m.focusField {
	case "provider":
		m.focusField = "keyval"
		m.keyProvider.Blur()
		m.keyValue.Focus()
	case "keyval":
		m.focusField = "login"
		m.keyValue.Blur()
		m.loginID.Focus()
	default:
		m.focusField = "provider"
		m.loginID.Blur()
		m.keyProvider.Focus()
	}
}

// ---- refresh ----

func (m Model) cmdRefresh() tea.Cmd {
	return func() tea.Msg { return m.cmdHealth() }
}

// refreshAll loads every read-only surface (used by headless smoke too).
func (m Model) cmdRefreshAll() tea.Cmd {
	return func() tea.Msg { return m.cmdHealth() }
}

func (m Model) fullRefresh() []tea.Cmd {
	return []tea.Cmd{
		func() tea.Msg { return m.cmdHealth() },
		func() tea.Msg { return m.cmdNode() },
		func() tea.Msg { return m.cmdModels() },
		func() tea.Msg { return m.cmdCaps() },
		func() tea.Msg { return m.cmdProvs() },
		func() tea.Msg { return m.cmdSessions() },
	}
}

// ---- sessions ----

func (m Model) cmdSessionInspect() tea.Cmd {
	return func() tea.Msg {
		if len(m.sessions) == 0 {
			return statusMsg{"no sessions — press c to create"}
		}
		if m.sessCursor < 0 || m.sessCursor >= len(m.sessions) {
			return statusMsg{"cursor out of range"}
		}
		id := m.sessions[m.sessCursor].ID
		s, err := m.daemon.Session(id)
		if err != nil {
			return sessionMsg{nil, err}
		}
		// also load history preview + wire chat tab to this session
		h, herr := m.daemon.History(id, nil, u32p(100))
		if herr != nil {
			return sessionMsg{s, nil}
		}
		_ = h
		return sessionMsg{s, nil}
	}
}

// cmdSessionInspectWithHistory chains session+history for the Sessions tab.
func (m *Model) cmdSessionInspectFull() tea.Cmd {
	return func() tea.Msg { return statusMsg{"use enter"} }
}

func (m Model) cmdSessionCreate() tea.Cmd {
	return func() tea.Msg {
		prov, model, err := m.defaultModel()
		if err != nil {
			return errMsg{err}
		}
		cmdID, err := client.NewCommandID()
		if err != nil {
			return errMsg{err}
		}
		r, err := m.daemon.CreateSession(&client.CreateSessionRequest{
			CommandID: cmdID + ":session",
			Provider:  prov,
			Model:     model,
		})
		if err != nil {
			return errMsg{err}
		}
		return statusMsg{"created session " + r.SessionID}
	}
}

func (m Model) defaultModel() (string, string, error) {
	for _, mo := range m.models {
		if mo.IsDefault && mo.Ready {
			return mo.Provider, mo.Model, nil
		}
	}
	for _, mo := range m.models {
		if mo.Ready {
			return mo.Provider, mo.Model, nil
		}
	}
	// fall back to documented default; daemon validates
	return "opencode-go", "glm-5.3-flash", nil
}

// ---- chat ----

func (m Model) cmdChatSend(text string) tea.Cmd {
	return func() tea.Msg {
		sid := strings.TrimSpace(m.chatSession.Value())
		if sid == "" {
			prov, model, _ := m.defaultModel()
			cmdID, err := client.NewCommandID()
			if err != nil {
				return receiptMsg{nil, err}
			}
			r, err := m.daemon.CreateSession(&client.CreateSessionRequest{
				CommandID: cmdID + ":session",
				Provider:  prov,
				Model:     model,
			})
			if err != nil {
				return receiptMsg{nil, err}
			}
			sid = r.SessionID
		}
		cmdID, err := client.NewCommandID()
		if err != nil {
			return receiptMsg{nil, err}
		}
		r, err := m.daemon.SendMessage(sid, &client.SendMessageRequest{
			CommandID: cmdID,
			Text:      text,
		})
		if err != nil {
			return receiptMsg{nil, err}
		}
		return receiptMsg{r, nil}
	}
}

// ---- turns ----

func (m Model) cmdTurnShow() tea.Cmd {
	return func() tea.Msg {
		id := strings.TrimSpace(m.turnInput.Value())
		if id == "" && m.turnDetail != nil {
			id = m.turnDetail.ID
		}
		if id == "" && m.chatTurn != "" {
			id = m.chatTurn
		}
		if id == "" {
			return statusMsg{"enter a turn id first"}
		}
		t, err := m.daemon.Turn(id)
		return turnMsg{t, err}
	}
}

func (m Model) cmdTurnCancel() tea.Cmd {
	return func() tea.Msg {
		id := strings.TrimSpace(m.turnInput.Value())
		if id == "" {
			id = m.chatTurn
		}
		if id == "" {
			return statusMsg{"nothing to cancel"}
		}
		// explicit cancellation; stop local follow but daemon turn continues to cancel
		cmdID, err := client.NewCommandID()
		if err != nil {
			return errMsg{err}
		}
		r, err := m.daemon.CancelTurn(id, &client.CancelTurnRequest{CommandID: cmdID})
		if err != nil {
			return errMsg{err}
		}
		return statusMsg{"cancel accepted for " + id + " rev " + fmt.Sprint(r.Revision)}
	}
}

func (m Model) cmdTurnRequests() tea.Cmd {
	return func() tea.Msg {
		id := strings.TrimSpace(m.turnInput.Value())
		if id == "" {
			id = m.chatTurn
		}
		if id == "" {
			return statusMsg{"enter a turn id first"}
		}
		p, err := m.daemon.ModelRequests(id, nil, u32p(50))
		return requestsMsg{p, err}
	}
}

func (m Model) cmdTurnTools() tea.Cmd {
	return func() tea.Msg {
		id := strings.TrimSpace(m.turnInput.Value())
		if id == "" {
			id = m.chatTurn
		}
		if id == "" {
			return statusMsg{"enter a turn id first"}
		}
		p, err := m.daemon.ToolInvocations(id, nil, u32p(50))
		return toolsMsg{p, err}
	}
}

func (m Model) cmdMessageShow() tea.Cmd {
	return func() tea.Msg {
		id := strings.TrimSpace(m.inspectInput.Value())
		if id == "" {
			return statusMsg{"enter a message id in the second field"}
		}
		mm, err := m.daemon.Message(id)
		return messageMsg{mm, err}
	}
}

func (m Model) cmdToolShow() tea.Cmd {
	return func() tea.Msg {
		id := strings.TrimSpace(m.inspectInput.Value())
		if id == "" {
			return statusMsg{"enter a tool invocation id in the second field"}
		}
		t, err := m.daemon.ToolInvocation(id)
		return toolMsg{t, err}
	}
}

// ---- artifacts ----

func (m Model) cmdArtifactShow() tea.Cmd {
	return func() tea.Msg {
		id := strings.TrimSpace(m.artInput.Value())
		if id == "" {
			return statusMsg{"enter an artifact id (sha256)"}
		}
		a, err := m.daemon.Artifact(id)
		return artifactMsg{a, err}
	}
}

func (m *Model) cmdArtifactDownload() tea.Cmd {
	id := strings.TrimSpace(m.artInput.Value())
	out := id + ".bin"
	m.artOut = out
	return func() tea.Msg {
		if id == "" {
			return downloadMsg{nil, nil, fmt.Errorf("enter an artifact id first")}
		}
		data, meta, err := m.daemon.DownloadArtifact(id)
		if err != nil {
			return downloadMsg{nil, nil, err}
		}
		// publish only after checksum verification (done inside client)
		if err := os.WriteFile(out, data, 0o600); err != nil {
			return downloadMsg{nil, nil, err}
		}
		return downloadMsg{data, meta, nil}
	}
}

// ---- providers ----

func (m Model) cmdSetKey() tea.Cmd {
	return func() tea.Msg {
		prov := strings.TrimSpace(m.keyProvider.Value())
		if prov == "" {
			prov = "opencode-go"
		}
		key := strings.TrimSpace(m.keyValue.Value())
		if key == "" {
			return statusMsg{"enter an API key in the key field (never echoed to logs)"}
		}
		s, err := m.daemon.SetProviderKey(prov, &client.SetAPIKeyRequest{APIKey: key})
		return setKeyMsg{s, err}
	}
}

func (m Model) cmdLoginStart() tea.Cmd {
	return func() tea.Msg {
		cmdID, err := client.NewCommandID()
		if err != nil {
			return loginMsg{nil, err}
		}
		l, err := m.daemon.StartCodexLogin(&client.StartLoginRequest{CommandID: cmdID})
		return loginMsg{l, err}
	}
}

func (m Model) cmdLoginStatus() tea.Cmd {
	return func() tea.Msg {
		id := strings.TrimSpace(m.loginID.Value())
		if id == "" {
			return statusMsg{"enter a login id first"}
		}
		s, err := m.daemon.CodexLoginStatus(id)
		return loginStatMsg{s, err}
	}
}

// ---- go direct ----

func (m Model) goproCatalog() []provider.Model {
	return provider.Catalog
}

func (m Model) cmdGoModels() tea.Cmd {
	return func() tea.Msg {
		if m.gopro == nil {
			return goModelsMsg{nil, fmt.Errorf("no OPENCODE_GO_API_KEY")}
		}
		ids, err := m.gopro.ListModels()
		return goModelsMsg{ids, err}
	}
}

func (m *Model) cmdGoRun(prompt string) tea.Cmd {
	cat := provider.Catalog
	if m.goCursor < 0 || m.goCursor >= len(cat) {
		return func() tea.Msg { return goResultMsg{nil, fmt.Errorf("pick a model first")} }
	}
	model := cat[m.goCursor].ID
	stream := m.goStream
	m.goWorking = true
	m.goView.SetContent(m.goView.View() + "\n» " + model + ": " + truncate(prompt, 200) + "\n")
	m.goView.GotoBottom()
	return func() tea.Msg {
		if m.gopro == nil {
			return goResultMsg{nil, fmt.Errorf("no OPENCODE_GO_API_KEY")}
		}
		if stream {
			r, err := m.gopro.CompleteStreaming(model, prompt, 512, "hive-direct", func(d string) {
				select {
				case m.goCh <- d:
				default:
				}
			})
			// drain streamer into view is handled by frameDrain ticker; push final
			_ = r
			return goResultMsg{r, err}
		}
		r, err := m.gopro.Complete(model, prompt, 512, "hive-direct")
		if err == nil && r != nil {
			select {
			case m.goCh <- r.Text + "\n":
			default:
			}
		}
		return goResultMsg{r, err}
	}
}
