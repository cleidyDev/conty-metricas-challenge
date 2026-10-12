package syncer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"conty-challenge/internal/clock"
	"conty-challenge/internal/domain"
	"conty-challenge/internal/provider"
	"conty-challenge/internal/store"
)

type Config struct {
	BaseBackoff    time.Duration
	MaxBackoff     time.Duration
	MaxAttempts    int
	MaxRetryAfter  time.Duration
	AttemptTimeout time.Duration
}

func DefaultConfig() Config {
	return Config{
		BaseBackoff:    500 * time.Millisecond,
		MaxBackoff:     5 * time.Second,
		MaxAttempts:    4, // 1 inicial + 3 retries
		MaxRetryAfter:  30 * time.Second,
		AttemptTimeout: 2 * time.Second,
	}
}

type SyncService struct {
	provider provider.MetricsProvider
	store    *store.MemoryStore
	clock    clock.Clock
	config   Config
	logger   *slog.Logger
}

func NewSyncService(
	prov provider.MetricsProvider,
	st *store.MemoryStore,
	cl clock.Clock,
	cfg Config,
	logger *slog.Logger,
) *SyncService {
	return &SyncService{
		provider: prov,
		store:    st,
		clock:    cl,
		config:   cfg,
		logger:   logger,
	}
}

func (s *SyncService) Sync(ctx context.Context, conn domain.Connection, window domain.Window) (domain.SyncResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.SyncResult{}, err
	}
	// syncId único por execução, sem depender de pacote externo:
	// 16 bytes do crypto/rand em hexadecimal.
	syncID := newSyncID()
	startedAt := s.clock.Now()
	if window.From.After(window.To) {
		return domain.SyncResult{}, fmt.Errorf("invalid window: from must not be after to")
	}

	if s.logger != nil {
		s.logger.Info("sync started",
			"syncId", syncID,
			"platform", conn.Platform,
			"accountId", conn.AccountID,
			"window", window,
		)
	}

	summary := domain.SyncSummary{}
	var firstError error
	pagesProcessed := 0

	cursor := ""
	for {
		// Buscar página com retry
		page, err := s.fetchPageWithRetry(ctx, conn, window, cursor)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
				return domain.SyncResult{}, err
			}
			if firstError == nil {
				firstError = err
			}
			break
		}

		pagesProcessed++

		dedupedItems := s.dedupePage(page.Items)

		items := s.filterByWindow(dedupedItems, window)
		if len(items) != len(dedupedItems) {
			s.debug("items dropped by window filter",
				"received", len(dedupedItems),
				"kept", len(items),
				"window", window,
			)
		}

		for _, item := range items {
			key := domain.Key{
				Platform:  conn.Platform,
				AccountID: conn.AccountID,
				PostID:    item.Snapshot.PostID,
			}
			fetchedAt := s.clock.Now()
			result, conflict := s.store.Upsert(key, item.Snapshot, item.Metrics, fetchedAt)

			switch result {
			case domain.Inserted:
				summary.Inserted++
			case domain.Updated:
				summary.Updated++
			case domain.Unchanged:
				summary.Unchanged++
				if conflict {
					summary.Conflicts++
				}
			case domain.Stale:
				summary.Stale++
			}
		}

		// Verificar se há mais páginas
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}

	finishedAt := s.clock.Now()

	// Determinar status
	var status domain.SyncStatus
	var errorMsg string
	if pagesProcessed == 0 {
		status = domain.FAILED
		if firstError != nil {
			errorMsg = firstError.Error()
		} else {
			errorMsg = "no pages processed"
		}
	} else if firstError != nil {
		status = domain.PARTIAL
		errorMsg = firstError.Error()
	} else {
		status = domain.COMPLETED
	}

	result := domain.SyncResult{
		SyncID:     syncID,
		Status:     status,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		Summary:    summary,
		Error:      errorMsg,
	}

	if s.logger != nil {
		s.logger.Info("sync finished",
			"syncId", syncID,
			"status", status,
			"summary", summary,
			"pagesProcessed", pagesProcessed,
		)
	}

	return result, nil
}

func (s *SyncService) fetchPageWithRetry(ctx context.Context, conn domain.Connection, window domain.Window, cursor string) (domain.Page, error) {
	var lastError error

	for attempt := 0; attempt < s.config.MaxAttempts; attempt++ {
		// Criar contexto com timeout para esta tentativa
		attemptCtx, cancel := context.WithTimeout(ctx, s.config.AttemptTimeout)

		page, err := s.provider.FetchPosts(attemptCtx, conn, window, cursor)
		attemptErr := attemptCtx.Err()
		cancel()

		// Verificar se o contexto original foi cancelado pelo chamador
		if ctx.Err() != nil {
			return domain.Page{}, ctx.Err()
		}
		if attemptErr == context.DeadlineExceeded && err == nil {
			err = &domain.TransientError{Err: context.DeadlineExceeded}
		}
		if attemptErr == context.DeadlineExceeded && err == context.DeadlineExceeded {
			err = &domain.TransientError{Err: err}
		}

		if err == nil {
			// Sucesso
			return page, nil
		}

		lastError = err

		// Verificar tipo de erro
		lastAttempt := attempt == s.config.MaxAttempts-1

		var rle *domain.RateLimitError
		if errors.As(err, &rle) {
			// Erro 429 - tratar Retry-After
			switch {
			case rle.RetryAfter < 0:

				s.warn("invalid retry-after, treating as transient",
					"retryAfter", rle.RetryAfter,
				)
				err = &domain.TransientError{Err: err}
			case rle.RetryAfter > s.config.MaxRetryAfter:

				s.warn("retry-after above max, failing",
					"retryAfter", rle.RetryAfter,
					"maxRetryAfter", s.config.MaxRetryAfter,
				)
				return domain.Page{}, err
			case rle.RetryAfter == 0:

				s.debug("retry-after is 0, immediate retry")
				continue
			default:

				if lastAttempt {
					return domain.Page{}, err
				}
				s.debug("rate limited, waiting", "retryAfter", rle.RetryAfter)
				sleepErr := s.clock.Sleep(ctx, rle.RetryAfter)
				if sleepErr != nil {
					return domain.Page{}, sleepErr
				}
				continue
			}
		}

		if domain.IsTransientError(err) {
			if !lastAttempt {
				backoff := s.calculateBackoff(attempt)
				s.debug("transient error, backing off",
					"attempt", attempt,
					"backoff", backoff,
					"error", err,
				)
				sleepErr := s.clock.Sleep(ctx, backoff)
				if sleepErr != nil {
					return domain.Page{}, sleepErr
				}
				continue
			}
		}

		return domain.Page{}, err
	}

	return domain.Page{}, lastError
}

func (s *SyncService) debug(msg string, args ...any) {
	if s.logger != nil {
		s.logger.Debug(msg, args...)
	}
}

func (s *SyncService) warn(msg string, args ...any) {
	if s.logger != nil {
		s.logger.Warn(msg, args...)
	}
}

// newSyncID gera um identificador único por execução da sincronização.
// Usa crypto/rand para não depender de pacote externo nem de relógio.
func newSyncID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Extremamente improvável: em último caso cai para tempo + contador.
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

func (s *SyncService) calculateBackoff(attempt int) time.Duration {
	backoff := s.config.BaseBackoff * time.Duration(1<<uint(attempt))
	if backoff > s.config.MaxBackoff {
		backoff = s.config.MaxBackoff
	}
	return backoff
}

func (s *SyncService) dedupePage(items []domain.PostMetrics) []domain.PostMetrics {
	seen := make(map[string]domain.PostMetrics)

	for _, item := range items {
		postID := item.Snapshot.PostID
		existing, exists := seen[postID]
		if !exists || item.Snapshot.SnapshotAt.After(existing.Snapshot.SnapshotAt) {
			seen[postID] = item
		}
	}
	result := make([]domain.PostMetrics, 0, len(seen))
	for _, item := range seen {
		result = append(result, item)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Snapshot.PostID < result[j].Snapshot.PostID
	})
	return result
}

func (s *SyncService) filterByWindow(items []domain.PostMetrics, window domain.Window) []domain.PostMetrics {
	result := make([]domain.PostMetrics, 0, len(items))
	for _, item := range items {
		publishedAt := item.Snapshot.PublishedAt
		if publishedAt.Before(window.From) || publishedAt.After(window.To) {
			continue
		}
		result = append(result, item)
	}
	return result
}
