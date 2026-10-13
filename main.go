// hive is a Bubble Tea TUI for the slopconductor daemon plus a direct
// OpenCode Go probe. It exercises every daemon capability (health, node,
// models, capabilities, sessions, history, send, events, turns, requests,
// tools, messages, artifacts, providers) and every provider wire shape
// (chat completions, responses, messages; streaming + non-streaming).
//
// Closing hive never cancels daemon work; cancellation is an explicit key.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"hive/internal/client"
	"hive/internal/config"
	"hive/internal/provider"
	"hive/internal/ui"
)

const version = "0.1.0"

func main() {
	daemonFlag := flag.String("daemon", "", "daemon origin (default "+config.DefaultDaemonURL+", env SLOP_DAEMON_URL)")
	tokenFlag := flag.String("token-file", "", "local API bearer token file (env SLOP_TOKEN_FILE)")
	smoke := flag.Bool("smoke-daemon", false, "headless daemon capability check (no TUI)")
	probe := flag.Bool("probe-go", false, "headless direct opencode-go probe (list + tiny turn)")
	showVer := flag.Bool("version", false, "print version")
	fullContext := flag.Bool("full-context", false, "opt in to the model's full context window (default cost cap: 275k tokens)")
	flag.Parse()

	if *showVer {
		fmt.Println("hive", version)
		return
	}
	cfg := config.Resolve(*daemonFlag, *tokenFlag)
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "full-context" {
			cfg.FullContext = fullContext
		}
	})

	if *smoke {
		os.Exit(smokeDaemon(cfg))
	}
	if *probe {
		os.Exit(probeGo(cfg))
	}

	m, err := ui.New(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hive:", err)
		os.Exit(1)
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "hive:", err)
		os.Exit(1)
	}
}

// smokeDaemon exercises the full daemon read surface headlessly.
func smokeDaemon(cfg config.Config) int {
	fail := func(format string, args ...any) int {
		fmt.Printf("FAIL "+format+"\n", args...)
		return 1
	}
	fmt.Println("daemon:", cfg.DaemonURL)
	c, err := client.New(cfg.DaemonURL)
	if err != nil {
		return fail("client: %v", err)
	}
	h, err := c.Health()
	if err != nil {
		return fail("health: %v", err)
	}
	fmt.Printf("health ok: %s %s api=%d caps=%s\n", h.Service, h.Version, h.APIVersion, strings.Join(h.Capabilities, ","))
	if !cfg.TokenAvails {
		fmt.Printf("no token file (%s); authenticated checks skipped\n", cfg.TokenFile)
		fmt.Println("SMOKE-PARTIAL: anonymous health only")
		return 0
	}
	c, err = client.NewWithTokenFile(cfg.DaemonURL, cfg.TokenFile)
	if err != nil {
		return fail("token: %v", err)
	}
	n, err := c.Node()
	if err != nil {
		return fail("node: %v", err)
	}
	fmt.Printf("node ok: %s (%s) os=%s\n", n.Name, n.NodeID, n.OS)
	mo, err := c.Models()
	if err != nil {
		return fail("models: %v", err)
	}
	fmt.Printf("models ok: %d\n", len(mo))
	cp, err := c.Capabilities()
	if err != nil {
		return fail("capabilities: %v", err)
	}
	fmt.Printf("capabilities ok: tools=%d steering=%t pause-resume=%t\n", len(cp.Tools), cp.ExecutionSteering, cp.TurnPauseResume)
	pv, err := c.Providers()
	if err != nil {
		return fail("providers: %v", err)
	}
	fmt.Printf("providers ok: %d\n", len(pv))
	ss, err := c.ListSessions(nil, u32p(20))
	if err != nil {
		return fail("sessions: %v", err)
	}
	fmt.Printf("sessions ok: %d listed\n", len(ss.Items))
	if len(ss.Items) > 0 {
		ins, err := c.SteeringInstructions(ss.Items[0].ID, nil, u32p(5))
		if err != nil {
			return fail("instructions: %v", err)
		}
		fmt.Printf("instructions ok: %d listed\n", len(ins.Items))
	}
	fmt.Println("SMOKE-OK: daemon read surface")
	return 0
}

func u32p(v uint32) *uint32 { return &v }

// probeGo exercises the direct provider: catalog, live list, and one tiny
// turn on the cheapest chat wire (glm-5.3-flash). Budget: ~32 output tokens.
func probeGo(cfg config.Config) int {
	if cfg.GoAPIKey == "" {
		fmt.Println("FAIL no OPENCODE_GO_API_KEY (env or ~/projects/slopconductor/.env)")
		return 1
	}
	fmt.Printf("go key: %s\n", cfg.GoKeySource)
	c, err := provider.New(cfg.GoAPIKey)
	if err != nil {
		fmt.Println("FAIL client:", err)
		return 1
	}
	ids, err := c.ListModels()
	if err != nil {
		fmt.Println("FAIL list_models:", err)
		return 1
	}
	fmt.Printf("live models: %d\n", len(ids))
	for _, want := range []string{"glm-5.3-flash", "claude-haiku-5-5", "gpt-6-luna"} {
		found := false
		for _, id := range ids {
			if id == want {
				found = true
				break
			}
		}
		fmt.Printf("  %-18s live=%t\n", want, found)
	}
	// One tiny non-streaming chat-completions turn.
	r, err := c.Complete("glm-5.3-flash", "Reply with exactly: ok", 32, "hive-probe")
	if err != nil {
		fmt.Println("FAIL chat turn:", err)
		return 1
	}
	fmt.Printf("chat turn ok: wire=%s outcome=%s text=%q\n", r.Wire, r.Outcome, truncate(r.Text, 80))
	if r.Usage.Total != nil {
		fmt.Printf("usage total=%d\n", *r.Usage.Total)
	}
	fmt.Println("PROBE-OK: direct provider")
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
