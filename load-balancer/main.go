package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

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

	srv, err := server.New(ctx, cfg)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	configWatcher, err := config.NewConfigWatcher(*configPath, func(newCfg *config.Config) {
		if err := srv.UpdateConfig(newCfg); err != nil {
			log.Printf("Failed to update config: %v", err)
		}
	})
	if err != nil {
		log.Printf("Failed to start config watcher: %v", err)
	}
	defer configWatcher.Close()

	// Handle shutdown gracefully
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := srv.Start(); err != nil {
			log.Printf("Server error: %v", err)
			cancel()
		}
	}()

	select {
	case <-sigChan:
		log.Println("Shutdown signal received")
	case <-ctx.Done():
		log.Println("Context cancelled")
	}

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Error during shutdown: %v", err)
	}
}
