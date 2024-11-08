package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/unkn0wn-root/go-load-balancer/internal/config"
	"github.com/unkn0wn-root/go-load-balancer/internal/server"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	srv, err := server.NewServer(ctx, errChan, cfg)
	if err != nil {
		log.Fatalf("Failed to initialize server %v", err)
	}

	configWatcher, err := config.NewConfigWatcher(*configPath, func(newCfg *config.Config) {
		srv.UpdateConfig(newCfg)
	})
	if err != nil {
		log.Printf("Could not start config watcher: %v", err)
	}
	defer configWatcher.Close()

	// Handle shutdown gracefully
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := srv.Start(); err != nil {
			errChan <- err
		}
	}()

	select {
	case <-sigChan:
		log.Println("Shutdown signal received, starting graceful shutdown")
		cancel()
	case err := <-errChan:
		log.Printf("Server error triggered shutdown: %v", err)
	case <-ctx.Done():
		log.Println("Context cancelled")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil && err != context.Canceled {
		log.Printf("Error during shutdown: %v", err)
	} else {
		log.Println("Shutdown completed")
	}
}
