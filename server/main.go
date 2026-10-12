package main

import (
	"conty-challenge/internal/clock"
	"conty-challenge/internal/httpapi"
	"conty-challenge/internal/provider"
	"conty-challenge/internal/store"
	"conty-challenge/internal/syncer"
	"log/slog"
	"net/http"
	"os"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	addr := ":8081"

	clk := clock.NewSystemClock()
	stores := store.NewMemoryStore(logger)
	providers := provider.NewFakeProvider()
	config := syncer.DefaultConfig()

	syncSv := syncer.NewSyncService(providers, stores, clk, config, logger)
	handler := httpapi.NewHandler(syncSv, stores, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sync", handler.SyncHandler)
	mux.HandleFunc("GET /metrics/{platform}/{accountId}", handler.MetricsHandler)

	logger.Info("server starting on", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		logger.Error("Server error", "error", err)
		os.Exit(1)
	}

}
