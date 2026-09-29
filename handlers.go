package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"
)

const (
	defaultDripInterval = 10 * time.Second
	largestSeconds      = 1e9
)

type holdResult struct {
	Mode      string `json:"mode"`
	Requested string `json:"requested"`
	Held      string `json:"held"`
}

func newHandler(cfg config, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /hold/{duration}", holdHandler(cfg.maxHold, logger))
	mux.HandleFunc("GET /drip/{duration}", dripHandler(cfg.maxHold, logger))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	if cfg.basePath == "" {
		return mux
	}
	return http.StripPrefix(cfg.basePath, mux)
}

func holdHandler(maxHold time.Duration, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hold, err := requestedHold(r.PathValue("duration"), maxHold)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		reqLog := requestLogger(logger, r, "hold", hold)
		start := time.Now()
		reqLog.Info("hold started")

		timer := time.NewTimer(hold)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			reqLog.Warn("caller gave up", "after", since(start))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		result := holdResult{Mode: "hold", Requested: hold.String(), Held: since(start)}
		if err := json.NewEncoder(w).Encode(result); err != nil {
			reqLog.Warn("could not send response", "after", since(start), "error", err)
			return
		}
		reqLog.Info("hold finished", "after", since(start))
	}
}

func dripHandler(maxHold time.Duration, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		total, err := requestedHold(r.PathValue("duration"), maxHold)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		interval, err := dripInterval(r.URL.Query().Get("interval"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		reqLog := requestLogger(logger, r, "drip", total).With("interval", interval.String())
		start := time.Now()

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		sender := http.NewResponseController(w)
		if err := sender.Flush(); err != nil {
			reqLog.Warn("could not send headers", "error", err)
			return
		}
		reqLog.Info("drip started")

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		deadline := time.NewTimer(total)
		defer deadline.Stop()

		for {
			select {
			case <-deadline.C:
				if err := sendLine(w, sender, "done after "+since(start)); err != nil {
					reqLog.Warn("caller gave up", "after", since(start), "error", err)
					return
				}
				reqLog.Info("drip finished", "after", since(start))
				return
			case <-ticker.C:
				if err := sendLine(w, sender, "still here after "+since(start)); err != nil {
					reqLog.Warn("caller gave up", "after", since(start), "error", err)
					return
				}
			case <-r.Context().Done():
				reqLog.Warn("caller gave up", "after", since(start))
				return
			}
		}
	}
}

func requestedHold(raw string, maxHold time.Duration) (time.Duration, error) {
	hold, err := parseDuration(raw)
	if err != nil {
		return 0, err
	}
	if hold > maxHold {
		return 0, fmt.Errorf("%s is longer than the max hold of %s", hold, maxHold)
	}
	return hold, nil
}

func dripInterval(raw string) (time.Duration, error) {
	if raw == "" {
		return defaultDripInterval, nil
	}
	interval, err := parseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("interval: %w", err)
	}
	if interval == 0 {
		return 0, errors.New("interval must be above zero")
	}
	return interval, nil
}

func parseDuration(raw string) (time.Duration, error) {
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
		if math.IsNaN(seconds) || seconds < 0 || seconds > largestSeconds {
			return 0, fmt.Errorf("%q is not a usable number of seconds", raw)
		}
		return time.Duration(seconds * float64(time.Second)), nil
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration, use seconds like 300 or a Go duration like 5m", raw)
	}
	if parsed < 0 {
		return 0, fmt.Errorf("%q is negative", raw)
	}
	return parsed, nil
}

func sendLine(w http.ResponseWriter, sender *http.ResponseController, line string) error {
	if _, err := fmt.Fprintln(w, line); err != nil {
		return err
	}
	return sender.Flush()
}

func requestLogger(logger *slog.Logger, r *http.Request, mode string, hold time.Duration) *slog.Logger {
	return logger.With(
		"mode", mode,
		"requested", hold.String(),
		"probe", r.URL.Query().Get("probe"),
		"remote", r.RemoteAddr,
		"forwardedFor", r.Header.Get("X-Forwarded-For"),
	)
}

func since(start time.Time) string {
	return time.Since(start).Round(time.Millisecond).String()
}
