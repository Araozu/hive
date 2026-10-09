package config

import "testing"

func TestLoopbackURLParsing(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"http://localhost:7331", true},
		{"http://127.0.0.1:7331", true},
		{"https://[::1]:7331", true},
		{"http://[::1]", true},
		{"https://example.com", false},
		{"http://localhost.example.com", false},
		{"http://localhost@remote.example.com", false},
		{"http://user@localhost", false},
		{"localhost:7331", false},
		{"http://[::1", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			if got := IsLoopback(tc.url); got != tc.want {
				t.Fatalf("IsLoopback = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestResolveExplicitValuesWinAndTrimWhitespace(t *testing.T) {
	t.Setenv("SLOP_DAEMON_URL", "http://env.example.com")
	t.Setenv("SLOP_TOKEN_FILE", "/env-token")
	cfg := Resolve(" http://flag.example.com ", " /flag-token ")
	if cfg.DaemonURL != "http://flag.example.com" || cfg.TokenFile != "/flag-token" {
		t.Fatalf("explicit config = %+v", struct{ URL, Token string }{cfg.DaemonURL, cfg.TokenFile})
	}
}
