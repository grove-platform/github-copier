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

	// New services
	ConfigLoader      ConfigLoader
	PatternMatcher    PatternMatcher
	PathTransformer   PathTransformer
	MessageTemplater  MessageTemplater
	PRTemplateFetcher PRTemplateFetcher
	AuditLogger       AuditLogger
	MetricsCollector  *MetricsCollector
	SlackNotifier     SlackNotifier

	// Server state
	StartTime time.Time

	// Shutdown state
	closeOnce sync.Once
	closed    bool
}

// NewServiceContainer creates and initializes all services
func NewServiceContainer(config *configs.Config) (*ServiceContainer, error) {
	// Initialize file state service
	fileStateService := NewFileStateService()

	// Initialize config loader based on configuration
	var configLoader ConfigLoader
	if config.UseMainConfig && config.MainConfigFile != "" {
		// Use main config loader for new format with workflow references (when USE_MAIN_CONFIG=true)
		configLoader = NewMainConfigLoader()
	} else {
		// Use default config loader for singular config file (when USE_MAIN_CONFIG=false)
		configLoader = NewConfigLoader()
	}

	patternMatcher := NewPatternMatcher()
	pathTransformer := NewPathTransformer()
	messageTemplater := NewMessageTemplater()
	prTemplateFetcher := NewPRTemplateFetcher()
	metricsCollector := NewMetricsCollector()

	// Initialize Slack notifier
	slackNotifier := NewSlackNotifier(
		config.SlackWebhookURL,
		config.SlackChannel,
		config.SlackUsername,
		config.SlackIconEmoji,
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
		ConfigLoader:      configLoader,
		PatternMatcher:    patternMatcher,
		PathTransformer:   pathTransformer,
		MessageTemplater:  messageTemplater,
		PRTemplateFetcher: prTemplateFetcher,
		AuditLogger:       auditLogger,
		MetricsCollector:  metricsCollector,
		SlackNotifier:     slackNotifier,
		StartTime:         time.Now(),
	}, nil
}

// Close cleans up resources. Safe to call multiple times.
func (sc *ServiceContainer) Close(ctx context.Context) error {
	var closeErr error
	sc.closeOnce.Do(func() {
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
