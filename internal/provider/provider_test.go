package provider

import (
	"context"
	"sync"
	"testing"
	"time"

	"conty-challenge/internal/domain"
)

var (
	jan1 = time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	jan2 = time.Date(2024, 1, 2, 12, 0, 0, 0, time.UTC)
	jan5 = time.Date(2024, 1, 5, 12, 0, 0, 0, time.UTC)
)

// ============================================================================
// HELPERS
// ============================================================================

var testConn = domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake-token"}

func testPost(postID string, publishedAt time.Time, likes int64) domain.PostMetrics {
	return domain.PostMetrics{
		Snapshot: domain.Snapshot{
			PostID:      postID,
			PublishedAt: publishedAt,
			SnapshotAt:  publishedAt,
			Likes:       likes,
			Views:       likes * 10,
			Comments:    likes / 10,
		},
		Metrics: domain.Metrics{Likes: likes, Views: likes * 10, Comments: likes / 10},
	}
}

func wideWindow() domain.Window {
	return domain.Window{From: jan1, To: jan5}
}

func fetch(t *testing.T, fp *FakeProvider, cursor string) domain.Page {
	t.Helper()
	page, err := fp.FetchPosts(context.Background(), testConn, wideWindow(), cursor)
	if err != nil {
		t.Fatalf("unexpected fetch error: %v", err)
	}
	return page
}

// ============================================================================
// A INTERFACE
// ============================================================================

func TestFakeProvider_SatisfiesInterface(t *testing.T) {
	// Compila apenas se FakeProvider implementa MetricsProvider: é isto que
	// permite trocar o provedor simulado por um real sem tocar na regra.
	var p MetricsProvider = NewFakeProvider()
	if _, err := p.FetchPosts(context.Background(), testConn, wideWindow(), ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ============================================================================
// CENÁRIO OK
// ============================================================================

func TestFetchPosts_Ok(t *testing.T) {
	fp := NewFakeProvider()
	fp.AddData("", []domain.PostMetrics{testPost("post1", jan1, 100)})
	fp.SetScenarios(ScenarioOK)

	page := fetch(t, fp, "")

	if len(page.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(page.Items))
	}
	if page.Items[0].Snapshot.PostID != "post1" {
		t.Fatalf("expected post1, got %s", page.Items[0].Snapshot.PostID)
	}
	if page.Items[0].Metrics.Likes != 100 {
		t.Fatalf("expected likes=100, got %d", page.Items[0].Metrics.Likes)
	}
	if page.HasMore {
		t.Fatalf("expected HasMore=false without a page 2, got %v (next=%q)", page.HasMore, page.NextCursor)
	}
}

func TestFetchPosts_SemDadosDevolvePaginaVazia(t *testing.T) {
	fp := NewFakeProvider()

	page := fetch(t, fp, "")

	if len(page.Items) != 0 {
		t.Fatalf("expected no items, got %d", len(page.Items))
	}
	if page.HasMore || page.NextCursor != "" {
		t.Fatalf("expected an empty terminal page, got HasMore=%v next=%q", page.HasMore, page.NextCursor)
	}
}

func TestFetchPosts_CursorDesconhecido(t *testing.T) {
	fp := NewFakeProvider()
	fp.AddData("", []domain.PostMetrics{testPost("post1", jan1, 100)})

	page := fetch(t, fp, "cursor-que-nao-existe")

	if len(page.Items) != 0 {
		t.Fatalf("expected no items, got %d", len(page.Items))
	}
}

func TestFetchPosts_SemCenarioFilaAssumeOk(t *testing.T) {
	// Sem roteiro configurado o comportamento é sempre "ok".
	fp := NewFakeProvider()
	fp.AddData("", []domain.PostMetrics{testPost("post1", jan1, 100)})

	for i := 0; i < 3; i++ {
		if page := fetch(t, fp, ""); len(page.Items) != 1 {
			t.Fatalf("call %d: expected 1 item, got %d", i, len(page.Items))
		}
	}
}

// ============================================================================
// FILTRO DA JANELA (publishedAt, inclusivo)
// ============================================================================

func TestFetchPosts_FiltraPorPublishedAt(t *testing.T) {
	cases := []struct {
		name   string
		window domain.Window
		want   []string // postIds esperados, por ordem
	}{
		{
			name:   "janela largo inclui tudo",
			window: domain.Window{From: jan1, To: jan5},
			want:   []string{"antes", "dentro", "depois"},
		},
		{
			name:   "limites sao inclusivos (jan1 e jan2 entram)",
			window: domain.Window{From: jan1, To: jan2},
			want:   []string{"antes", "dentro"},
		},
		{
			name:   "janela de um unico dia so inclui esse dia",
			window: domain.Window{From: jan2, To: jan2},
			want:   []string{"dentro"},
		},
		{
			name:   "janela antes de tudo",
			window: domain.Window{From: jan1.AddDate(0, 0, -30), To: jan1.AddDate(0, 0, -1)},
			want:   nil,
		},
		{
			name:   "janela depois de tudo",
			window: domain.Window{From: jan5.AddDate(0, 0, 1), To: jan5.AddDate(0, 0, 30)},
			want:   nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := NewFakeProvider()
			// Publicados em jan1, jan2 e jan5; a janela larga é [jan1, jan5].
			fp.AddData("", []domain.PostMetrics{
				testPost("antes", jan1, 10),
				testPost("dentro", jan2, 20),
				testPost("depois", jan5, 30),
			})

			page, err := fp.FetchPosts(context.Background(), testConn, tc.window, "")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(page.Items) != len(tc.want) {
				t.Fatalf("expected %d items (%v), got %d", len(tc.want), tc.want, len(page.Items))
			}
			for i, want := range tc.want {
				if page.Items[i].Snapshot.PostID != want {
					t.Fatalf("item %d: expected %s, got %s", i, want, page.Items[i].Snapshot.PostID)
				}
			}
		})
	}
}

// ============================================================================
// PAGINAÇÃO POR CURSOR
// ============================================================================

func TestFetchPosts_PaginacaoTresPaginas(t *testing.T) {
	fp := NewFakeProvider()
	fp.AddData("", []domain.PostMetrics{testPost("post1", jan1, 10), testPost("post2", jan1, 20)})
	fp.AddData("page2", []domain.PostMetrics{testPost("post3", jan1, 30)})
	fp.AddData("page3", []domain.PostMetrics{testPost("post4", jan1, 40)})

	page1 := fetch(t, fp, "")
	if len(page1.Items) != 2 || !page1.HasMore || page1.NextCursor != "page2" {
		t.Fatalf("unexpected page 1: items=%d hasMore=%v next=%q", len(page1.Items), page1.HasMore, page1.NextCursor)
	}

	page2 := fetch(t, fp, page1.NextCursor)
	if len(page2.Items) != 1 || !page2.HasMore || page2.NextCursor != "page3" {
		t.Fatalf("unexpected page 2: items=%d hasMore=%v next=%q", len(page2.Items), page2.HasMore, page2.NextCursor)
	}

	page3 := fetch(t, fp, page2.NextCursor)
	if len(page3.Items) != 1 || page3.HasMore {
		t.Fatalf("unexpected page 3: items=%d hasMore=%v next=%q", len(page3.Items), page3.HasMore, page3.NextCursor)
	}
}

func TestFetchPosts_PaginacaoParaQuandoNaoHaProximaPagina(t *testing.T) {
	fp := NewFakeProvider()
	fp.AddData("", []domain.PostMetrics{testPost("post1", jan1, 10)})
	fp.AddData("page2", []domain.PostMetrics{testPost("post2", jan1, 20)})
	// "page3" fica vazia de propósito.

	page1 := fetch(t, fp, "")
	if !page1.HasMore {
		t.Fatal("expected page 1 to announce a next page")
	}

	page2 := fetch(t, fp, "page2")
	if page2.HasMore {
		t.Fatalf("expected HasMore=false when page3 is empty, got %v (next=%q)", page2.HasMore, page2.NextCursor)
	}
}

func TestFetchPosts_HasMoreIgnoraFiltroDaJanela(t *testing.T) {
	// A página 2 existe, mas todos os posts caem fora da janela pedida.
	// O fake anuncia HasMore e a página chega vazia: a regra de negócio
	// trata isso como página válida sem itens.
	fp := NewFakeProvider()
	fp.AddData("", []domain.PostMetrics{testPost("post1", jan1, 10)})
	fp.AddData("page2", []domain.PostMetrics{testPost("post-fora", jan5.AddDate(0, 0, 10), 20)})

	narrow := domain.Window{From: jan1, To: jan2}
	page1, err := fp.FetchPosts(context.Background(), testConn, narrow, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !page1.HasMore {
		t.Fatal("expected HasMore=true because page2 has data")
	}

	page2, err := fp.FetchPosts(context.Background(), testConn, narrow, "page2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page2.Items) != 0 {
		t.Fatalf("expected the out-of-window post to be filtered out, got %d items", len(page2.Items))
	}
}

// ============================================================================
// CENÁRIOS DE FALHA
// ============================================================================

func TestFetchPosts_CenarioTimeout(t *testing.T) {
	fp := NewFakeProvider()
	fp.SetScenarios(ScenarioTimeout)

	page, err := fp.FetchPosts(context.Background(), testConn, wideWindow(), "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !domain.IsTransientError(err) {
		t.Fatalf("expected a TransientError, got %T: %v", err, err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("expected no items on error, got %d", len(page.Items))
	}
}

func TestFetchPosts_CenarioDuplicate(t *testing.T) {
	fp := NewFakeProvider()
	fp.AddData("", []domain.PostMetrics{testPost("post1", jan1, 100), testPost("post2", jan1, 200)})
	fp.SetScenarios(ScenarioDuplicate)

	page := fetch(t, fp, "")

	// O fake devolve 3 itens: o primeiro aparece duas vezes.
	if len(page.Items) != 3 {
		t.Fatalf("expected 3 items (first duplicated), got %d", len(page.Items))
	}
	if page.Items[0].Snapshot.PostID != page.Items[2].Snapshot.PostID {
		t.Fatalf("expected the first item to be duplicated, got %s and %s",
			page.Items[0].Snapshot.PostID, page.Items[2].Snapshot.PostID)
	}
}

func TestFetchPosts_CenarioDuplicateComPaginaVazia(t *testing.T) {
	fp := NewFakeProvider()
	fp.SetScenarios(ScenarioDuplicate)

	page := fetch(t, fp, "")

	if len(page.Items) != 0 {
		t.Fatalf("expected no items, got %d", len(page.Items))
	}
}

func TestFetchPosts_CenarioRateLimit(t *testing.T) {
	cases := []struct {
		name    string
		seconds int
		want    time.Duration
	}{
		{"sem Retry-After", 0, 0},
		{"Retry-After de 7s", 7, 7 * time.Second},
		{"Retry-After no teto", 30, 30 * time.Second},
		{"Retry-After acima do teto", 60, 60 * time.Second},
		{"Retry-After negativo (inválido)", -5, -5 * time.Second},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := NewFakeProvider()
			fp.SetScenarios(ScenarioRateLimit)
			fp.SetRateLimitSeconds(tc.seconds)

			_, err := fp.FetchPosts(context.Background(), testConn, wideWindow(), "")
			if err == nil {
				t.Fatal("expected an error")
			}

			var rle *domain.RateLimitError
			if !asRateLimit(err, &rle) {
				t.Fatalf("expected a RateLimitError, got %T: %v", err, err)
			}
			if rle.RetryAfter != tc.want {
				t.Fatalf("expected retryAfter=%v, got %v", tc.want, rle.RetryAfter)
			}
			// Um 429 não é uma falha transitória: a regra decide o que fazer.
			if domain.IsTransientError(err) {
				t.Fatal("expected a RateLimitError not to be treated as transient")
			}
		})
	}
}

func asRateLimit(err error, target **domain.RateLimitError) bool {
	rle, ok := err.(*domain.RateLimitError)
	if ok {
		*target = rle
	}
	return ok
}

func TestFetchPosts_CenarioDesconhecidoAssumeOk(t *testing.T) {
	fp := NewFakeProvider()
	fp.AddData("", []domain.PostMetrics{testPost("post1", jan1, 10)})
	fp.SetScenarios(Scenario("cena-que-nao-existe"))

	page := fetch(t, fp, "")

	if len(page.Items) != 0 {
		t.Fatalf("expected an empty page for an unknown scenario, got %d items", len(page.Items))
	}
}

// ============================================================================
// ROTEIRO (fila por chamada)
// ============================================================================

func TestFetchPosts_RoteiroConsumidoPorChamada(t *testing.T) {
	fp := NewFakeProvider()
	fp.AddData("", []domain.PostMetrics{testPost("post1", jan1, 10)})
	fp.SetScenarios(ScenarioRateLimit, ScenarioTimeout, ScenarioOK)
	fp.SetRateLimitSeconds(3)

	ctx := context.Background()

	// 1ª chamada: 429
	_, err := fp.FetchPosts(ctx, testConn, wideWindow(), "")
	if _, ok := err.(*domain.RateLimitError); !ok {
		t.Fatalf("call 1: expected RateLimitError, got %T: %v", err, err)
	}

	// 2ª chamada: timeout
	_, err = fp.FetchPosts(ctx, testConn, wideWindow(), "")
	if !domain.IsTransientError(err) {
		t.Fatalf("call 2: expected TransientError, got %T: %v", err, err)
	}

	// 3ª chamada: ok (a fila acabou)
	page, err := fp.FetchPosts(ctx, testConn, wideWindow(), "")
	if err != nil {
		t.Fatalf("call 3: unexpected error: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("call 3: expected 1 item, got %d", len(page.Items))
	}

	// 4ª chamada: continua ok (fila vazia = ok)
	page, err = fp.FetchPosts(ctx, testConn, wideWindow(), "")
	if err != nil {
		t.Fatalf("call 4: unexpected error: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("call 4: expected 1 item, got %d", len(page.Items))
	}
}

func TestSetScenarios_SubstituiOFilaAnterior(t *testing.T) {
	fp := NewFakeProvider()
	fp.SetScenarios(ScenarioTimeout, ScenarioTimeout)
	fp.SetScenarios(ScenarioOK)

	page, err := fp.FetchPosts(context.Background(), testConn, wideWindow(), "")
	if err != nil {
		t.Fatalf("expected the old queue to be replaced, got error: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("expected an empty page, got %d items", len(page.Items))
	}
}

func TestAddData_SubstituiOCursor(t *testing.T) {
	fp := NewFakeProvider()
	fp.AddData("", []domain.PostMetrics{testPost("post1", jan1, 10)})
	fp.AddData("", []domain.PostMetrics{testPost("post9", jan1, 90)})

	page := fetch(t, fp, "")

	if len(page.Items) != 1 || page.Items[0].Snapshot.PostID != "post9" {
		t.Fatalf("expected the second AddData to win, got %+v", page.Items)
	}
}

// ============================================================================
// CONTEXTO
// ============================================================================

func TestFetchPosts_ContextoCancelado(t *testing.T) {
	fp := NewFakeProvider()
	fp.AddData("", []domain.PostMetrics{testPost("post1", jan1, 10)})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := fp.FetchPosts(ctx, testConn, wideWindow(), "")
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestFetchPosts_ContextoExpirado(t *testing.T) {
	fp := NewFakeProvider()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := fp.FetchPosts(ctx, testConn, wideWindow(), "")
	if err != context.DeadlineExceeded {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
}

func TestFetchPosts_ContextoCanceladoNaoConsomeORoteiro(t *testing.T) {
	fp := NewFakeProvider()
	fp.AddData("", []domain.PostMetrics{testPost("post1", jan1, 10)})
	fp.SetScenarios(ScenarioTimeout)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = fp.FetchPosts(ctx, testConn, wideWindow(), "")

	// A chamada cancelada devolveu antes de tocar no roteiro: a próxima
	// chamada ainda é o timeout que estava agendado.
	_, err := fp.FetchPosts(context.Background(), testConn, wideWindow(), "")
	if !domain.IsTransientError(err) {
		t.Fatalf("expected the queued ScenarioTimeout to be next, got %T: %v", err, err)
	}
}

// ============================================================================
// RESET
// ============================================================================

func TestReset(t *testing.T) {
	fp := NewFakeProvider()
	fp.AddData("", []domain.PostMetrics{testPost("post1", jan1, 10)})
	fp.SetScenarios(ScenarioTimeout)
	fp.SetRateLimitSeconds(10)

	fp.Reset()

	if page := fetch(t, fp, ""); len(page.Items) != 0 {
		t.Fatalf("expected the data to be cleared, got %d items", len(page.Items))
	}
	fp.SetScenarios(ScenarioRateLimit)
	_, err := fp.FetchPosts(context.Background(), testConn, wideWindow(), "")
	var rle *domain.RateLimitError
	if !asRateLimit(err, &rle) {
		t.Fatalf("expected a RateLimitError, got %T: %v", err, err)
	}
	if rle.RetryAfter != 0 {
		t.Fatalf("expected retryAfter to be reset to 0, got %v", rle.RetryAfter)
	}
}

// ============================================================================
// CONCORRÊNCIA (corre com -race)
// ============================================================================

func TestFakeProvider_Concorrente(t *testing.T) {
	fp := NewFakeProvider()
	fp.AddData("", []domain.PostMetrics{testPost("post1", jan1, 10)})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := fp.FetchPosts(context.Background(), testConn, wideWindow(), ""); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
}
