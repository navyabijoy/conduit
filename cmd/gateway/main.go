package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"conduit/gateway"
	"conduit/mock"
)

func main() {
	// ── Flags ────────────────────────────────────────────────────────────────
	addr := flag.String("addr", ":8080", "Gateway listen address")
	dbPath := flag.String("db", "./conduit.db", "SQLite database path")
	mockAddr := flag.String("mock-addr", ":8081", "Mock provider listen address (empty = use real providers)")
	webhookSecret := flag.String("webhook-secret", getenv("CONDUIT_WEBHOOK_SECRET", "mock_webhook_secret_value"), "Shared HMAC webhook secret")
	useMock := flag.Bool("mock", true, "Start built-in mock provider server")
	flag.Parse()

	// ── Mock Provider ────────────────────────────────────────────────────────
	mockHost := ""
	if *useMock {
		log.Printf("[Main] Starting mock provider on %s", *mockAddr)
		mockSrv := mock.NewMockServer(*mockAddr, *webhookSecret)
		mockSrv.Start()
		// Give it a moment to bind
		time.Sleep(50 * time.Millisecond)
		mockHost = "localhost" + *mockAddr
	}

	// ── Database ─────────────────────────────────────────────────────────────
	db, err := gateway.NewDB(*dbPath)
	if err != nil {
		log.Fatalf("[Main] Failed to open database: %v", err)
	}
	defer db.Close()

	// ── Gateway ───────────────────────────────────────────────────────────────
	gw := gateway.NewGateway(db, mockHost, *webhookSecret)
	gw.Start(*addr)

	log.Printf("[Main] Conduit gateway running on %s", *addr)
	if *useMock {
		log.Printf("[Main] Mock provider running on %s", *mockAddr)
	}
	log.Println("[Main] Press Ctrl+C to stop.")

	// ── Graceful Shutdown ─────────────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("[Main] Shutting down...")
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
