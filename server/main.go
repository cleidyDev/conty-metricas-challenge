package main

import (
	"log/slog"
	"net/http"
	"os"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	addr := "8080"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /test", http.NotFound)
	logger.Info("server starting", "addr", addr)

}
