package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"hive/internal/client"
	"hive/internal/provider"
)

// handleKey processes global + per-tab keys. Returns true when the key was
// consumed and input routing should stop.
func (m *Model) handleKey(msg tea.KeyMsg, cmds *[]tea.Cmd) bool {
	key := msg.String()

	// Closing the observer never sends a daemon cancellation command.
	if key == "ctrl+c" || key == "ctrl+q" {
		m.stopFollow()
		*cmds = append(*cmds, tea.Quit)
		return true
	}
	if key == "tab" || key == "shift+tab" {
		step := 1
		if key == "shift+tab" {
			step = TabCount - 1
		}
		*cmds = append(*cmds, m.setTab((m.tab+step)%TabCount))
		return true
	}
	if key == "f1" || key == "f2" || key == "f3" {
		m.focusInput(int(key[1]-'1'), cmds)
		return true
	}
	if (key == "[" || key == "]") && !m.chatBox.Focused() && !m.goPrompt.Focused() {
		switch m.tab {
		case TabProviders:
			m.cycleProviderFocus(cmds)
			return true
		case TabTurns:
			field := 1
			if m.inspectInput.Focused() {
				field = 0
			}
			m.focusInput(field, cmds)
			return true
		case TabChat:
			m.focusInput(1, cmds)
			return true
		}
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
					*cmds = append(*cmds, m.cmdChatSend(text))
				}
				return true
			}
			if m.tab == TabChat {
				m.focusInput(1, cmds)
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
				if m.focusField == "login" {
					*cmds = append(*cmds, m.cmdLoginStatus())
				} else {
					*cmds = append(*cmds, m.cmdSetKey())
				}
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
	case "q":
		m.stopFollow()
		*cmds = append(*cmds, tea.Quit)
		return true
	case "?", "h":
		m.showHelp = !m.showHelp
		return true
	case "1", "2", "3", "4", "5", "6", "7":
		*cmds = append(*cmds, m.setTab(int(key[0]-'1')))
		return true
	case "r", "R":
		switch m.tab {
		case TabChat:
			*cmds = append(*cmds, m.cmdHistory())
		case TabTurns:
			*cmds = append(*cmds, m.cmdTurnShow())
		case TabArtifacts:
			*cmds = append(*cmds, m.cmdArtifactShow())
		case TabProviders:
			*cmds = append(*cmds, func() tea.Msg { return m.cmdProvs() })
		case TabGoDirect:
			*cmds = append(*cmds, m.cmdGoModels())
		default:
			*cmds = append(*cmds, m.cmdRefresh())
		}
		return true
	case "pgup", "pgdown", "home", "end":
		var cmd tea.Cmd
		switch m.tab {
		case TabChat:
			if key == "home" {
				m.chatView.GotoTop()
			} else if key == "end" {
				m.chatView.GotoBottom()
			} else {
				m.chatView, cmd = m.chatView.Update(msg)
			}
		case TabGoDirect:
			if key == "home" {
				m.goView.GotoTop()
			} else if key == "end" {
				m.goView.GotoBottom()
			} else {
				m.goView, cmd = m.goView.Update(msg)
			}
		default:
			m.detailView.SetContent(m.viewTab())
			if key == "home" {
				m.detailView.GotoTop()
			} else if key == "end" {
				m.detailView.GotoBottom()
			} else {
				m.detailView, cmd = m.detailView.Update(msg)
			}
		}
		*cmds = append(*cmds, cmd)
		return true
	case "e":
		if m.tab == TabTurns || m.tab == TabArtifacts || m.tab == TabProviders {
			m.focusInput(0, cmds)
			return true
		}
	}

	switch m.tab {
	case TabSessions:
		switch key {
		case "j", "down":
			if m.sessCursor < len(m.sessions)-1 {
				m.sessCursor++
			}
			m.revealSessionCursor()
			return true
		case "k", "up":
			if m.sessCursor > 0 {
				m.sessCursor--
			}
			m.revealSessionCursor()
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
			m.focusInput(1, cmds)
			return true
		case "enter":
			m.focusInput(1, cmds)
			return true
		case "f":
			sid := m.chatSession.Value()
			if sid != "" {
				m.startFollow(sid, m.chatCursor)
				m.status = "following " + sid
				*cmds = append(*cmds, m.cmdHistory())
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
			m.focusInput(0, cmds)
			return true
		case "enter":
			p := strings.TrimSpace(m.goPrompt.Value())
			if p == "" {
				m.focusInput(0, cmds)
				return true
			}
			*cmds = append(*cmds, m.cmdGoRun(p))
			return true
		}
	}
	return false
}

func (m *Model) revealSessionCursor() {
	m.detailView.SetContent(m.viewSessions())
	row := m.sessCursor + 1
	if row < m.detailView.YOffset {
		m.detailView.SetYOffset(row)
	} else if row >= m.detailView.YOffset+m.detailView.Height {
		m.detailView.SetYOffset(row - m.detailView.Height + 1)
	}
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

func (m *Model) setTab(t int) tea.Cmd {
	m.blurAll()
	m.tab = t
	m.detailView.GotoTop()
	switch t {
	case TabChat:
		m.focusField = "chat"
		return m.chatSession.Focus()
	case TabTurns:
		return m.turnInput.Focus()
	case TabArtifacts:
		return m.artInput.Focus()
	case TabProviders:
		m.focusField = "provider"
		return m.keyProvider.Focus()
	case TabGoDirect:
		m.focusField = "list"
	default:
		m.focusField = "chat"
	}
	return nil
}

func (m *Model) cycleProviderFocus(cmds *[]tea.Cmd) {
	switch m.focusField {
	case "provider":
		m.focusInput(1, cmds)
	case "keyval":
		m.focusInput(2, cmds)
	default:
		m.focusInput(0, cmds)
	}
}

func (m *Model) focusInput(field int, cmds *[]tea.Cmd) {
	m.blurAll()
	var cmd tea.Cmd
	switch m.tab {
	case TabChat:
		m.focusField = "chat"
		if field == 1 {
			m.focusField = "input"
			cmd = m.chatBox.Focus()
		} else {
			cmd = m.chatSession.Focus()
		}
	case TabTurns:
		if field == 1 {
			cmd = m.inspectInput.Focus()
		} else {
			cmd = m.turnInput.Focus()
		}
	case TabArtifacts:
		cmd = m.artInput.Focus()
	case TabProviders:
		switch field {
		case 1:
			m.focusField, cmd = "keyval", m.keyValue.Focus()
		case 2:
			m.focusField, cmd = "login", m.loginID.Focus()
		default:
			m.focusField, cmd = "provider", m.keyProvider.Focus()
		}
	case TabGoDirect:
		m.focusField, cmd = "prompt", m.goPrompt.Focus()
	}
	*cmds = append(*cmds, cmd)
}

// ---- refresh ----

func (m Model) cmdRefresh() tea.Cmd {
	return func() tea.Msg { return m.cmdHealth() }
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
		return sessionMsg{s, nil}
	}
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
		return sessionCreatedMsg{r, err}
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

func (m *Model) cmdChatSend(text string) tea.Cmd {
	if m.chatSending || m.chatActive {
		return func() tea.Msg {
			return statusMsg{"a turn is already pending; wait for completion or cancel it in Turns"}
		}
	}
	m.chatSending = true
	daemon, sid := m.daemon, strings.TrimSpace(m.chatSession.Value())
	prov, model, _ := m.defaultModel()
	return func() tea.Msg {
		if sid == "" {
			cmdID, err := client.NewCommandID()
			if err != nil {
				return receiptMsg{err: err, text: text}
			}
			r, err := daemon.CreateSession(&client.CreateSessionRequest{
				CommandID: cmdID + ":session",
				Provider:  prov,
				Model:     model,
			})
			if err != nil {
				return receiptMsg{err: err, text: text}
			}
			sid = r.SessionID
		}
		cmdID, err := client.NewCommandID()
		if err != nil {
			return receiptMsg{err: err, text: text, session: sid}
		}
		r, err := daemon.SendMessage(sid, &client.SendMessageRequest{
			CommandID: cmdID,
			Text:      text,
		})
		return receiptMsg{r: r, err: err, text: text, session: sid}
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
		// Only this action explicitly asks the daemon to cancel execution.
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
		if err := writeArtifact(out, data); err != nil {
			return downloadMsg{nil, nil, err}
		}
		return downloadMsg{data, meta, nil}
	}
}

// Publish verified bytes with an atomic rename so an existing output symlink
// cannot redirect the write and a failed write cannot expose a partial file.
func writeArtifact(out string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(out), ".hive-artifact-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), out)
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
	if m.goWorking {
		return func() tea.Msg { return statusMsg{"a direct provider request is already running"} }
	}
	cat := provider.Catalog
	if m.goCursor < 0 || m.goCursor >= len(cat) {
		return func() tea.Msg { return goResultMsg{nil, fmt.Errorf("pick a model first")} }
	}
	model := cat[m.goCursor].ID
	stream := m.goStream
	m.goWorking = true
	m.appendGo("\n» " + model + ": " + truncate(prompt, 200) + "\n")
	m.goRunPrefix = m.goText
	gp, ch := m.gopro, m.goCh
	return func() tea.Msg {
		if gp == nil {
			return goResultMsg{nil, fmt.Errorf("no OPENCODE_GO_API_KEY")}
		}
		if stream {
			r, err := gp.CompleteStreaming(model, prompt, 512, "hive-direct", func(d string) {
				select {
				case ch <- d:
				default:
				}
			})
			return goResultMsg{r, err}
		}
		r, err := gp.Complete(model, prompt, 512, "hive-direct")
		return goResultMsg{r, err}
	}
}
