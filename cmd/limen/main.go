package main

import (
	"context"
	"database/sql"
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
	"github.com/huz/limen/internal/run"
	"github.com/huz/limen/internal/store"
	_ "github.com/lib/pq"
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

	openAIEndpoint, err := provider.EndpointForBaseURL(cfg.OpenAIBaseURL)
	if err != nil {
		logger.Error("invalid OpenAI endpoint", "error", err)
		os.Exit(1)
	}
	anthropicEndpoint, err := provider.EndpointForBaseURL(cfg.AnthropicBaseURL)
	if err != nil {
		logger.Error("invalid Anthropic endpoint", "error", err)
		os.Exit(1)
	}
	client := provider.NewSecureHTTPClient(provider.HTTPClientOptions{AllowedEndpoints: []string{openAIEndpoint, anthropicEndpoint}})
	openAI := provider.NewOpenAI(client, cfg.OpenAIBaseURL, cfg.OpenAIAPIKey)
	anthropic := provider.NewAnthropic(client, cfg.AnthropicBaseURL, cfg.AnthropicAPIKey)
	registry := gateway.NewCompatibilityRegistry()
	if len(cfg.Models) > 0 {
		models := make([]gateway.Model, 0, len(cfg.Models))
		for _, model := range cfg.Models {
			targets := make([]gateway.Target, 0, len(model.Targets))
			for _, target := range model.Targets {
				targets = append(targets, gatewayTarget(target))
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
	var runService run.Service
	var database *sql.DB
	if cfg.DatabaseURL != "" {
		database, err = sql.Open("postgres", cfg.DatabaseURL)
		if err != nil {
			logger.Error("open database failed", "error", err)
			os.Exit(1)
		}
		defer database.Close()
		pingCtx, cancelPing := context.WithTimeout(context.Background(), 5*time.Second)
		if err := database.PingContext(pingCtx); err != nil {
			cancelPing()
			logger.Error("database health check failed", "error", err)
			os.Exit(1)
		}
		cancelPing()
		migrationCtx, cancelMigration := context.WithTimeout(context.Background(), 10*time.Second)
		if err := store.ApplyMigrations(migrationCtx, database); err != nil {
			cancelMigration()
			logger.Error("database migration failed", "error", err)
			os.Exit(1)
		}
		if err := store.EnsureTenant(migrationCtx, database, cfg.TenantID); err != nil {
			cancelMigration()
			logger.Error("database tenant initialization failed", "error", err)
			os.Exit(1)
		}
		cancelMigration()
		runService = store.NewPostgresStore(database)
	}
	if runService == nil && os.Getenv("LIMEN_RUN_STORE") == "memory" {
		runService = run.NewMemoryService(nil)
	}
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.WithLogging(logger, httpapi.NewWithHealthAndRunsForTenantScopes(cfg.LimenAPIKey, router, health, cfg.TenantID, cfg.Scopes, runService)),
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

// gatewayTarget 将配置目标转换为 Router 使用的不可变目标。
func gatewayTarget(target config.Target) gateway.Target {
	streaming := true
	if target.SupportsStreaming != nil {
		streaming = *target.SupportsStreaming
	}
	return gateway.Target{
		ID:                target.ID,
		Provider:          target.Provider,
		UpstreamModel:     target.UpstreamModel,
		Capabilities:      append([]string(nil), target.Capabilities...),
		SupportsStreaming: streaming,
		QualityTier:       target.QualityTier,
		CostTier:          target.CostTier,
		ContextWindow:     target.ContextWindow,
		DataClasses:       append([]string(nil), target.DataClasses...),
		Pricing:           target.Pricing,
	}
}
