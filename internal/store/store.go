package store

import (
	"conty-challenge/internal/domain"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Row representa uma linha armazenada para um post
type Row struct {
	Snapshot      domain.Snapshot
	Metrics       domain.Metrics
	FetchedAt     time.Time // quando o valor atual foi obtido (muda só em inserted/updated)
	LastCheckedAt time.Time // última vez que o serviço verificou o post (atualizado também em unchanged
}

type MemoryStore struct {
	mu     sync.RWMutex
	rows   map[domain.Key]Row
	logger *slog.Logger
}

func NewMemoryStore(logger *slog.Logger) *MemoryStore {
	return &MemoryStore{
		rows:   make(map[domain.Key]Row),
		logger: logger,
	}
}

func (s *MemoryStore) Upsert(key domain.Key, snapshot domain.Snapshot, metrics domain.Metrics, fetchedAt time.Time) (domain.UpsertResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, exists := s.rows[key]

	switch {
	case !exists:
		// Nova linha - inserir
		s.rows[key] = Row{
			Snapshot:      snapshot,
			Metrics:       metrics,
			FetchedAt:     fetchedAt,
			LastCheckedAt: fetchedAt,
		}
		return domain.Inserted, false

	case snapshot.SnapshotAt.Before(cur.Snapshot.SnapshotAt):
		// Snapshot mais antigo - ignorar para não regredir
		s.rows[key] = Row{
			Snapshot:      cur.Snapshot,
			Metrics:       cur.Metrics,
			FetchedAt:     cur.FetchedAt,
			LastCheckedAt: fetchedAt,
		}
		return domain.Stale, false

	case snapshot.SnapshotAt.Equal(cur.Snapshot.SnapshotAt):
		// Mesmo snapshotAt
		if metrics.Likes == cur.Metrics.Likes &&
			metrics.Views == cur.Metrics.Views &&
			metrics.Comments == cur.Metrics.Comments {

			s.rows[key] = Row{
				Snapshot:      cur.Snapshot,
				Metrics:       cur.Metrics,
				FetchedAt:     cur.FetchedAt, // mantém o fetchedAt original
				LastCheckedAt: fetchedAt,     // atualiza LastCheckedAt
			}
			return domain.Unchanged, false
		}

		if s.logger != nil {
			s.logger.Warn("conflict: same snapshotAt but different metrics",
				"key", key,
				"snapshotAt", snapshot.SnapshotAt,
				"old_metrics", cur.Metrics,
				"new_metrics", metrics,
			)
		}
		s.rows[key] = Row{
			Snapshot:      cur.Snapshot,
			Metrics:       cur.Metrics,
			FetchedAt:     cur.FetchedAt,
			LastCheckedAt: fetchedAt,
		}
		return domain.Unchanged, true // conflito

	default:

		s.rows[key] = Row{
			Snapshot:      snapshot,
			Metrics:       metrics,
			FetchedAt:     fetchedAt,
			LastCheckedAt: fetchedAt,
		}
		return domain.Updated, false
	}
}

func (s *MemoryStore) GetTotals(platform string, accountID string) domain.Totals {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var totals domain.Totals
	for key, row := range s.rows {
		if key.Platform == platform && key.AccountID == accountID {
			totals.Likes += row.Metrics.Likes
			totals.Views += row.Metrics.Views
			totals.Comments += row.Metrics.Comments
			totals.Count++
		}
	}
	return totals
}

func (s *MemoryStore) GetPosts(platform string, accountID string) []domain.PostView {
	s.mu.RLock()
	defer s.mu.RUnlock()

	posts := make([]domain.PostView, 0)
	for key, row := range s.rows {
		if key.Platform == platform && key.AccountID == accountID {
			posts = append(posts, domain.PostView{
				Snapshot:      row.Snapshot,
				Metrics:       row.Metrics,
				FetchedAt:     row.FetchedAt,
				LastCheckedAt: row.LastCheckedAt,
			})
		}
	}

	sort.Slice(posts, func(i, j int) bool {
		if !posts[i].Snapshot.PublishedAt.Equal(posts[j].Snapshot.PublishedAt) {
			return posts[i].Snapshot.PublishedAt.Before(posts[j].Snapshot.PublishedAt)
		}
		return posts[i].Snapshot.PostID < posts[j].Snapshot.PostID
	})

	return posts
}

func (s *MemoryStore) GetRow(key domain.Key) (Row, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	row, exists := s.rows[key]
	return row, exists
}

func (s *MemoryStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.rows)
}

func (s *MemoryStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = make(map[domain.Key]Row)
}
