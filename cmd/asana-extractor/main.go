// Command asana-extractor exports the users and projects of one Asana
// workspace to one JSON file per resource, once or on a fixed interval.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"asana-extractor/internal/asana"
	"asana-extractor/internal/config"
	"asana-extractor/internal/extractor"
	"asana-extractor/internal/scheduler"
	"asana-extractor/internal/storage"
)

const (
	exitOK      = 0
	exitFailure = 1 // --once extraction failed (or was interrupted)
	exitConfig  = 2 // invalid startup configuration
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop() // restore default handling: a second signal terminates immediately
	}()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.ReadFile, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, readFile func(string) ([]byte, error), stderr io.Writer) int {
	cfg, err := config.Load(args, getenv, readFile, stderr)
	if errors.Is(err, config.ErrHelp) {
		return exitOK
	}
	if err != nil {
		fmt.Fprintf(stderr, "configuration error:\n%v\n", err)
		return exitConfig
	}

	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))

	client := asana.New(asana.Options{
		BaseURL:            cfg.BaseURL,
		Token:              cfg.AccessToken,
		HTTPTimeout:        cfg.HTTPTimeout,
		RateLimitPerMinute: cfg.RateLimitPerMinute,
		MaxRetries:         cfg.MaxRetries,
		Logger:             log,
	})
	store := storage.New(cfg.OutputDir)
	if n, err := store.CleanupTemp(time.Minute); err != nil {
		log.Warn("could not remove leftover temp files", "err", err)
	} else if n > 0 {
		log.Info("removed leftover temp files from a previous crash", "count", n)
	}
	ex := extractor.New(client, store, cfg.WorkspaceGID, log)

	mode := "periodic"
	if cfg.Once {
		mode = "once"
	}
	log.Info("asana-extractor starting",
		"mode", mode,
		"interval", cfg.Interval,
		"workspace", cfg.WorkspaceGID,
		"output_dir", cfg.OutputDir,
		"rate_limit_per_minute", cfg.RateLimitPerMinute,
		"max_retries", cfg.MaxRetries,
		"http_timeout", cfg.HTTPTimeout,
	)

	if cfg.Once {
		if _, err := ex.Run(ctx); err != nil {
			return exitFailure // already logged with details
		}
		return exitOK
	}

	scheduler.New(cfg.Interval, log).Run(ctx, func(ctx context.Context) {
		_, _ = ex.Run(ctx) // failures are logged; the next scheduled run proceeds
	})
	log.Info("shutdown complete")
	return exitOK
}
