package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/grove-platform/github-copier/configs"
	"github.com/grove-platform/github-copier/services"
)

func main() {
	// Parse command line flags
	var envFile string
	var dryRun bool
	var validateOnly bool

	flag.StringVar(&envFile, "env", "./configs/.env", "Path to environment file")
	flag.BoolVar(&dryRun, "dry-run", false, "Enable dry-run mode (no actual changes)")
	flag.BoolVar(&validateOnly, "validate", false, "Validate configuration and exit")
	help := flag.Bool("help", false, "Show help")
	flag.Parse()

	if *help {
		printHelp()
		return
	}

	// Load environment configuration
	config, err := configs.LoadEnvironment(envFile)
	if err != nil {
		fmt.Printf("❌ Error loading environment: %v\n", err)
		os.Exit(1)
	}

	// Load secrets from Secret Manager if not directly provided
	ctx := context.Background()
	if err := services.LoadWebhookSecret(ctx, config); err != nil {
		fmt.Printf("❌ Error loading webhook secret: %v\n", err)
		os.Exit(1)
	}

	if err := services.LoadMongoURI(ctx, config); err != nil {
		fmt.Printf("❌ Error loading MongoDB URI: %v\n", err)
		os.Exit(1)
	}

	// Override dry-run from command line
	if dryRun {
		config.DryRun = true
	}

	// Initialize services
	container, err := services.NewServiceContainer(config)
	if err != nil {
		fmt.Printf("❌ Failed to initialize services: %v\n", err)
		os.Exit(1)
	}
	defer container.Close(context.Background())

	// If validate-only mode, validate config and exit
	if validateOnly {
		if err := validateConfiguration(container); err != nil {
			fmt.Printf("❌ Configuration validation failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("✅ Configuration is valid")
		return
	}

	// Initialize Google Cloud logging
	services.InitializeLogger(config)
	defer services.CloseGoogleLogger()

	// Configure GitHub permissions
	if err := services.ConfigurePermissions(ctx, config); err != nil {
		fmt.Printf("❌ Failed to configure GitHub permissions: %v\n", err)
		os.Exit(1)
	}

	// Print startup banner
	printBanner(config, container)

	// Start web server
	if err := startWebServer(config, container); err != nil {
		fmt.Printf("❌ Failed to start web server: %v\n", err)
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Println("GitHub Code Example Copier")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  app [options]")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  -env string       Path to environment file (default: ./configs/.env)")
	fmt.Println("  -dry-run          Enable dry-run mode (no actual changes)")
	fmt.Println("  -validate         Validate configuration and exit")
	fmt.Println("  -help             Show this help message")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  app -env ./configs/.env.test")
	fmt.Println("  app -dry-run")
	fmt.Println("  app -validate -env ./configs/.env.prod")
}

func printBanner(config *configs.Config, container *services.ServiceContainer) {
	fmt.Println("╔════════════════════════════════════════════════════════════════╗")
	fmt.Println("║  GitHub Code Example Copier                                    ║")
	fmt.Println("╠════════════════════════════════════════════════════════════════╣")
	fmt.Printf("║  Port:         %-48s║\n", config.Port)
	fmt.Printf("║  Webhook Path: %-48s║\n", config.WebserverPath)
	fmt.Printf("║  Config File:  %-48s║\n", config.ConfigFile)
	fmt.Printf("║  Dry Run:      %-48v║\n", config.DryRun)
	fmt.Printf("║  Audit Log:    %-48v║\n", config.AuditEnabled)
	fmt.Printf("║  Metrics:      %-48v║\n", config.MetricsEnabled)
	fmt.Println("╚════════════════════════════════════════════════════════════════╝")
	fmt.Println()
}

func validateConfiguration(container *services.ServiceContainer) error {
	ctx := context.Background()
	_, err := container.ConfigLoader.LoadConfig(ctx, container.Config)
	return err
}

func startWebServer(config *configs.Config, container *services.ServiceContainer) error {
	// Create HTTP handler with all routes
	mux := http.NewServeMux()

	// Webhook endpoint
	mux.HandleFunc(config.WebserverPath, func(w http.ResponseWriter, r *http.Request) {
		handleWebhook(w, r, config, container)
	})

	// Liveness probe — lightweight, always 200 if process is running
	mux.HandleFunc("/health", services.HealthHandler(container.StartTime))

	// Readiness probe — checks GitHub auth, MongoDB connectivity
	mux.HandleFunc("/ready", services.ReadinessHandler(container))

	// Metrics endpoint (if enabled)
	if config.MetricsEnabled {
		mux.HandleFunc("/metrics", services.MetricsHandler(container.MetricsCollector, container.FileStateService))
	}

	// Info endpoint
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "GitHub Code Example Copier\n")
		fmt.Fprintf(w, "Webhook endpoint: %s\n", config.WebserverPath)
		fmt.Fprintf(w, "Health check: /health\n")   //nolint:errcheck
		fmt.Fprintf(w, "Readiness check: /ready\n") //nolint:errcheck
		if config.MetricsEnabled {
			fmt.Fprintf(w, "Metrics: /metrics\n") //nolint:errcheck
		}
	})

	// Create server
	port := ":" + config.Port
	server := &http.Server{
		Addr:         port,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Channel to signal server errors
	serverErr := make(chan error, 1)

	// Start server in goroutine
	go func() {
		services.LogInfo("Starting web server", "port", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- fmt.Errorf("server error: %w", err)
		}
		close(serverErr)
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Block until we receive a signal or server error
	select {
	case err := <-serverErr:
		if err != nil {
			return err
		}
	case sig := <-sigChan:
		services.LogInfo("Received signal, initiating graceful shutdown", "signal", sig)
	}

	// Graceful shutdown with timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	services.LogInfo("Waiting for in-flight requests to complete")
	if err := server.Shutdown(shutdownCtx); err != nil {
		services.LogError("Server shutdown error", "error", err)
	} else {
		services.LogInfo("Server stopped accepting new connections")
	}

	// Cleanup resources (flush audit logs, close connections)
	services.LogInfo("Cleaning up resources")
	if err := container.Close(shutdownCtx); err != nil {
		services.LogError("Cleanup error", "error", err)
	}

	services.LogInfo("Shutdown complete")
	return nil
}

func handleWebhook(w http.ResponseWriter, r *http.Request, config *configs.Config, container *services.ServiceContainer) {
	// Record webhook received
	container.MetricsCollector.RecordWebhookReceived()
	startTime := time.Now()

	// Create context with timeout
	baseCtx, rid := services.WithRequestID(r)
	timeout := time.Duration(60) * time.Second
	ctx, cancel := context.WithTimeout(baseCtx, timeout)
	defer cancel()

	r = r.WithContext(ctx)

	services.LogWebhookOperation(ctx, "receive", "webhook received", nil, map[string]interface{}{
		"request_id": rid,
	})

	// Handle webhook with new pattern matching
	services.HandleWebhookWithContainer(w, r, config, container)

	// Record processing time
	container.MetricsCollector.RecordWebhookProcessed(time.Since(startTime))
}
