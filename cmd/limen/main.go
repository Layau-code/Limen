package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/huz/limen/internal/config"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/httpapi"
	"github.com/huz/limen/internal/provider"
)

// main 组装 Limen 依赖并管理 HTTP 服务生命周期。
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if exitCode, handled := runCommand(os.Args[1:], os.Stdout, os.Stderr); handled {
		os.Exit(exitCode)
	}
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	openAI := provider.NewOpenAI(http.DefaultClient, cfg.OpenAIBaseURL, cfg.OpenAIAPIKey)
	anthropic := provider.NewAnthropic(http.DefaultClient, cfg.AnthropicBaseURL, cfg.AnthropicAPIKey)
	registry := gateway.NewCompatibilityRegistry()
	if len(cfg.Models) > 0 {
		models := make([]gateway.Model, 0, len(cfg.Models))
		for _, model := range cfg.Models {
			targets := make([]gateway.Target, 0, len(model.Targets))
			for _, target := range model.Targets {
				targets = append(targets, gateway.Target{Provider: target.Provider, UpstreamModel: target.UpstreamModel})
			}
			models = append(models, gateway.Model{
				ID:          model.ID,
				Targets:     targets,
				DisplayName: model.DisplayName,
			})
		}
		registry, err = gateway.NewModelRegistry(models)
		if err != nil {
			logger.Error("invalid model registry", "error", err)
			os.Exit(1)
		}
	}
	router := gateway.NewRouter(map[string]provider.Provider{
		"openai":    openAI,
		"anthropic": anthropic,
	}, registry, gateway.Policy{
		RequestTimeout:   cfg.RequestTimeout,
		AttemptTimeout:   cfg.Routing.AttemptTimeout,
		FailureThreshold: cfg.Routing.FailureThreshold,
		Cooldown:         cfg.Routing.Cooldown,
	})
	health := httpapi.NewHealth()
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.WithLogging(logger, httpapi.NewWithHealth(cfg.LimenAPIKey, router, health)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	health.SetReady(true)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		health.SetReady(false)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown failed", "error", err)
		}
	}()

	logger.Info("Limen listening", "address", cfg.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}
