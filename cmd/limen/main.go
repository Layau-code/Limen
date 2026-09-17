package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/huz/limen/internal/auth"
	"github.com/huz/limen/internal/config"
	"github.com/huz/limen/internal/configstore"
	"github.com/huz/limen/internal/credentialstore"
	"github.com/huz/limen/internal/gateway"
	"github.com/huz/limen/internal/httpapi"
	"github.com/huz/limen/internal/journal"
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
	openAIEndpointID, err := provider.EndpointIDForBaseURL(cfg.OpenAIBaseURL)
	if err != nil {
		logger.Error("invalid OpenAI endpoint binding", "error", err)
		os.Exit(1)
	}
	anthropicEndpointID, err := provider.EndpointIDForBaseURL(cfg.AnthropicBaseURL)
	if err != nil {
		logger.Error("invalid Anthropic endpoint binding", "error", err)
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
	router.SetConfigVersion(cfg.ConfigVersion)
	health := httpapi.NewHealth()
	var runService run.Service
	var decisionStore journal.Store = journal.NewMemoryStore()
	var configStore configstore.Store = configstore.NewMemoryStore()
	var credentialStore credentialstore.Store
	var credentialSetters map[string]httpapi.ProviderCredentialSetter
	var credentialEndpoints map[string]string
	var credentialListener *store.CredentialChangeListener
	var authenticator auth.Authenticator = auth.NewStaticAuthenticator(cfg.LimenAPIKey, cfg.TenantID, cfg.Scopes)
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
		decisionStore = store.NewDecisionJournal(database)
		configStore = store.NewPostgresConfigStore(database)
		published, publishErr := configStore.GetPublished(context.Background(), cfg.TenantID)
		if publishErr == nil {
			publishedRegistry, registryErr := registryFromConfig(published.Models)
			if registryErr != nil {
				logger.Error("published model registry is invalid", "error", registryErr)
				os.Exit(1)
			}
			policy := router.Policy()
			policy.AttemptTimeout = published.Routing.AttemptTimeout
			policy.FailureThreshold = published.Routing.FailureThreshold
			policy.Cooldown = published.Routing.Cooldown
			if err := router.ReplaceRegistryWithPolicy(publishedRegistry, published.Version, policy); err != nil {
				logger.Error("published model registry activation failed", "error", err)
				os.Exit(1)
			}
			registry = publishedRegistry
			cfg.ConfigVersion = published.Version
		} else if !errors.Is(publishErr, configstore.ErrNotFound) {
			logger.Error("published model registry load failed", "error", publishErr)
			os.Exit(1)
		}
		if cfg.APIKeyStore == "postgres" {
			authenticator = store.NewAPIKeyAuthenticator(database, cfg.APIKeyHMACSecret)
		}
		if cfg.CredentialMasterKey != "" {
			masterKey, keyErr := credentialstore.ParseMasterKey(cfg.CredentialMasterKey)
			if keyErr != nil {
				logger.Error("invalid credential master key", "error", keyErr)
				os.Exit(1)
			}
			vault, vaultErr := credentialstore.NewVault(masterKey)
			if vaultErr != nil {
				logger.Error("invalid credential vault", "error", vaultErr)
				os.Exit(1)
			}
			credentials := store.NewPostgresCredentialStore(database, vault)
			credentialStore = credentials
			credentialSetters = map[string]httpapi.ProviderCredentialSetter{"openai": openAI, "anthropic": anthropic}
			credentialEndpoints = map[string]string{"openai": openAIEndpointID, "anthropic": anthropicEndpointID}
			credentialListener, err = store.NewCredentialChangeListener(cfg.DatabaseURL)
			if err != nil {
				logger.Warn("credential change listener unavailable", "error", err)
				credentialListener = nil
			}
			credentialContext, cancelCredentials := context.WithTimeout(context.Background(), 3*time.Second)
			openAIStored, openAIError := loadStoredCredential(credentialContext, credentials, cfg.TenantID, "openai", openAIEndpointID, openAI)
			anthropicStored, anthropicError := loadStoredCredential(credentialContext, credentials, cfg.TenantID, "anthropic", anthropicEndpointID, anthropic)
			cancelCredentials()
			if openAIError != nil {
				logger.Error("OpenAI credential load failed", "error", openAIError)
				os.Exit(1)
			}
			if anthropicError != nil {
				logger.Error("Anthropic credential load failed", "error", anthropicError)
				os.Exit(1)
			}
			if registryUsesProvider(registry, "openai") && cfg.OpenAIAPIKey == "" && !openAIStored {
				logger.Error("OpenAI credential is missing")
				os.Exit(1)
			}
			if registryUsesProvider(registry, "anthropic") && cfg.AnthropicAPIKey == "" && !anthropicStored {
				logger.Error("Anthropic credential is missing")
				os.Exit(1)
			}
		}
	}
	if runService == nil && os.Getenv("LIMEN_RUN_STORE") == "memory" {
		runService = run.NewMemoryService(nil)
	}
	if credentialStore == nil {
		if err := validateRuntimeProviderKeys(registry, cfg); err != nil {
			logger.Error("provider credential is missing", "error", err)
			os.Exit(1)
		}
	}
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.WithLogging(logger, httpapi.NewWithHealthAndRunsForTenantAuthenticatorJournalConfigCredentials(authenticator, router, health, cfg.TenantID, decisionStore, configStore, credentialStore, credentialSetters, credentialEndpoints, runService)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	health.SetReady(true)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if credentialListener != nil {
		defer credentialListener.Close()
		go watchCredentialChanges(ctx, credentialListener, cfg.TenantID, credentialStore, credentialSetters, credentialEndpoints, logger)
	}
	if leaseService, ok := runService.(run.LeaseService); ok {
		go recoverExpiredRequests(ctx, leaseService, cfg.TenantID, logger)
	}
	if settlementService, ok := runService.(run.SettlementRecoveryService); ok {
		go recoverPendingSettlements(ctx, settlementService, cfg.TenantID, newSettlementWorkerOwner(), logger)
	}
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

// watchCredentialChanges 监听凭据变更并刷新当前租户的 Provider 密钥。
func watchCredentialChanges(ctx context.Context, listener *store.CredentialChangeListener, tenantID string, credentials credentialstore.Store, setters map[string]httpapi.ProviderCredentialSetter, endpoints map[string]string, logger *slog.Logger) {
	err := listener.Run(ctx, func(change store.CredentialChange) {
		if change.TenantID != tenantID || credentials == nil || endpoints[change.Provider] != change.EndpointID {
			return
		}
		setter := setters[change.Provider]
		if setter == nil {
			return
		}
		if change.Revoked {
			setter.ClearAPIKey()
			return
		}
		refreshContext, cancel := context.WithTimeout(ctx, 3*time.Second)
		loaded, loadErr := loadStoredCredential(refreshContext, credentials, tenantID, change.Provider, change.EndpointID, setter)
		cancel()
		if loadErr != nil {
			logger.Error("provider credential refresh failed", "provider", change.Provider, "error", loadErr)
			return
		}
		if !loaded {
			setter.ClearAPIKey()
		}
	})
	if err != nil && ctx.Err() == nil {
		logger.Error("credential change listener stopped", "error", err)
	}
}

// newSettlementWorkerOwner 为后台结算任务生成不含业务正文的执行实例标识。
func newSettlementWorkerOwner() string {
	owner, err := run.NewID("settlement-worker")
	if err != nil {
		return "settlement-worker-local"
	}
	return owner
}

// validateRuntimeProviderKeys 在数据库配置生效后按当前目录校验环境密钥。
func validateRuntimeProviderKeys(registry *gateway.ModelRegistry, cfg config.Config) error {
	if registryUsesProvider(registry, "openai") && strings.TrimSpace(cfg.OpenAIAPIKey) == "" {
		return errors.New("OPENAI_API_KEY is required by the active model registry")
	}
	if registryUsesProvider(registry, "anthropic") && strings.TrimSpace(cfg.AnthropicAPIKey) == "" {
		return errors.New("ANTHROPIC_API_KEY is required by the active model registry")
	}
	return nil
}

type apiKeySetter interface {
	SetAPIKey(string) error
}

// loadStoredCredential 读取加密凭据并在内存中短暂交给对应 Provider。
func loadStoredCredential(ctx context.Context, store credentialstore.Store, tenantID, providerName, endpointID string, setter apiKeySetter) (bool, error) {
	secret, _, err := store.Resolve(ctx, tenantID, providerName, endpointID)
	if errors.Is(err, credentialstore.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() {
		for index := range secret {
			secret[index] = 0
		}
	}()
	if err := setter.SetAPIKey(string(secret)); err != nil {
		return false, err
	}
	return true, nil
}

// registryUsesProvider 判断当前有效目录是否引用指定 Provider。
func registryUsesProvider(registry *gateway.ModelRegistry, providerName string) bool {
	for _, model := range registry.List() {
		for _, target := range model.Targets {
			if target.Provider == providerName {
				return true
			}
		}
	}
	return false
}

// recoverExpiredRequests 定期回收崩溃实例遗留的请求租约，不重放上游调用。
func recoverExpiredRequests(ctx context.Context, service run.LeaseService, tenantID string, logger *slog.Logger) {
	ticker := time.NewTicker(run.RequestLeaseRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			recoveryContext, cancel := context.WithTimeout(ctx, 5*time.Second)
			requests, err := service.RecoverExpiredRequests(recoveryContext, tenantID, time.Now().UTC(), 100)
			cancel()
			if err != nil {
				logger.Error("expired request recovery failed", "error", err)
				continue
			}
			if len(requests) > 0 {
				logger.Warn("expired requests recovered", "count", len(requests))
			}
		case <-ctx.Done():
			return
		}
	}
}

// recoverPendingSettlements 定期领取并恢复未完成的结算任务。
func recoverPendingSettlements(ctx context.Context, service run.SettlementRecoveryService, tenantID, owner string, logger *slog.Logger) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			recoveryContext, cancel := context.WithTimeout(ctx, 5*time.Second)
			processed, err := run.ProcessSettlementJobs(recoveryContext, service, tenantID, owner, time.Now().UTC(), 100)
			cancel()
			if err != nil {
				logger.Error("pending settlement recovery failed", "error", err)
				continue
			}
			if processed > 0 {
				logger.Info("pending settlements recovered", "count", processed)
			}
		case <-ctx.Done():
			return
		}
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

// registryFromConfig 将数据库配置转换为可替换的只读模型目录。
func registryFromConfig(models []config.Model) (*gateway.ModelRegistry, error) {
	converted := make([]gateway.Model, 0, len(models))
	for _, model := range models {
		targets := make([]gateway.Target, 0, len(model.Targets))
		for _, target := range model.Targets {
			targets = append(targets, gatewayTarget(target))
		}
		converted = append(converted, gateway.Model{ID: model.ID, DisplayName: model.DisplayName, Targets: targets})
	}
	return gateway.NewModelRegistry(converted)
}
