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
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	openAI := provider.NewOpenAI(http.DefaultClient, cfg.OpenAIBaseURL, cfg.OpenAIAPIKey, cfg.RequestTimeout)
	anthropic := provider.NewAnthropic(http.DefaultClient, cfg.AnthropicBaseURL, cfg.AnthropicAPIKey, cfg.RequestTimeout)
	registry := gateway.NewCompatibilityRegistry()
	if len(cfg.Models) > 0 {
		models := make([]gateway.Model, 0, len(cfg.Models))
		for _, model := range cfg.Models {
			target := model.Targets[0]
			models = append(models, gateway.Model{
				ID:            model.ID,
				Provider:      target.Provider,
				UpstreamModel: target.UpstreamModel,
				DisplayName:   model.DisplayName,
			})
		}
		registry, err = gateway.NewModelRegistry(models)
		if err != nil {
			logger.Error("invalid model registry", "error", err)
			os.Exit(1)
		}
	}
	router := gateway.NewRouter(openAI, anthropic, registry)
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.WithLogging(logger, httpapi.New(cfg.LimenAPIKey, router)),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
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
