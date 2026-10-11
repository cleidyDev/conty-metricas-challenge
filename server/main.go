package main

import (
	"conty-challenge/internal/clock"
	"log/slog"
	"net/http"
	"os"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	addr := "8080"
	clk := clock.NewSystemClock()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /test", http.NotFound)
	logger.Info("server starting", "addr", addr)

}
