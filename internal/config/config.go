// Package config resolves daemon connectivity and credentials the same
// way slop-cli does: explicit flags win, then SLOP_* env, then loopback
// defaults. It also loads OPENCODE_GO_API_KEY from the slopconductor .env
// for the direct provider probe.
package config

import (
	"bufio"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const DefaultDaemonURL = "http://127.0.0.1:7331"

// Config is the resolved hive runtime configuration.
type Config struct {
	DaemonURL   string
	TokenFile   string
	TokenAvails bool
	GoAPIKey    string
	GoKeySource string
	FullContext *bool
}

// Resolve builds a Config from flags/env/defaults.
func Resolve(daemonFlag, tokenFlag string) Config {
	daemon := firstNonEmpty(daemonFlag, os.Getenv("SLOP_DAEMON_URL"), DefaultDaemonURL)
	token := firstNonEmpty(tokenFlag, os.Getenv("SLOP_TOKEN_FILE"), "")
	if token == "" && IsLoopback(daemon) {
		token = DefaultTokenFile()
	}
	cfg := Config{DaemonURL: daemon, TokenFile: token}
	if token != "" {
		if st, err := os.Stat(token); err == nil && !st.IsDir() {
			cfg.TokenAvails = true
		}
	}
	key, src := ResolveGoKey()
	cfg.GoAPIKey = key
	cfg.GoKeySource = src
	return cfg
}

// ResolveGoKey returns OPENCODE_GO_API_KEY from env or the slopconductor .env.
func ResolveGoKey() (string, string) {
	if v := strings.TrimSpace(os.Getenv("OPENCODE_GO_API_KEY")); v != "" {
		return v, "env:OPENCODE_GO_API_KEY"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	path := filepath.Join(home, "projects/slopconductor/.env")
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.Index(line, "="); i > 0 {
			k := strings.TrimSpace(line[:i])
			v := strings.TrimSpace(line[i+1:])
			v = strings.Trim(v, `"'`)
			if k == "OPENCODE_GO_API_KEY" && v != "" {
				return v, "file:" + path
			}
		}
	}
	return "", ""
}

// DefaultTokenFile mirrors slop-cli: XDG_DATA_HOME or ~/.local/share.
func DefaultTokenFile() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" && filepath.IsAbs(v) {
		return filepath.Join(v, "slopconductor/credentials/local-api-token")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local/share/slopconductor/credentials/local-api-token")
}

// IsLoopback reports whether an endpoint URL targets localhost/loopback.
func IsLoopback(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
