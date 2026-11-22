// Command server runs the portfolio chatbot API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/portpholiyo/chatbot/internal/config"
	"github.com/portpholiyo/chatbot/internal/knowledge"
	"github.com/portpholiyo/chatbot/internal/llm"
	"github.com/portpholiyo/chatbot/internal/server"
	"github.com/portpholiyo/chatbot/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	// Used by the container HEALTHCHECK (the image has no shell or curl).
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if gaps := knowledge.MissingItems(); len(gaps) > 0 {
		slog.Info("knowledge base has unfilled gaps", "count", len(gaps))
	}

	st, err := store.Open(ctx, cfg.DBHost, cfg.DBPort, cfg.DBName, cfg.DBUser, cfg.DBPassword)
	if err != nil {
		return err
	}
	defer st.Close()

	ai := llm.NewAnthropic(cfg.APIKey, cfg.Model, knowledge.Text(), cfg.MaxOutputTokens)
	srv := server.New(ctx, server.Options{
		Store:           st,
		LLM:             ai,
		Model:           ai.Model(),
		AdminToken:      cfg.AdminToken,
		IPSalt:          cfg.IPSalt,
		AllowedOrigins:  cfg.AllowedOrigins,
		ClientIPHeader:  cfg.ClientIPHeader,
		DailyCap:        cfg.DailyCap,
		MaxMessageChars: cfg.MaxMessageChars,
		HistoryTurns:    cfg.HistoryTurns,
		MaxConcurrent:   cfg.MaxConcurrent,
		IPRatePerMin:    cfg.IPRatePerMin,
		IPBurst:         cfg.IPBurst,
		RequestTimeout:  cfg.RequestTimeout,
	})

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		// WriteTimeout is deliberately unset: answers are streamed, and each
		// request is bounded by its own context timeout instead.
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.ListenAddr, "model", ai.Model())
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		slog.Info("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(sctx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
	}
	return nil
}

func healthcheck() int {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	if addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
