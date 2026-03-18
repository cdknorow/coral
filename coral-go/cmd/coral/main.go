// Command coral starts the Coral dashboard web server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/server"
	"github.com/cdknorow/coral/internal/store"
)

func main() {
	cfg := config.Load()

	// CLI flags override config
	host := flag.String("host", cfg.Host, "Host to bind to")
	port := flag.Int("port", cfg.Port, "Port to bind to")
	noBrowser := flag.Bool("no-browser", false, "Don't open the browser on startup")
	flag.Parse()

	cfg.Host = *host
	cfg.Port = *port

	// Open database
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Build and start the HTTP server
	srv := server.New(cfg, db)
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)

	httpServer := &http.Server{
		Addr:         addr,
		Handler:      srv.Router(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // Disabled for WebSocket/SSE
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown on SIGINT/SIGTERM
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("Coral dashboard: http://localhost:%d", cfg.Port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Open browser unless --no-browser
	if !*noBrowser {
		go func() {
			time.Sleep(500 * time.Millisecond)
			openBrowser(fmt.Sprintf("http://localhost:%d", cfg.Port))
		}()
	}

	<-ctx.Done()
	log.Println("Shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}
}

func openBrowser(url string) {
	// macOS
	cmd := "open"
	args := []string{url}

	// Best-effort; ignore errors
	proc := &os.ProcAttr{Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}}
	p, err := os.StartProcess("/usr/bin/"+cmd, append([]string{cmd}, args...), proc)
	if err == nil {
		p.Release()
	}
}
