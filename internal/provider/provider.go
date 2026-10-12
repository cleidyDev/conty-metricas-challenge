package provider

import (
	"context"
	"conty-challenge/internal/domain"
	"sync"
	"time"
)

type MetricsProvider interface {
	FetchPosts(ctx context.Context, conn domain.Connection, window domain.Window, cursor string) (domain.Page, error)
}

// Scenario define o comportamento simulado de uma chamada ao provider
type Scenario string

const (
	ScenarioOK Scenario = "ok"

	ScenarioTimeout Scenario = "timeout"

	ScenarioDuplicate Scenario = "duplicate"

	ScenarioRateLimit Scenario = "rateLimit"
)

type FakeProvider struct {
	mu           sync.Mutex
	scenarios    []Scenario // fila de cenários por chamada
	rateLimitSec int
	data         map[string][]domain.PostMetrics
}

func NewFakeProvider() *FakeProvider {
	return &FakeProvider{
		scenarios: make([]Scenario, 0),
		data:      make(map[string][]domain.PostMetrics),
	}
}

func (fp *FakeProvider) SetScenarios(scenarios ...Scenario) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.scenarios = scenarios
}

func (fp *FakeProvider) SetRateLimitSeconds(seconds int) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.rateLimitSec = seconds
}

func (fp *FakeProvider) AddData(cursor string, posts []domain.PostMetrics) {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.data[cursor] = posts
}

func (fp *FakeProvider) FetchPosts(ctx context.Context, conn domain.Connection, window domain.Window, cursor string) (domain.Page, error) {
	fp.mu.Lock()
	defer fp.mu.Unlock()

	// Verificar se contexto foi cancelado
	select {
	case <-ctx.Done():
		return domain.Page{}, ctx.Err()
	default:
	}

	// Pegar o próximo cenário da fila
	scenario := ScenarioOK
	if len(fp.scenarios) > 0 {
		scenario = fp.scenarios[0]
		fp.scenarios = fp.scenarios[1:]
	}

	switch scenario {
	case ScenarioOK:
		return fp.fetchData(cursor, window), nil
	case ScenarioTimeout:
		return domain.Page{}, &domain.TransientError{Err: context.DeadlineExceeded}
	case ScenarioDuplicate:
		// Retorna dados com duplicados
		posts := fp.fetchData(cursor, window)
		if len(posts.Items) > 0 {
			// Duplica o primeiro item
			posts.Items = append(posts.Items, posts.Items[0])
		}
		return posts, nil
	case ScenarioRateLimit:
		retryAfter := time.Duration(fp.rateLimitSec) * time.Second
		return domain.Page{}, &domain.RateLimitError{RetryAfter: retryAfter}
	default:
		return domain.Page{}, nil
	}
}

func (fp *FakeProvider) fetchData(cursor string, window domain.Window) domain.Page {
	posts, exists := fp.data[cursor]
	if !exists {
		return domain.Page{
			Items:      []domain.PostMetrics{},
			NextCursor: "",
			HasMore:    false,
		}
	}

	filtered := make([]domain.PostMetrics, 0, len(posts))
	for _, post := range posts {
		publishedAt := post.Snapshot.PublishedAt
		if !publishedAt.Before(window.From) && !publishedAt.After(window.To) {
			filtered = append(filtered, post)
		}
	}

	nextCursor := ""
	hasMore := false
	if cursor == "" {
		nextCursor = "page2"
		hasMore = len(fp.data["page2"]) > 0
	} else if cursor == "page2" {
		nextCursor = "page3"
		hasMore = len(fp.data["page3"]) > 0
	}

	return domain.Page{
		Items:      filtered,
		NextCursor: nextCursor,
		HasMore:    hasMore,
	}
}

func (fp *FakeProvider) Reset() {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.scenarios = nil
	fp.data = make(map[string][]domain.PostMetrics)
	fp.rateLimitSec = 0
}
