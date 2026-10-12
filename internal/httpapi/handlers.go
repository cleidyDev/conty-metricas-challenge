package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"conty-challenge/internal/domain"
	"conty-challenge/internal/store"
	"conty-challenge/internal/syncer"
)

type Handler struct {
	syncer *syncer.SyncService
	store  *store.MemoryStore
	logger *slog.Logger
}

func NewHandler(sync *syncer.SyncService, st *store.MemoryStore, logger *slog.Logger) *Handler {
	return &Handler{
		syncer: sync,
		store:  st,
		logger: logger,
	}
}

func (h *Handler) SyncHandler(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil {
		h.logError("empty request body")
		http.Error(w, "invalid json: empty body", http.StatusBadRequest)
		return
	}
	var req domain.SyncRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&req); err != nil {
		h.logError("invalid json", "error", err)
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "invalid json: expected a single object", http.StatusBadRequest)
		return
	}

	if req.From.IsZero() || req.To.IsZero() {
		h.logError("invalid window: from and to are required")
		http.Error(w, "invalid window: from and to are required", http.StatusBadRequest)
		return
	}

	if req.From.After(req.To) {
		h.logError("invalid window: from after to")
		http.Error(w, "invalid window: from must be before to", http.StatusBadRequest)
		return
	}

	if req.Platform == "" {
		h.logError("platform is required")
		http.Error(w, "platform is required", http.StatusBadRequest)
		return
	}

	if req.AccountID == "" {
		h.logError("accountId is required")
		http.Error(w, "accountId is required", http.StatusBadRequest)
		return
	}

	conn := domain.Connection{
		Platform:  req.Platform,
		AccountID: req.AccountID,
		Token:     "fake-token", // token fictício para testes
	}

	window := domain.Window{
		From: req.From,
		To:   req.To,
	}

	result, err := h.syncer.Sync(r.Context(), conn, window)
	if err != nil {
		h.logError("sync error", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var statusCode int
	switch result.Status {
	case domain.COMPLETED:
		statusCode = http.StatusOK
	case domain.PARTIAL, domain.FAILED:
		statusCode = http.StatusBadGateway
	default:
		statusCode = http.StatusInternalServerError
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(result)
}

func (h *Handler) MetricsHandler(w http.ResponseWriter, r *http.Request) {
	platform := r.PathValue("platform")
	accountID := r.PathValue("accountId")

	if platform == "" {
		http.Error(w, "platform is required", http.StatusBadRequest)
		return
	}
	if accountID == "" {
		http.Error(w, "accountId is required", http.StatusBadRequest)
		return
	}

	posts := h.store.GetPosts(platform, accountID)
	totals := h.store.GetTotals(platform, accountID)

	response := domain.MetricsResponse{
		Platform:  platform,
		AccountID: accountID,
		Posts:     posts,
		Totals:    totals,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

func (h *Handler) logError(msg string, args ...any) {
	if h.logger != nil {
		h.logger.Error(msg, args...)
	}
}
