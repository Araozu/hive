// Package ui is the hive Bubble Tea TUI. One tab per capability area,
// all backed by the daemon public API plus a direct OpenCode Go probe.
// Closing the TUI never cancels daemon work; cancellation is explicit.
package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"hive/internal/client"
	"hive/internal/config"
	"hive/internal/provider"
)

// Tabs. Keys 1..7 switch.
const (
	TabStatus = iota
	TabSessions
	TabChat
	TabTurns
	TabArtifacts
	TabProviders
	TabGoDirect
	TabCount
)

var tabNames = []string{"Status", "Sessions", "Chat", "Turns", "Artifacts", "Providers", "GoDirect"}

// Async result messages.
type (
	healthMsg struct {
		h   *client.HealthResponse
		err error
	}
	nodeMsg struct {
		n   *client.NodeResponse
		err error
	}
	modelsMsg struct {
		m   []client.ModelResponse
		err error
	}
	capsMsg struct {
		c   *client.CapabilitiesResponse
		err error
	}
	provsMsg struct {
		p   []client.ProviderStatus
		err error
	}
	sessionsMsg struct {
		p   *client.Page[client.SessionResponse]
		err error
	}
	sessionMsg struct {
		s   *client.SessionResponse
		err error
	}
	historyMsg struct {
		p   *client.Page[client.MessageResponse]
		err error
	}
	receiptMsg struct {
		r   *client.CommandReceipt
		err error
	}
	turnMsg struct {
		t   *client.TurnResponse
		err error
	}
	requestsMsg struct {
		p   *client.Page[client.ModelRequestResponse]
		err error
	}
	toolsMsg struct {
		p   *client.Page[client.ToolInvocationResponse]
		err error
	}
	toolMsg struct {
		t   *client.ToolInvocationResponse
		err error
	}
	messageMsg struct {
		m   *client.MessageResponse
		err error
	}
	artifactMsg struct {
		a   *client.ArtifactResponse
		err error
	}
	downloadMsg struct {
		data []byte
		meta *client.ArtifactResponse
		err  error
	}
	loginMsg struct {
		l   *client.LoginResponse
		err error
	}
	loginStatMsg struct {
		s   *client.LoginStatus
		err error
	}
	setKeyMsg struct {
		s   *client.ProviderStatus
		err error
	}
	goModelsMsg struct {
		ids []string
		err error
	}
	goResultMsg struct {
		r   *provider.Result
		err error
	}
	tickMsg       struct{}
	frameDrainMsg struct{}
	statusMsg     struct{ s string }
	errMsg        struct{ err error }
)

// Model is the root TUI state.
type Model struct {
	cfg      config.Config
	daemon   *client.DaemonClient
	gopro    *provider.Client
	tab      int
	w, h     int
	status   string
	showHelp bool

	health *client.HealthResponse
	node   *client.NodeResponse
	models []client.ModelResponse
	caps   *client.CapabilitiesResponse
	provs  []client.ProviderStatus

	sessions   []client.SessionResponse
	sessCursor int
	sessDetail *client.SessionResponse
	history    []client.MessageResponse

	// chat
	chatSession textinput.Model
	chatBox     textarea.Model
	chatView    viewport.Model
	chatTurn    string
	chatCursor  uint64
	chatActive  bool
	followCh    chan *client.EventFrame
	followCtx   context.CancelFunc
	streamBuf   string

	// turns
	turnInput  textinput.Model
	turnDetail *client.TurnResponse
	turnReqs   []client.ModelRequestResponse
	turnTools  []client.ToolInvocationResponse
	singleTool *client.ToolInvocationResponse
	singleMsg  *client.MessageResponse

	// artifacts
	artInput textinput.Model
	artMeta  *client.ArtifactResponse
	artBytes int
	artOut   string

	// providers
	keyProvider textinput.Model
	keyValue    textinput.Model
	loginID     textinput.Model

	// go direct
	goCursor  int
	goPrompt  textarea.Model
	goStream  bool
	goView    viewport.Model
	goResult  *provider.Result
	goIDs     []string
	goWorking bool
	goCh      chan string

	// generic single-line inspector
	inspectInput textinput.Model
	focusField   string // which input has focus on current tab
}

func New(cfg config.Config) Model {
	daemon, _ := client.New(cfg.DaemonURL)
	if cfg.TokenAvails {
		if d, err := client.NewWithTokenFile(cfg.DaemonURL, cfg.TokenFile); err == nil {
			daemon = d
		}
	}
	var gp *provider.Client
	if cfg.GoAPIKey != "" {
		if c, err := provider.New(cfg.GoAPIKey); err == nil {
			gp = c
		}
	}
	ti := func(ph string) textinput.Model {
		t := textinput.New()
		t.Placeholder = ph
		t.CharLimit = 256
		t.Width = 48
		return t
	}
	ta := func(ph string) textarea.Model {
		t := textarea.New()
		t.Placeholder = ph
		t.SetWidth(80)
		t.SetHeight(4)
		t.CharLimit = 8000
		return t
	}
	m := Model{
		cfg: cfg, daemon: daemon, gopro: gp,
		chatSession:  ti("session id (empty = create new)"),
		turnInput:    ti("turn id"),
		artInput:     ti("artifact id (sha256)"),
		keyProvider:  ti("provider: opencode-go"),
		keyValue:     ti("api key (input hidden on screen log)"),
		loginID:      ti("login id"),
		inspectInput: ti("message id / tool id"),
		followCh:     make(chan *client.EventFrame, 256),
		goCh:         make(chan string, 256),
	}
	m.chatBox = ta("Type a message, Enter to send (Esc blurs first)")
	m.goPrompt = ta("Direct provider prompt — Enter runs")
	m.chatView = viewport.New(80, 12)
	m.goView = viewport.New(80, 12)
	m.keyProvider.SetValue("opencode-go")
	m.keyValue.EchoMode = textinput.EchoPassword
	m.keyValue.EchoCharacter = '•'
	m.focusField = "chat"
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(fetchAll(m), tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(4000000000, func(_ time.Time) tea.Msg { return tickMsg{} })
	// placeholder replaced below by fast drain ticker
}

func fastTick() tea.Cmd {
	return tea.Tick(150000000, func(_ time.Time) tea.Msg { return frameDrainMsg{} })
}

func fetchAll(m Model) tea.Cmd {
	return func() tea.Msg {
		h, err := m.daemon.Health()
		if err != nil {
			return healthMsg{nil, err}
		}
		return healthMsg{h, nil}
	}
}

// Commands
func (m Model) cmdHealth() tea.Msg {
	h, err := m.daemon.Health()
	return healthMsg{h, err}
}
func (m Model) cmdNode() tea.Msg {
	n, err := m.daemon.Node()
	return nodeMsg{n, err}
}
func (m Model) cmdModels() tea.Msg {
	mo, err := m.daemon.Models()
	return modelsMsg{mo, err}
}
func (m Model) cmdCaps() tea.Msg {
	c, err := m.daemon.Capabilities()
	return capsMsg{c, err}
}
func (m Model) cmdProvs() tea.Msg {
	p, err := m.daemon.Providers()
	return provsMsg{p, err}
}
func (m Model) cmdSessions() tea.Msg {
	p, err := m.daemon.ListSessions(nil, u32p(100))
	return sessionsMsg{p, err}
}
func u32p(v uint32) *uint32 { return &v }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.chatView.Width = max(40, m.w-6)
		m.chatView.Height = max(6, m.h-22)
		m.goView.Width = max(40, m.w-6)
		m.goView.Height = max(6, m.h-22)
		m.chatBox.SetWidth(max(40, m.w-6))
		m.goPrompt.SetWidth(max(40, m.w-6))
		return m, nil
	case tea.KeyMsg:
		if m.handleKey(msg, &cmds) {
			return m, tea.Batch(cmds...)
		}
	case healthMsg:
		if msg.err != nil {
			m.status = "health: " + msg.err.Error()
		} else {
			m.health = msg.h
			m.status = "health ok: " + msg.h.Version
			cmds = append(cmds,
				func() tea.Msg { return m.cmdNode() },
				func() tea.Msg { return m.cmdModels() },
				func() tea.Msg { return m.cmdCaps() },
				func() tea.Msg { return m.cmdProvs() },
				func() tea.Msg { return m.cmdSessions() },
			)
		}
		return m, tea.Batch(cmds...)
	case nodeMsg:
		if msg.err != nil {
			m.status = "node: " + msg.err.Error()
		} else {
			m.node = msg.n
		}
		return m, nil
	case modelsMsg:
		if msg.err != nil {
			m.status = "models: " + msg.err.Error()
		} else {
			m.models = msg.m
			m.status = fmt.Sprintf("models: %d listed", len(msg.m))
		}
		return m, nil
	case capsMsg:
		if msg.err != nil {
			m.status = "capabilities: " + msg.err.Error()
		} else {
			m.caps = msg.c
		}
		return m, nil
	case provsMsg:
		if msg.err != nil {
			m.status = "providers: " + msg.err.Error()
		} else {
			m.provs = msg.p
		}
		return m, nil
	case sessionsMsg:
		if msg.err != nil {
			m.status = "sessions: " + msg.err.Error()
		} else if msg.p != nil {
			m.sessions = msg.p.Items
			m.status = fmt.Sprintf("sessions: %d listed", len(m.sessions))
		}
		return m, nil
	case sessionMsg:
		if msg.err != nil {
			m.status = "session: " + msg.err.Error()
		} else {
			m.sessDetail = msg.s
			m.status = "session " + msg.s.ID
			m.chatSession.SetValue(msg.s.ID)
			m.chatCursor = msg.s.LastEventSeq
			sid := msg.s.ID
			cmds = append(cmds, func() tea.Msg {
				p, err := m.daemon.History(sid, nil, u32p(100))
				return historyMsg{p, err}
			})
		}
		return m, tea.Batch(cmds...)
	case historyMsg:
		if msg.err != nil {
			m.status = "history: " + msg.err.Error()
		} else if msg.p != nil {
			m.history = msg.p.Items
			var sb strings.Builder
			for _, mm := range m.history {
				fmt.Fprintf(&sb, "%s: %s\n", mm.Role, mm.Text)
			}
			m.chatView.SetContent(sb.String())
			m.chatView.GotoBottom()
		}
		return m, nil
	case receiptMsg:
		if msg.err != nil {
			m.status = "send: " + msg.err.Error()
			m.chatActive = false
		} else if msg.r != nil {
			m.status = fmt.Sprintf("accepted cmd %s rev %d evt %d", shortID(msg.r.CommandID), msg.r.Revision, msg.r.EventSequence)
			if msg.r.TurnID != nil {
				m.chatTurn = *msg.r.TurnID
				m.chatCursor = msg.r.EventSequence
				m.chatActive = true
				m.streamBuf = ""
				m.startFollow(m.chatSession.Value(), m.chatCursor)
				cmds = append(cmds, fastTick())
			}
		}
		return m, tea.Batch(cmds...)
	case turnMsg:
		if msg.err != nil {
			m.status = "turn: " + msg.err.Error()
		} else {
			m.turnDetail = msg.t
			m.status = "turn " + msg.t.ID + " " + msg.t.Status
		}
		return m, nil
	case requestsMsg:
		if msg.err != nil {
			m.status = "requests: " + msg.err.Error()
		} else if msg.p != nil {
			m.turnReqs = msg.p.Items
		}
		return m, nil
	case toolsMsg:
		if msg.err != nil {
			m.status = "tools: " + msg.err.Error()
		} else if msg.p != nil {
			m.turnTools = msg.p.Items
		}
		return m, nil
	case toolMsg:
		if msg.err != nil {
			m.status = "tool: " + msg.err.Error()
		} else {
			m.singleTool = msg.t
		}
		return m, nil
	case messageMsg:
		if msg.err != nil {
			m.status = "message: " + msg.err.Error()
		} else {
			m.singleMsg = msg.m
		}
		return m, nil
	case artifactMsg:
		if msg.err != nil {
			m.status = "artifact: " + msg.err.Error()
		} else {
			m.artMeta = msg.a
			m.status = "artifact " + msg.a.ID
		}
		return m, nil
	case downloadMsg:
		if msg.err != nil {
			m.status = "download: " + msg.err.Error()
		} else {
			m.artBytes = len(msg.data)
			m.status = fmt.Sprintf("artifact verified %d bytes", len(msg.data))
			if m.artOut != "" {
				// best effort write is done in the command already
			}
			_ = msg.meta
		}
		return m, nil
	case loginMsg:
		if msg.err != nil {
			m.status = "login: " + msg.err.Error()
		} else {
			m.status = "login " + msg.l.LoginID + " -> " + msg.l.AuthorizationURL
			m.loginID.SetValue(msg.l.LoginID)
		}
		return m, nil
	case loginStatMsg:
		if msg.err != nil {
			m.status = "login-status: " + msg.err.Error()
		} else {
			m.status = "login " + msg.s.LoginID + ": " + msg.s.Status
		}
		return m, nil
	case setKeyMsg:
		if msg.err != nil {
			m.status = "set-key: " + msg.err.Error()
		} else {
			m.status = "key saved for " + msg.s.Provider
			m.keyValue.SetValue("")
			cmds = append(cmds, func() tea.Msg { return m.cmdProvs() })
		}
		return m, tea.Batch(cmds...)
	case goModelsMsg:
		if msg.err != nil {
			m.status = "go models: " + msg.err.Error()
		} else {
			m.goIDs = msg.ids
			m.status = fmt.Sprintf("go live models: %d", len(msg.ids))
		}
		m.goWorking = false
		return m, nil
	case goResultMsg:
		m.goWorking = false
		if msg.err != nil {
			m.status = "go turn: " + msg.err.Error()
			m.goView.SetContent(m.goView.View() + "\n[error] " + msg.err.Error())
		} else if msg.r != nil {
			m.goResult = msg.r
			usage := "?"
			if msg.r.Usage.Total != nil {
				usage = fmt.Sprint(*msg.r.Usage.Total)
			}
			m.status = fmt.Sprintf("go %s %s tokens=%s", msg.r.Model, msg.r.Outcome, usage)
		}
		m.goView.GotoBottom()
		return m, nil
	case frameDrainMsg:
		m.drainFollow()
		m.drainGo()
		if m.chatActive {
			cmds = append(cmds, fastTick())
		}
		return m, tea.Batch(cmds...)
	case tickMsg:
		cmds = append(cmds, tickCmd())
		return m, tea.Batch(cmds...)
	case statusMsg:
		m.status = msg.s
		return m, nil
	case errMsg:
		m.status = "error: " + msg.err.Error()
		return m, nil
	}

	// focused input routing
	switch m.tab {
	case TabChat:
		if m.focusField == "input" {
			var c tea.Cmd
			m.chatBox, c = m.chatBox.Update(msg)
			cmds = append(cmds, c)
		} else {
			var c tea.Cmd
			m.chatSession, c = m.chatSession.Update(msg)
			cmds = append(cmds, c)
			var c2 tea.Cmd
			m.chatView, c2 = m.chatView.Update(msg)
			cmds = append(cmds, c2)
		}
	case TabTurns:
		var c tea.Cmd
		m.turnInput, c = m.turnInput.Update(msg)
		cmds = append(cmds, c)
		var c2 tea.Cmd
		m.inspectInput, c2 = m.inspectInput.Update(msg)
		cmds = append(cmds, c2)
	case TabArtifacts:
		var c tea.Cmd
		m.artInput, c = m.artInput.Update(msg)
		cmds = append(cmds, c)
	case TabProviders:
		switch m.focusField {
		case "keyval":
			var c tea.Cmd
			m.keyValue, c = m.keyValue.Update(msg)
			cmds = append(cmds, c)
		case "login":
			var c tea.Cmd
			m.loginID, c = m.loginID.Update(msg)
			cmds = append(cmds, c)
		default:
			var c tea.Cmd
			m.keyProvider, c = m.keyProvider.Update(msg)
			cmds = append(cmds, c)
		}
	case TabGoDirect:
		if m.focusField == "prompt" {
			var c tea.Cmd
			m.goPrompt, c = m.goPrompt.Update(msg)
			cmds = append(cmds, c)
		} else {
			var c tea.Cmd
			m.goView, c = m.goView.Update(msg)
			cmds = append(cmds, c)
		}
	}
	return m, tea.Batch(cmds...)
}

// refreshProtected loads everything needing auth after health succeeds.
func (m Model) refreshProtected() tea.Msg {
	if _, err := m.daemon.Health(); err != nil {
		return errMsg{err}
	}
	// chain: return node; other loads are triggered by keypress 'r' and init
	n, err := m.daemon.Node()
	if err != nil {
		return nodeMsg{nil, err}
	}
	return nodeMsg{n, nil}
}

func shortID(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// drainFollow moves buffered event frames into the chat viewport.
func (m *Model) drainFollow() {
	for {
		select {
		case f := <-m.followCh:
			if f == nil {
				m.chatActive = false
				m.chatView.SetContent(m.chatView.View() + "\n[stream closed]\n")
				m.chatView.GotoBottom()
				return
			}
			m.applyFrame(f)
		default:
			return
		}
	}
}

func (m *Model) applyFrame(f *client.EventFrame) {
	switch f.Type {
	case "delta":
		if f.Kind == "" || f.Kind == "text" {
			m.streamBuf += f.Text
			m.chatView.SetContent(m.chatView.View() + f.Text)
			m.chatView.GotoBottom()
		} else {
			m.chatView.SetContent(m.chatView.View() + fmt.Sprintf("\n[%s] %s", f.Kind, f.Text))
			m.chatView.GotoBottom()
		}
	case "durable":
		if f.Event != nil {
			if f.Event.Sequence > m.chatCursor {
				m.chatCursor = f.Event.Sequence
			}
			switch f.Event.Kind {
			case "turn_completed", "turn_failed", "turn_cancelled", "turn_interrupted", "turn_incomplete":
				m.chatActive = false
				m.chatView.SetContent(m.chatView.View() + fmt.Sprintf("\n[%s]\n", f.Event.Kind))
				m.chatView.GotoBottom()
				// reconcile canonical text
				go func(turnID, sess string) {}("", "")
			case "assistant_message_completed", "assistant_message_checkpointed":
				// canonical snapshot arrives via history poll below
			}
		}
	case "heartbeat":
		// no-op
	}
}

func (m *Model) drainGo() {
	for {
		select {
		case d := <-m.goCh:
			m.goView.SetContent(m.goView.View() + d)
			m.goView.GotoBottom()
		default:
			return
		}
	}
}

// startFollow opens a follow=true stream in the background feeding followCh.
func (m *Model) startFollow(session string, after uint64) {
	if m.followCtx != nil {
		m.followCtx()
	}
	// drain stale
	for {
		select {
		case <-m.followCh:
		default:
			goto drained
		}
	}
drained:
	ctx, cancel := context.WithCancel(context.Background())
	m.followCtx = cancel
	if session == "" {
		return
	}
	go func() {
		defer func() {
			select {
			case m.followCh <- nil:
			case <-ctx.Done():
			}
		}()
		stream, err := m.daemon.Events(session, after, true)
		if err != nil {
			return
		}
		defer stream.Close()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			f, err := stream.Next()
			if err != nil {
				return
			}
			if f == nil {
				return
			}
			select {
			case m.followCh <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Styles
var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	activeStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10")).Underline(true)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
)

// View renders the whole TUI.
func (m Model) View() string {
	var sb strings.Builder
	// header tabs
	tabs := make([]string, 0, TabCount)
	for i, n := range tabNames {
		if i == m.tab {
			tabs = append(tabs, activeStyle.Render(fmt.Sprintf("[%d]%s", i+1, n)))
		} else {
			tabs = append(tabs, dimStyle.Render(fmt.Sprintf("[%d]%s", i+1, n)))
		}
	}
	sb.WriteString(titleStyle.Render("hive") + "  " + strings.Join(tabs, "  ") + "\n")
	sb.WriteString(dimStyle.Render(fmt.Sprintf("daemon %s  token %s  go-key %s", m.cfg.DaemonURL, tokenState(m.cfg), goState(m.cfg, m.gopro))) + "\n")
	sb.WriteString(strings.Repeat("─", max(40, m.w-2)) + "\n")
	sb.WriteString(m.viewTab())
	sb.WriteString("\n" + strings.Repeat("─", max(40, m.w-2)) + "\n")
	if m.status != "" {
		sb.WriteString(dimStyle.Render("status: ") + m.status + "\n")
	}
	if m.showHelp {
		sb.WriteString(dimStyle.Render("keys: 1-7 tabs · tab cycle · r refresh · enter run/send · c new session · esc blur · s stream toggle (GoDirect) · q quit · ? help") + "\n")
	} else {
		sb.WriteString(dimStyle.Render("? help · q quit") + "\n")
	}
	return sb.String()
}

func tokenState(c config.Config) string {
	if c.TokenAvails {
		return "ok"
	}
	return "missing"
}
func goState(c config.Config, gp *provider.Client) string {
	if gp != nil {
		return "ok (" + c.GoKeySource + ")"
	}
	if c.GoAPIKey != "" {
		return "present"
	}
	return "unset"
}

func (m Model) viewTab() string {
	switch m.tab {
	case TabStatus:
		return m.viewStatus()
	case TabSessions:
		return m.viewSessions()
	case TabChat:
		return m.viewChat()
	case TabTurns:
		return m.viewTurns()
	case TabArtifacts:
		return m.viewArtifacts()
	case TabProviders:
		return m.viewProviders()
	case TabGoDirect:
		return m.viewGo()
	}
	return ""
}

func (m Model) viewStatus() string {
	var sb strings.Builder
	if m.health != nil {
		sb.WriteString(okStyle.Render("● connected") + fmt.Sprintf(" %s %s api=%d\n", m.health.Service, m.health.Version, m.health.APIVersion))
		sb.WriteString("capabilities: " + strings.Join(m.health.Capabilities, ", ") + "\n")
	} else {
		sb.WriteString(errStyle.Render("● no health yet (press r)") + "\n")
	}
	if m.node != nil {
		sb.WriteString(fmt.Sprintf("node: %s (%s) os=%s\n", m.node.Name, m.node.NodeID, m.node.OS))
	}
	if len(m.models) > 0 {
		ready := 0
		for _, mo := range m.models {
			if mo.Ready {
				ready++
			}
		}
		sb.WriteString(fmt.Sprintf("models: %d (%d ready)\n", len(m.models), ready))
		for _, mo := range m.models {
			mark := " "
			if mo.IsDefault {
				mark = "*"
			}
			state := "unavailable"
			if mo.Ready {
				state = "ready"
			}
			sb.WriteString(fmt.Sprintf(" %s %-34s %-9s %s\n", mark, mo.ID, state, mo.DisplayName))
		}
	}
	if m.caps != nil {
		sb.WriteString(fmt.Sprintf("structured: msgs=%t per-turn-model=%t per-turn-settings=%t tools=%d\n",
			m.caps.StructuredMessages, m.caps.PerTurnModelSelection, m.caps.PerTurnSettings, len(m.caps.Tools)))
		for _, t := range m.caps.Tools {
			sb.WriteString(fmt.Sprintf(" - %-12s [%s] %s\n", t.Name, t.SideEffects, t.Description))
		}
	}
	if len(m.provs) > 0 {
		sb.WriteString("providers:\n")
		for _, p := range m.provs {
			sb.WriteString(fmt.Sprintf(" - %-12s key=%t chatgpt=%t exec=%t\n", p.Provider, p.APIKeyConfigured, p.ChatGPTConfigured, p.ExecutionSupported))
		}
	}
	sb.WriteString(dimStyle.Render("r refreshes health/node/models/capabilities/providers") + "\n")
	return sb.String()
}

func (m Model) viewSessions() string {
	var sb strings.Builder
	sb.WriteString(dimStyle.Render(fmt.Sprintf("%d sessions (j/k or ↑/↓, enter inspect, c new, r refresh)", len(m.sessions))) + "\n")
	for i, s := range m.sessions {
		cursor := "  "
		if i == m.sessCursor {
			cursor = "▸ "
		}
		title := ""
		if s.Title != nil {
			title = *s.Title
		}
		sb.WriteString(fmt.Sprintf("%s%s %s rev=%d evt=%d %s\n", cursor, s.ID, s.Model, s.Revision, s.LastEventSeq, title))
	}
	if m.sessDetail != nil {
		sb.WriteString(fmt.Sprintf("\nselected: %s provider=%s model=%s rev=%d evt=%d\n",
			m.sessDetail.ID, m.sessDetail.Provider, m.sessDetail.Model, m.sessDetail.Revision, m.sessDetail.LastEventSeq))
	}
	if len(m.history) > 0 {
		sb.WriteString(dimStyle.Render("history preview:") + "\n")
		for _, mm := range m.history {
			sb.WriteString(fmt.Sprintf(" %s: %s\n", mm.Role, truncate(mm.Text, 160)))
		}
	}
	return sb.String()
}

func (m Model) viewChat() string {
	var sb strings.Builder
	sb.WriteString("session: " + m.chatSession.View() + "\n")
	if m.chatTurn != "" {
		state := "running"
		if !m.chatActive {
			state = "done"
		}
		sb.WriteString(dimStyle.Render(fmt.Sprintf("turn %s [%s] cursor %d", m.chatTurn, state, m.chatCursor)) + "\n")
	}
	sb.WriteString(m.chatView.View() + "\n")
	if m.focusField == "input" {
		sb.WriteString(m.chatBox.View() + "\n")
		sb.WriteString(dimStyle.Render("enter send · esc blur") + "\n")
	} else {
		sb.WriteString(dimStyle.Render("e edit message · enter send (when session set or new) · f follow stream") + "\n")
	}
	return sb.String()
}

func (m Model) viewTurns() string {
	var sb strings.Builder
	sb.WriteString("turn: " + m.turnInput.View() + "   msg/tool: " + m.inspectInput.View() + "\n")
	if m.turnDetail != nil {
		t := m.turnDetail
		resolved := ""
		if t.ResolvedModel != nil {
			resolved = *t.ResolvedModel
		}
		sb.WriteString(fmt.Sprintf("id=%s status=%s req=%s resolved=%s\n", t.ID, t.Status, t.RequestedModel, resolved))
		if t.Usage != nil && t.Usage.TotalTokens != nil {
			sb.WriteString(fmt.Sprintf("usage total=%d\n", *t.Usage.TotalTokens))
		}
		if t.ErrorMessage != nil {
			sb.WriteString(errStyle.Render("error: "+*t.ErrorMessage) + "\n")
		}
	}
	if len(m.turnReqs) > 0 {
		sb.WriteString(dimStyle.Render("model requests:") + "\n")
		for _, r := range m.turnReqs {
			fr := ""
			if r.FinishReason != nil {
				fr = *r.FinishReason
			}
			sb.WriteString(fmt.Sprintf(" %s %s %s %s\n", r.ID, r.Status, r.RequestedModel, fr))
		}
	}
	if len(m.turnTools) > 0 {
		sb.WriteString(dimStyle.Render("tool invocations:") + "\n")
		for _, ti := range m.turnTools {
			sb.WriteString(fmt.Sprintf(" %s %s %s out=%s artifacts=%d\n", ti.ID, ti.Name, ti.Status, previewOut(ti.Output), len(ti.ArtifactIDs)))
		}
	}
	if m.singleTool != nil {
		sb.WriteString(fmt.Sprintf("tool %s: %s\n", m.singleTool.ID, m.singleTool.Status))
	}
	if m.singleMsg != nil {
		sb.WriteString(fmt.Sprintf("message %s [%s]: %s\n", m.singleMsg.ID, m.singleMsg.Role, truncate(m.singleMsg.Text, 300)))
	}
	sb.WriteString(dimStyle.Render("enter show turn · x cancel · q-requests · t-tools · m message · i tool · r refresh") + "\n")
	return sb.String()
}

func (m Model) viewArtifacts() string {
	var sb strings.Builder
	sb.WriteString("artifact: " + m.artInput.View() + "\n")
	if m.artMeta != nil {
		sb.WriteString(fmt.Sprintf("id=%s size=%d type=%s\n", m.artMeta.ID, m.artMeta.SizeBytes, m.artMeta.MediaType))
	}
	if m.artBytes > 0 {
		sb.WriteString(fmt.Sprintf("downloaded+verified: %d bytes -> %s\n", m.artBytes, m.artOut))
	}
	sb.WriteString(dimStyle.Render("enter show · d download to ./<id>.bin · r refresh") + "\n")
	return sb.String()
}

func (m Model) viewProviders() string {
	var sb strings.Builder
	if len(m.provs) > 0 {
		for _, p := range m.provs {
			sb.WriteString(fmt.Sprintf(" %-12s key=%t chatgpt=%t exec=%t\n", p.Provider, p.APIKeyConfigured, p.ChatGPTConfigured, p.ExecutionSupported))
		}
	} else {
		sb.WriteString(dimStyle.Render("no provider status yet (press r)") + "\n")
	}
	sb.WriteString("set-key provider: " + m.keyProvider.View() + "\n")
	sb.WriteString("set-key value:    " + m.keyValue.View() + dimStyle.Render("  (enter saves via PUT, never echoed)") + "\n")
	sb.WriteString("codex login id:   " + m.loginID.View() + dimStyle.Render("  (l start login · L status)") + "\n")
	sb.WriteString(dimStyle.Render("focus: F1 provider · F2 key · F3 login — or tab between with [ ]") + "\n")
	return sb.String()
}

func (m Model) viewGo() string {
	var sb strings.Builder
	if m.gopro == nil {
		sb.WriteString(errStyle.Render("no OPENCODE_GO_API_KEY — set env or ~/projects/slopconductor/.env") + "\n")
		return sb.String()
	}
	sb.WriteString(dimStyle.Render(fmt.Sprintf("catalog %d models · live %d · stream=%t (s toggles)", len(provider.Catalog), len(m.goIDs), m.goStream)) + "\n")
	for i, mo := range provider.Catalog {
		cursor := "  "
		if i == m.goCursor {
			cursor = "▸ "
		}
		live := ""
		for _, id := range m.goIDs {
			if id == mo.ID {
				live = " ●"
				break
			}
		}
		sb.WriteString(fmt.Sprintf("%s%-28s [%-22s] %s%s\n", cursor, mo.ID, string(mo.Wire), mo.DisplayName, live))
	}
	sb.WriteString("\nprompt:\n" + m.goPrompt.View() + "\n")
	sb.WriteString(m.goView.View() + "\n")
	if m.goResult != nil {
		sb.WriteString(dimStyle.Render(fmt.Sprintf("last: %s %s resolved=%s", m.goResult.Model, m.goResult.Outcome, m.goResult.ResolvedModel)) + "\n")
	}
	sb.WriteString(dimStyle.Render("j/k move · enter run · s stream toggle · g list live models") + "\n")
	return sb.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
func previewOut(s *string) string {
	if s == nil {
		return "-"
	}
	return truncate(*s, 80)
}
