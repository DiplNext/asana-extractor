package config

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func files(m map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if c, ok := m[p]; ok {
			return []byte(c), nil
		}
		return nil, os.ErrNotExist
	}
}

var validEnv = map[string]string{
	EnvAccessToken:  "secret-token",
	EnvWorkspaceGID: "1200000000000001",
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(nil, envMap(validEnv), files(nil), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		AccessToken:        "secret-token",
		WorkspaceGID:       "1200000000000001",
		BaseURL:            DefaultBaseURL,
		OutputDir:          DefaultOutputDir,
		Interval:           5 * time.Minute,
		HTTPTimeout:        30 * time.Second,
		RateLimitPerMinute: 60,
		MaxRetries:         5,
		LogLevel:           slog.LevelInfo,
	}
	if cfg != want {
		t.Fatalf("got %+v\nwant %+v", cfg, want)
	}
}

func TestLoadIntervals(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want time.Duration
		once bool
	}{
		{nil, 5 * time.Minute, false},
		{[]string{"--interval=5m"}, 5 * time.Minute, false},
		{[]string{"--interval=30s"}, 30 * time.Second, false},
		{[]string{"--once"}, 5 * time.Minute, true},
	} {
		cfg, err := Load(tc.args, envMap(validEnv), files(nil), io.Discard)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if cfg.Interval != tc.want || cfg.Once != tc.once {
			t.Errorf("%v: interval=%s once=%v", tc.args, cfg.Interval, cfg.Once)
		}
	}
}

func TestPrecedence(t *testing.T) {
	fileContent := strings.Join([]string{
		"# comment",
		"export ASANA_ACCESS_TOKEN='file-token'",
		`ASANA_WORKSPACE_GID="111"`,
		"ASANA_EXTRACT_INTERVAL=1m",
		"ASANA_OUTPUT_DIR=file-out",
		"ASANA_MAX_RETRIES=1",
	}, "\n")
	env := map[string]string{
		EnvInterval:  "2m",
		EnvOutputDir: "env-out",
	}
	cfg, err := Load([]string{"--interval=30s"}, envMap(env), files(map[string]string{".env": fileContent}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Interval != 30*time.Second {
		t.Errorf("flag should win: %s", cfg.Interval)
	}
	if cfg.OutputDir != "env-out" {
		t.Errorf("process env should beat file: %s", cfg.OutputDir)
	}
	if cfg.AccessToken != "file-token" || cfg.WorkspaceGID != "111" || cfg.MaxRetries != 1 {
		t.Errorf("file values not applied: %+v", cfg)
	}
	if cfg.HTTPTimeout != DefaultHTTPTimeout {
		t.Errorf("default not applied: %s", cfg.HTTPTimeout)
	}
}

func TestExplicitMissingEnvFileFails(t *testing.T) {
	_, err := Load([]string{"--env-file=nope.env"}, envMap(validEnv), files(nil), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "nope.env") {
		t.Fatalf("expected env file error, got %v", err)
	}
}

func TestValidationErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		env  map[string]string
		want string
	}{
		"missing token":     {nil, map[string]string{EnvWorkspaceGID: "1"}, EnvAccessToken},
		"missing workspace": {nil, map[string]string{EnvAccessToken: "t"}, EnvWorkspaceGID},
		"bad workspace":     {[]string{"--workspace=abc"}, validEnv, EnvWorkspaceGID},
		"bad interval":      {[]string{"--interval=soon"}, validEnv, "invalid duration"},
		"tiny interval":     {[]string{"--interval=10ms"}, validEnv, "at least"},
		"zero timeout":      {[]string{"--http-timeout=0s"}, validEnv, EnvHTTPTimeout},
		"zero rate":         {[]string{"--rate-limit=0"}, validEnv, EnvRateLimit},
		"huge rate":         {[]string{"--rate-limit=100000"}, validEnv, EnvRateLimit},
		"token with space":  {nil, merge(validEnv, EnvAccessToken, "abc def"), "whitespace"},
		"bad rate":          {[]string{"--rate-limit=fast"}, validEnv, "invalid integer"},
		"negative retries":  {[]string{"--max-retries=-1"}, validEnv, EnvMaxRetries},
		"bad log level":     {[]string{"--log-level=loud"}, validEnv, EnvLogLevel},
		"extra args":        {[]string{"now"}, validEnv, "unexpected arguments"},
		"plain http remote": {nil, merge(validEnv, EnvBaseURL, "http://example.com"), "https"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(tc.args, envMap(tc.env), files(nil), io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestErrorsDoNotLeakToken(t *testing.T) {
	env := merge(validEnv, EnvWorkspaceGID, "bad")
	_, err := Load([]string{"--interval=x"}, envMap(env), files(nil), io.Discard)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoopbackHTTPAllowed(t *testing.T) {
	cfg, err := Load(nil, envMap(merge(validEnv, EnvBaseURL, "http://127.0.0.1:8080/api/")), files(nil), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "http://127.0.0.1:8080/api" {
		t.Fatalf("base url: %s", cfg.BaseURL)
	}
}

func TestHelp(t *testing.T) {
	_, err := Load([]string{"-h"}, envMap(validEnv), files(nil), io.Discard)
	if !errors.Is(err, ErrHelp) {
		t.Fatalf("want ErrHelp, got %v", err)
	}
}

func TestParseEnvRejectsGarbage(t *testing.T) {
	if _, err := ParseEnv("JUST_A_WORD"); err == nil {
		t.Fatal("expected error")
	}
}

func merge(base map[string]string, k, v string) map[string]string {
	out := map[string]string{k: v}
	for bk, bv := range base {
		if bk != k {
			out[bk] = bv
		}
	}
	return out
}

func TestParseEnvInlineCommentsAndExport(t *testing.T) {
	vars, err := ParseEnv("export\tA=1\nB=two # note\nC=\"x # kept\"\nD=#only-comment\nexportE=5\n")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "two", "C": "x # kept", "D": "", "exportE": "5"}
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("%s=%q, want %q", k, vars[k], v)
		}
	}
}

func TestParseAndValidationErrorsReportedTogether(t *testing.T) {
	_, err := Load([]string{"--interval=banana"}, envMap(map[string]string{EnvWorkspaceGID: "1"}), files(nil), io.Discard)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"invalid duration", EnvAccessToken + " is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "must be at least") {
		t.Errorf("unparsable interval must not also produce a range error:\n%v", err)
	}
}
