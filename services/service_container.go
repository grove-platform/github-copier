package services

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/grove-platform/github-copier/configs"
)

// ServiceContainer holds all application services with dependency injection
type ServiceContainer struct {
	Config           *configs.Config
	FileStateService FileStateService
	TokenManager     *TokenManager

	// New services
	ConfigLoader      ConfigLoader
	PatternMatcher    PatternMatcher
	PathTransformer   PathTransformer
	MessageTemplater  MessageTemplater
	PRTemplateFetcher PRTemplateFetcher
	AuditLogger       AuditLogger
	MetricsCollector  *MetricsCollector
	SlackNotifier     SlackNotifier

	// Webhook deduplication
	DeliveryTracker *DeliveryTracker

	// Server state
	StartTime time.Time

	// Background goroutine tracking (for graceful shutdown and tests)
	wg sync.WaitGroup

	// Shutdown state
	closeOnce sync.Once
	closed    bool
}

// NewServiceContainer creates and initializes all services
func NewServiceContainer(config *configs.Config) (*ServiceContainer, error) {
	// Initialize file state service
	fileStateService := NewFileStateService()

	// Initialize config loader based on configuration, wrapped with an optional TTL cache
	var configLoader ConfigLoader
	if config.UseMainConfig && config.MainConfigFile != "" {
		// Use main config loader for new format with workflow references (when USE_MAIN_CONFIG=true)
		configLoader = NewMainConfigLoader()
	} else {
		// Deprecated: the legacy single-file config path will be removed in a future release.
		LogWarning("DEPRECATION: USE_MAIN_CONFIG is not set or MAIN_CONFIG_FILE is empty. "+
			"The legacy single-file config path will be removed in a future release. "+
			"Migrate to the main config format. Falling back to: %s", config.EffectiveConfigFile())
		configLoader = NewConfigLoader()
	}
	configLoader = NewCachedConfigLoader(configLoader, time.Duration(config.ConfigCacheTTLSeconds)*time.Second)

	patternMatcher := NewPatternMatcher()
	pathTransformer := NewPathTransformer()
	messageTemplater := NewMessageTemplater()
	prTemplateFetcher := NewPRTemplateFetcher()
	metricsCollector := NewMetricsCollector()

	// Initialize Slack notifier
	// Use plain text mode for Workflow Builder webhooks (they don't support attachments)
	slackNotifier := NewSlackNotifierWithOptions(
		config.SlackWebhookURL,
		config.SlackChannel,
		config.SlackUsername,
		config.SlackIconEmoji,
		config.SlackPlainText,
		config.SlackMessageVariable,
	)

	// Initialize audit logger
	ctx := context.Background()
	auditLogger, err := NewMongoAuditLogger(
		ctx,
		config.MongoURI,
		config.AuditDatabase,
		config.AuditCollection,
		config.AuditEnabled,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize audit logger: %w", err)
	}

	return &ServiceContainer{
		Config:            config,
		FileStateService:  fileStateService,
		TokenManager:      defaultTokenManager,
		ConfigLoader:      configLoader,
		PatternMatcher:    patternMatcher,
		PathTransformer:   pathTransformer,
		MessageTemplater:  messageTemplater,
		PRTemplateFetcher: prTemplateFetcher,
		AuditLogger:       auditLogger,
		MetricsCollector:  metricsCollector,
		SlackNotifier:     slackNotifier,
		DeliveryTracker:   NewDeliveryTracker(1 * time.Hour),
		StartTime:         time.Now(),
	}, nil
}

// Wait blocks until all background goroutines tracked by this container have finished.
func (sc *ServiceContainer) Wait() {
	sc.wg.Wait()
}

// Close cleans up resources. Safe to call multiple times.
func (sc *ServiceContainer) Close(ctx context.Context) error {
	var closeErr error
	sc.closeOnce.Do(func() {
		if sc.DeliveryTracker != nil {
			sc.DeliveryTracker.Stop()
		}
		if sc.AuditLogger != nil {
			closeErr = sc.AuditLogger.Close(ctx)
		}
		sc.closed = true
	})
	return closeErr
}

// IsClosed returns true if the container has been closed
func (sc *ServiceContainer) IsClosed() bool {
	return sc.closed
}
