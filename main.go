package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type config struct {
	port     string
	basePath string
	maxHold  time.Duration
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           newHandler(cfg, logger),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.ListenAndServe()
	}()
	logger.Info("listening", "port", cfg.port, "basePath", cfg.basePath, "maxHold", cfg.maxHold.String())

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Warn("closing requests that are still held", "error", err)
		return server.Close()
	}
	return nil
}

func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{port: "8080", maxHold: 30 * time.Minute}

	if port := getenv("PORT"); port != "" {
		cfg.port = port
	}

	if basePath := strings.TrimSuffix(getenv("BASE_PATH"), "/"); basePath != "" {
		if !strings.HasPrefix(basePath, "/") {
			return cfg, fmt.Errorf("BASE_PATH must start with /, got %q", basePath)
		}
		cfg.basePath = basePath
	}

	if maxHold := getenv("MAX_HOLD"); maxHold != "" {
		parsed, err := parseDuration(maxHold)
		if err != nil {
			return cfg, fmt.Errorf("MAX_HOLD: %w", err)
		}
		cfg.maxHold = parsed
	}

	return cfg, nil
}
