// Package config loads and validates runtime configuration.
//
// Precedence, highest first: command-line flags, process environment,
// env file (default ".env"), built-in defaults. The access token is accepted
// only from the environment or the env file so it never appears in process
// listings or shell history.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	EnvAccessToken  = "ASANA_ACCESS_TOKEN"
	EnvWorkspaceGID = "ASANA_WORKSPACE_GID"
	EnvOutputDir    = "ASANA_OUTPUT_DIR"
	EnvInterval     = "ASANA_EXTRACT_INTERVAL"
	EnvHTTPTimeout  = "ASANA_HTTP_TIMEOUT"
	EnvRateLimit    = "ASANA_RATE_LIMIT_PER_MINUTE"
	EnvMaxRetries   = "ASANA_MAX_RETRIES"
	EnvBaseURL      = "ASANA_BASE_URL"
	EnvLogLevel     = "LOG_LEVEL"

	DefaultBaseURL     = "https://app.asana.com/api/1.0"
	DefaultOutputDir   = "output"
	DefaultInterval    = 5 * time.Minute
	DefaultHTTPTimeout = 30 * time.Second
	DefaultRateLimit   = 60
	DefaultMaxRetries  = 5
	DefaultEnvFile     = ".env"

	minInterval   = time.Second
	maxRetriesCap = 20
	maxRateLimit  = 1500 // Asana's documented paid-tier quota per minute
)

var gidPattern = regexp.MustCompile(`^[0-9]{1,64}$`)

type Config struct {
	AccessToken        string
	WorkspaceGID       string
	BaseURL            string
	OutputDir          string
	Interval           time.Duration
	Once               bool
	HTTPTimeout        time.Duration
	RateLimitPerMinute int
	MaxRetries         int
	LogLevel           slog.Level
}

// ErrHelp is returned when -h/--help was requested; usage has already been printed.
var ErrHelp = flag.ErrHelp

// Load parses args (without the program name) and resolves every setting.
// getenv reads the process environment; readFile reads the env file.
func Load(args []string, getenv func(string) string, readFile func(string) ([]byte, error), usage io.Writer) (Config, error) {
	fs := flag.NewFlagSet("asana-extractor", flag.ContinueOnError)
	fs.SetOutput(usage)

	envFile := fs.String("env-file", DefaultEnvFile, "path to a KEY=VALUE env file; a missing default file is ignored")
	workspace := fs.String("workspace", "", "Asana workspace GID (env "+EnvWorkspaceGID+")")
	outputDir := fs.String("output-dir", "", "output directory (env "+EnvOutputDir+", default "+DefaultOutputDir+")")
	interval := fs.String("interval", "", "extraction interval, e.g. 5m or 30s (env "+EnvInterval+", default 5m)")
	once := fs.Bool("once", false, "run a single extraction and exit; nonzero exit status on failure")
	httpTimeout := fs.String("http-timeout", "", "per-request HTTP timeout (env "+EnvHTTPTimeout+", default 30s)")
	rateLimit := fs.String("rate-limit", "", "max API requests per minute (env "+EnvRateLimit+", default 60)")
	maxRetries := fs.String("max-retries", "", "max retries per request after the first attempt (env "+EnvMaxRetries+", default 5)")
	logLevel := fs.String("log-level", "", "debug, info, warn or error (env "+EnvLogLevel+", default info)")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	fileVars, err := loadEnvFile(*envFile, explicit["env-file"], readFile)
	if err != nil {
		return Config{}, err
	}

	// lookup applies flag > process env > env file > default.
	lookup := func(flagName, flagValue, envKey, def string) string {
		if flagName != "" && explicit[flagName] {
			return flagValue
		}
		if v := getenv(envKey); v != "" {
			return v
		}
		if v, ok := fileVars[envKey]; ok && v != "" {
			return v
		}
		return def
	}

	cfg := Config{
		AccessToken:  strings.TrimSpace(lookup("", "", EnvAccessToken, "")),
		WorkspaceGID: strings.TrimSpace(lookup("workspace", *workspace, EnvWorkspaceGID, "")),
		BaseURL:      strings.TrimRight(lookup("", "", EnvBaseURL, DefaultBaseURL), "/"),
		OutputDir:    lookup("output-dir", *outputDir, EnvOutputDir, DefaultOutputDir),
		Once:         *once,
	}

	// A value that fails to parse keeps its default, so Validate reports only
	// real problems and the user sees parse and validation errors together.
	var parseErrs []error
	cfg.Interval = parseDuration(lookup("interval", *interval, EnvInterval, ""), DefaultInterval, "interval", EnvInterval, &parseErrs)
	cfg.HTTPTimeout = parseDuration(lookup("http-timeout", *httpTimeout, EnvHTTPTimeout, ""), DefaultHTTPTimeout, "http-timeout", EnvHTTPTimeout, &parseErrs)
	cfg.RateLimitPerMinute = parseInt(lookup("rate-limit", *rateLimit, EnvRateLimit, ""), DefaultRateLimit, "rate-limit", EnvRateLimit, &parseErrs)
	cfg.MaxRetries = parseInt(lookup("max-retries", *maxRetries, EnvMaxRetries, ""), DefaultMaxRetries, "max-retries", EnvMaxRetries, &parseErrs)
	cfg.LogLevel = parseLevel(lookup("log-level", *logLevel, EnvLogLevel, ""), &parseErrs)

	if err := errors.Join(append(parseErrs, cfg.Validate())...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks semantic constraints and reports every problem at once.
func (c Config) Validate() error {
	var errs []error
	if c.AccessToken == "" {
		errs = append(errs, fmt.Errorf("%s is required", EnvAccessToken))
	} else if strings.IndexFunc(c.AccessToken, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		errs = append(errs, fmt.Errorf("%s contains whitespace or control characters", EnvAccessToken))
	}
	if !gidPattern.MatchString(c.WorkspaceGID) {
		errs = append(errs, fmt.Errorf("%s (--workspace) must be a numeric Asana GID, got %q", EnvWorkspaceGID, c.WorkspaceGID))
	}
	if strings.TrimSpace(c.OutputDir) == "" {
		errs = append(errs, fmt.Errorf("%s (--output-dir) must not be empty", EnvOutputDir))
	}
	if c.Interval < minInterval {
		errs = append(errs, fmt.Errorf("%s (--interval) must be at least %s, got %s", EnvInterval, minInterval, c.Interval))
	}
	if c.HTTPTimeout <= 0 {
		errs = append(errs, fmt.Errorf("%s (--http-timeout) must be positive, got %s", EnvHTTPTimeout, c.HTTPTimeout))
	}
	if c.RateLimitPerMinute < 1 || c.RateLimitPerMinute > maxRateLimit {
		errs = append(errs, fmt.Errorf("%s (--rate-limit) must be between 1 and %d, got %d", EnvRateLimit, maxRateLimit, c.RateLimitPerMinute))
	}
	if c.MaxRetries < 0 || c.MaxRetries > maxRetriesCap {
		errs = append(errs, fmt.Errorf("%s (--max-retries) must be between 0 and %d, got %d", EnvMaxRetries, maxRetriesCap, c.MaxRetries))
	}
	if err := validateBaseURL(c.BaseURL); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// validateBaseURL requires HTTPS so the token is never sent in clear text;
// plain HTTP is allowed only for loopback hosts (local mock servers in tests).
func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s must be an absolute URL, got %q", EnvBaseURL, raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if ip := net.ParseIP(u.Hostname()); (ip != nil && ip.IsLoopback()) || u.Hostname() == "localhost" {
			return nil
		}
	}
	return fmt.Errorf("%s must use https (http is allowed only for loopback hosts), got %q", EnvBaseURL, raw)
}

func loadEnvFile(path string, explicit bool, readFile func(string) ([]byte, error)) (map[string]string, error) {
	data, err := readFile(path)
	if errors.Is(err, os.ErrNotExist) && !explicit {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read env file %s: %w", path, err)
	}
	vars, err := ParseEnv(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse env file %s: %w", path, err)
	}
	return vars, nil
}

// ParseEnv parses KEY=VALUE lines. Blank lines and lines starting with # are
// ignored, an optional "export" prefix is accepted, values wrapped in
// matching single or double quotes are unquoted, and for unquoted values a
// " #" starts an inline comment. No variable expansion.
func ParseEnv(content string) (map[string]string, error) {
	vars := map[string]string{}
	for i, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			line = strings.TrimSpace(rest)
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t") {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", i+1)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		} else if i := strings.Index(value, " #"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		} else if strings.HasPrefix(value, "#") {
			value = ""
		}
		vars[key] = value
	}
	return vars, nil
}

func parseDuration(raw string, def time.Duration, flagName, envKey string, errs *[]error) time.Duration {
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s (--%s): invalid duration %q", envKey, flagName, raw))
		return def
	}
	return d
}

func parseInt(raw string, def int, flagName, envKey string, errs *[]error) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s (--%s): invalid integer %q", envKey, flagName, raw))
		return def
	}
	return n
}

func parseLevel(raw string, errs *[]error) slog.Level {
	var l slog.Level
	if raw == "" {
		return slog.LevelInfo
	}
	if err := l.UnmarshalText([]byte(strings.TrimSpace(raw))); err != nil {
		*errs = append(*errs, fmt.Errorf("%s (--log-level): invalid level %q", EnvLogLevel, raw))
		return slog.LevelInfo
	}
	return l
}
