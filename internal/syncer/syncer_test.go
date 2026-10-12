package syncer

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"conty-challenge/internal/clock"
	"conty-challenge/internal/domain"
	"conty-challenge/internal/provider"
	"conty-challenge/internal/store"
)

// ============================================================================
// HELPER FUNCTIONS
// ============================================================================

func setupTest() (*SyncService, *provider.FakeProvider, *store.MemoryStore, *clock.FakeClock) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	fakeClock := clock.NewFakeClock(time.Now().UTC())
	fakeProvider := provider.NewFakeProvider()
	memoryStore := store.NewMemoryStore(logger)
	config := DefaultConfig()

	syncSvc := NewSyncService(fakeProvider, memoryStore, fakeClock, config, logger)

	return syncSvc, fakeProvider, memoryStore, fakeClock
}

type cancelableSleepClock struct{ entered chan struct{} }

func (c cancelableSleepClock) Now() time.Time { return time.Now().UTC() }
func (c cancelableSleepClock) Sleep(ctx context.Context, _ time.Duration) error {
	close(c.entered)
	<-ctx.Done()
	return ctx.Err()
}

func makePost(postID string, publishedAt, snapshotAt time.Time, likes, views, comments int64) domain.PostMetrics {
	return domain.PostMetrics{
		Snapshot: domain.Snapshot{
			PostID:      postID,
			PublishedAt: publishedAt,
			SnapshotAt:  snapshotAt,
			Likes:       likes,
			Views:       views,
			Comments:    comments,
		},
		Metrics: domain.Metrics{
			Likes:    likes,
			Views:    views,
			Comments: comments,
		},
	}
}

// ============================================================================
// TEST 1: Janelas sobrepostas não duplicam
// ============================================================================

func TestOverlappingWindows_NoDuplication(t *testing.T) {
	syncSvc, fakeProvider, memoryStore, _ := setupTest()
	ctx := context.Background()

	baseTime := time.Now().UTC()

	// Post 1: publicado em 2024-01-02
	post1 := makePost("post1", baseTime.AddDate(0, 0, 2), baseTime.AddDate(0, 0, 2), 100, 1000, 10)
	fakeProvider.AddData("", []domain.PostMetrics{post1})

	// Primeira sync: janela 2024-01-01 a 2024-01-03
	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window1 := domain.Window{From: baseTime.AddDate(0, 0, 1), To: baseTime.AddDate(0, 0, 3)}
	fakeProvider.SetScenarios(provider.ScenarioOK)

	result1, _ := syncSvc.Sync(ctx, conn, window1)
	if result1.Status != domain.COMPLETED {
		t.Fatalf("expected COMPLETED, got %s", result1.Status)
	}
	if result1.Summary.Inserted != 1 {
		t.Fatalf("expected 1 inserted, got %d", result1.Summary.Inserted)
	}

	// Segunda sync: janela sobreposta 2024-01-02 a 2024-01-05
	// Mesmo post com mesmo snapshotAt
	fakeProvider.AddData("", []domain.PostMetrics{post1})
	fakeProvider.SetScenarios(provider.ScenarioOK)
	window2 := domain.Window{From: baseTime.AddDate(0, 0, 2), To: baseTime.AddDate(0, 0, 5)}

	result2, _ := syncSvc.Sync(ctx, conn, window2)
	if result2.Status != domain.COMPLETED {
		t.Fatalf("expected COMPLETED, got %s", result2.Status)
	}
	// Deve ser unchanged, não inserted
	if result2.Summary.Unchanged != 1 {
		t.Fatalf("expected 1 unchanged, got %d", result2.Summary.Unchanged)
	}
	if result2.Summary.Inserted != 0 {
		t.Fatalf("expected 0 inserted (no duplication), got %d", result2.Summary.Inserted)
	}

	// Verificar que só há 1 linha no store
	if memoryStore.Count() != 1 {
		t.Fatalf("expected 1 row in store, got %d", memoryStore.Count())
	}
}

// ============================================================================
// TEST 2: Repetir sync é no-op
// ============================================================================

func TestRepeatSync_NoOp(t *testing.T) {
	syncSvc, fakeProvider, _, _ := setupTest()
	ctx := context.Background()

	baseTime := time.Now().UTC()
	post1 := makePost("post1", baseTime, baseTime, 100, 1000, 10)
	fakeProvider.AddData("", []domain.PostMetrics{post1})

	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}

	// Primeira sync
	fakeProvider.SetScenarios(provider.ScenarioOK)
	result1, _ := syncSvc.Sync(ctx, conn, window)
	if result1.Summary.Inserted != 1 {
		t.Fatalf("expected 1 inserted, got %d", result1.Summary.Inserted)
	}

	// Segunda sync idêntica
	fakeProvider.AddData("", []domain.PostMetrics{post1})
	fakeProvider.SetScenarios(provider.ScenarioOK)
	result2, _ := syncSvc.Sync(ctx, conn, window)
	if result2.Summary.Unchanged != 1 {
		t.Fatalf("expected 1 unchanged, got %d", result2.Summary.Unchanged)
	}
	if result2.Summary.Inserted != 0 {
		t.Fatalf("expected 0 inserted, got %d", result2.Summary.Inserted)
	}
}

// ============================================================================
// TEST 3: Item duplicado na mesma resposta conta uma vez
// ============================================================================

func TestDuplicateInSameResponse_CountsOnce(t *testing.T) {
	syncSvc, fakeProvider, memoryStore, _ := setupTest()
	ctx := context.Background()

	baseTime := time.Now().UTC()
	post1 := makePost("post1", baseTime, baseTime, 100, 1000, 10)

	// Adicionar dados
	fakeProvider.AddData("", []domain.PostMetrics{post1})

	// Configurar cenário duplicate (retorna o mesmo item 2x)
	fakeProvider.SetScenarios(provider.ScenarioDuplicate)

	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}

	result, _ := syncSvc.Sync(ctx, conn, window)
	if result.Status != domain.COMPLETED {
		t.Fatalf("expected COMPLETED, got %s", result.Status)
	}
	// Deve contar apenas 1 inserted, não 2
	if result.Summary.Inserted != 1 {
		t.Fatalf("expected 1 inserted (deduped), got %d", result.Summary.Inserted)
	}
	if memoryStore.Count() != 1 {
		t.Fatalf("expected 1 row in store, got %d", memoryStore.Count())
	}
}

// ============================================================================
// TEST 4: Snapshot mais novo atualiza, mais antigo ignorado
// ============================================================================

func TestSnapshot_NewerUpdates_OlderIgnored(t *testing.T) {
	syncSvc, fakeProvider, memoryStore, _ := setupTest()
	ctx := context.Background()

	baseTime := time.Now().UTC()

	// Primeiro sync com snapshot antigo
	postOld := makePost("post1", baseTime, baseTime, 100, 1000, 10)
	fakeProvider.AddData("", []domain.PostMetrics{postOld})
	fakeProvider.SetScenarios(provider.ScenarioOK)

	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}

	result1, _ := syncSvc.Sync(ctx, conn, window)
	if result1.Summary.Inserted != 1 {
		t.Fatalf("expected 1 inserted, got %d", result1.Summary.Inserted)
	}

	// Segundo sync com snapshot mais novo
	postNew := makePost("post1", baseTime, baseTime.Add(time.Hour), 200, 2000, 20)
	fakeProvider.AddData("", []domain.PostMetrics{postNew})
	fakeProvider.SetScenarios(provider.ScenarioOK)

	result2, _ := syncSvc.Sync(ctx, conn, window)
	if result2.Summary.Updated != 1 {
		t.Fatalf("expected 1 updated, got %d", result2.Summary.Updated)
	}

	// Terceiro sync com snapshot mais antigo (deve ser ignorado)
	fakeProvider.AddData("", []domain.PostMetrics{postOld})
	fakeProvider.SetScenarios(provider.ScenarioOK)

	result3, _ := syncSvc.Sync(ctx, conn, window)
	if result3.Summary.Stale != 1 {
		t.Fatalf("expected 1 stale, got %d", result3.Summary.Stale)
	}

	// Verificar que o store tem o valor mais novo
	key := domain.Key{Platform: "instagram", AccountID: "ada", PostID: "post1"}
	row, _ := memoryStore.GetRow(key)
	if row.Metrics.Likes != 200 {
		t.Fatalf("expected likes=200 (newer), got %d", row.Metrics.Likes)
	}
}

// ============================================================================
// TEST 5: FetchedAt e LastCheckedAt corretos
// ============================================================================

func TestFetchedAtAndLastCheckedAt(t *testing.T) {
	syncSvc, fakeProvider, memoryStore, fakeClock := setupTest()
	ctx := context.Background()

	baseTime := time.Now().UTC()
	post1 := makePost("post1", baseTime, baseTime, 100, 1000, 10)
	fakeProvider.AddData("", []domain.PostMetrics{post1})
	fakeProvider.SetScenarios(provider.ScenarioOK)

	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}

	// Primeira sync
	fakeClock.Advance(time.Hour)
	firstSyncTime := fakeClock.Now()
	_, _ = syncSvc.Sync(ctx, conn, window)

	key := domain.Key{Platform: "instagram", AccountID: "ada", PostID: "post1"}
	row1, _ := memoryStore.GetRow(key)
	if row1.FetchedAt != firstSyncTime {
		t.Fatalf("FetchedAt should be first sync time")
	}
	if row1.LastCheckedAt != firstSyncTime {
		t.Fatalf("LastCheckedAt should be first sync time")
	}

	// Segunda sync (unchanged)
	fakeProvider.AddData("", []domain.PostMetrics{post1})
	fakeProvider.SetScenarios(provider.ScenarioOK)
	fakeClock.Advance(time.Hour)
	secondSyncTime := fakeClock.Now()

	result2, _ := syncSvc.Sync(ctx, conn, window)
	if result2.Summary.Unchanged != 1 {
		t.Fatalf("expected 1 unchanged, got %d", result2.Summary.Unchanged)
	}

	row2, _ := memoryStore.GetRow(key)
	// FetchedAt NÃO deve mudar em unchanged
	if row2.FetchedAt != firstSyncTime {
		t.Fatalf("FetchedAt should not change on unchanged")
	}
	// LastCheckedAt DEVE mudar
	if row2.LastCheckedAt != secondSyncTime {
		t.Fatalf("LastCheckedAt should update on unchanged")
	}
}

// ============================================================================
// TEST 6: 429 espera exatamente o Retry-After
// ============================================================================

func TestRateLimit_WaitsExactlyRetryAfter(t *testing.T) {
	syncSvc, fakeProvider, _, fakeClock := setupTest()
	ctx := context.Background()

	baseTime := time.Now().UTC()
	post1 := makePost("post1", baseTime, baseTime, 100, 1000, 10)
	fakeProvider.AddData("", []domain.PostMetrics{post1})

	// Configurar: 429 com 7 segundos
	fakeProvider.SetScenarios(provider.ScenarioRateLimit, provider.ScenarioOK)
	fakeProvider.SetRateLimitSeconds(7)

	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}

	startTime := fakeClock.Now()
	result, _ := syncSvc.Sync(ctx, conn, window)
	endTime := fakeClock.Now()

	if result.Status != domain.COMPLETED {
		t.Fatalf("expected COMPLETED, got %s", result.Status)
	}

	// Verificar que esperou exatamente 7 segundos
	elapsed := endTime.Sub(startTime)
	if elapsed != 7*time.Second {
		t.Fatalf("expected 7s elapsed, got %v", elapsed)
	}

	// Verificar sleeps no fake clock
	sleeps := fakeClock.GetSleeps()
	if len(sleeps) != 1 {
		t.Fatalf("expected 1 sleep, got %d", len(sleeps))
	}
	if sleeps[0] != 7*time.Second {
		t.Fatalf("expected sleep of 7s, got %v", sleeps[0])
	}
}

// ============================================================================
// TEST 7: 429 acima do teto falha sem esperar
// ============================================================================

func TestRateLimit_AboveMax_FailsWithoutWaiting(t *testing.T) {
	syncSvc, fakeProvider, _, fakeClock := setupTest()
	ctx := context.Background()

	baseTime := time.Now().UTC()
	post1 := makePost("post1", baseTime, baseTime, 100, 1000, 10)
	fakeProvider.AddData("", []domain.PostMetrics{post1})

	// Configurar: 429 com 60 segundos (acima do teto de 30s)
	fakeProvider.SetScenarios(provider.ScenarioRateLimit)
	fakeProvider.SetRateLimitSeconds(60)

	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}

	startTime := fakeClock.Now()
	result, _ := syncSvc.Sync(ctx, conn, window)
	endTime := fakeClock.Now()

	if result.Status != domain.FAILED {
		t.Fatalf("expected FAILED, got %s", result.Status)
	}

	// Não deve ter esperado
	elapsed := endTime.Sub(startTime)
	if elapsed != 0 {
		t.Fatalf("expected 0s elapsed (no wait), got %v", elapsed)
	}

	sleeps := fakeClock.GetSleeps()
	if len(sleeps) != 0 {
		t.Fatalf("expected 0 sleeps, got %d", len(sleeps))
	}
}

// ============================================================================
// TEST 8: 429 repetido para no teto
// ============================================================================

func TestRateLimit_Repeated_StopsAtMaxAttempts(t *testing.T) {
	syncSvc, fakeProvider, _, fakeClock := setupTest()
	ctx := context.Background()

	baseTime := time.Now().UTC()
	post1 := makePost("post1", baseTime, baseTime, 100, 1000, 10)
	fakeProvider.AddData("", []domain.PostMetrics{post1})

	// Configurar: 429 repetido 5 vezes (maxAttempts=4, deve parar antes)
	fakeProvider.SetScenarios(
		provider.ScenarioRateLimit,
		provider.ScenarioRateLimit,
		provider.ScenarioRateLimit,
		provider.ScenarioRateLimit,
		provider.ScenarioRateLimit,
	)
	fakeProvider.SetRateLimitSeconds(1)

	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}

	result, _ := syncSvc.Sync(ctx, conn, window)

	if result.Status != domain.FAILED {
		t.Fatalf("expected FAILED, got %s", result.Status)
	}

	// Deve ter tentado no máximo 4 vezes (maxAttempts).
	// 4 tentativas de 429 com Retry-After=1s geram 3 sleeps: a última
	// tentativa falha sem dormir porque não há próxima chamada.
	sleeps := fakeClock.GetSleeps()
	if len(sleeps) != 3 {
		t.Fatalf("expected 3 sleeps (4 attempts, no sleep after last), got %d", len(sleeps))
	}
	for i, s := range sleeps {
		if s != 1*time.Second {
			t.Fatalf("sleep[%d]: expected 1s, got %v", i, s)
		}
	}
}

// ============================================================================
// TEST 8b: Tabela completa de Retry-After (zero / negativo / acima do teto)
// ============================================================================

func TestRateLimit_RetryAfterTable(t *testing.T) {
	baseTime := time.Now().UTC()
	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}

	cases := []struct {
		name          string
		retryAfter    time.Duration
		wantSleeps    []time.Duration // sleeps esperados por tentativa de página
		wantStatus    domain.SyncStatus
		wantCompleted bool
	}{
		{
			name:          "zero retenta imediatamente sem sleep",
			retryAfter:    0,
			wantSleeps:    nil,
			wantStatus:    domain.COMPLETED,
			wantCompleted: true,
		},
		{
			name:          "positivo pequeno espera exatamente o valor",
			retryAfter:    2 * time.Second,
			wantSleeps:    []time.Duration{2 * time.Second},
			wantStatus:    domain.COMPLETED,
			wantCompleted: true,
		},
		{
			name:          "no teto (30s) ainda espera",
			retryAfter:    30 * time.Second,
			wantSleeps:    []time.Duration{30 * time.Second},
			wantStatus:    domain.COMPLETED,
			wantCompleted: true,
		},
		{
			name:          "acima do teto falha sem esperar",
			retryAfter:    31 * time.Second,
			wantSleeps:    nil,
			wantStatus:    domain.FAILED,
			wantCompleted: false,
		},
		{
			// Negativo não é um Retry-After válido: trata como TransientError
			// (backoff 500ms) e depois completa na 2ª tentativa.
			name:          "negativo vira TransientError com backoff",
			retryAfter:    -5 * time.Second,
			wantSleeps:    []time.Duration{500 * time.Millisecond},
			wantStatus:    domain.COMPLETED,
			wantCompleted: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			syncSvc, fakeProvider, _, fakeClock := setupTest()
			ctx := context.Background()

			post1 := makePost("post1", baseTime, baseTime, 100, 1000, 10)
			fakeProvider.AddData("", []domain.PostMetrics{post1})
			// 1ª chamada: 429 com o Retry-After do caso; 2ª: sucesso.
			// Acima do teto nunca chega à 2ª chamada (falha sem esperar).
			fakeProvider.SetScenarios(provider.ScenarioRateLimit, provider.ScenarioOK)
			fakeProvider.SetRateLimitSeconds(int(tc.retryAfter / time.Second))

			window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}
			result, err := syncSvc.Sync(ctx, conn, window)
			if err != nil {
				t.Fatalf("unexpected sync error: %v", err)
			}
			if result.Status != tc.wantStatus {
				t.Fatalf("expected %s, got %s", tc.wantStatus, result.Status)
			}

			sleeps := fakeClock.GetSleeps()
			if len(sleeps) != len(tc.wantSleeps) {
				t.Fatalf("expected %d sleeps (%v), got %d (%v)",
					len(tc.wantSleeps), tc.wantSleeps, len(sleeps), sleeps)
			}
			for i, want := range tc.wantSleeps {
				if sleeps[i] != want {
					t.Fatalf("sleep[%d]: expected %v, got %v", i, want, sleeps[i])
				}
			}
		})
	}
}

// ============================================================================
// TEST 9: Timeouts com backoff e teto
// ============================================================================

func TestTimeout_RetryWithBackoff_StopsAtMax(t *testing.T) {
	syncSvc, fakeProvider, _, fakeClock := setupTest()
	ctx := context.Background()

	baseTime := time.Now().UTC()
	post1 := makePost("post1", baseTime, baseTime, 100, 1000, 10)
	fakeProvider.AddData("", []domain.PostMetrics{post1})

	// Configurar: timeout repetido
	fakeProvider.SetScenarios(
		provider.ScenarioTimeout,
		provider.ScenarioTimeout,
		provider.ScenarioTimeout,
		provider.ScenarioTimeout,
		provider.ScenarioOK,
	)

	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}

	result, _ := syncSvc.Sync(ctx, conn, window)

	// Deve falhar após maxAttempts
	if result.Status != domain.FAILED {
		t.Fatalf("expected FAILED, got %s", result.Status)
	}

	// Verificar backoff: 500ms, 1s, 2s (3 retries)
	sleeps := fakeClock.GetSleeps()
	expectedSleeps := []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second}
	if len(sleeps) != len(expectedSleeps) {
		t.Fatalf("expected %d sleeps, got %d", len(expectedSleeps), len(sleeps))
	}
	for i, expected := range expectedSleeps {
		if sleeps[i] != expected {
			t.Fatalf("sleep[%d]: expected %v, got %v", i, expected, sleeps[i])
		}
	}
}

// ============================================================================
// TEST 10: Paginação por cursor (3 páginas)
// ============================================================================

func TestPagination_ThreePages(t *testing.T) {
	syncSvc, fakeProvider, memoryStore, _ := setupTest()
	ctx := context.Background()

	baseTime := time.Now().UTC()

	// Página 1
	post1 := makePost("post1", baseTime, baseTime, 100, 1000, 10)
	post2 := makePost("post2", baseTime, baseTime, 200, 2000, 20)
	fakeProvider.AddData("", []domain.PostMetrics{post1, post2})

	// Página 2
	post3 := makePost("post3", baseTime, baseTime, 300, 3000, 30)
	fakeProvider.AddData("page2", []domain.PostMetrics{post3})

	// Página 3
	post4 := makePost("post4", baseTime, baseTime, 400, 4000, 40)
	fakeProvider.AddData("page3", []domain.PostMetrics{post4})

	fakeProvider.SetScenarios(provider.ScenarioOK, provider.ScenarioOK, provider.ScenarioOK)

	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}

	result, _ := syncSvc.Sync(ctx, conn, window)

	if result.Status != domain.COMPLETED {
		t.Fatalf("expected COMPLETED, got %s", result.Status)
	}
	if result.Summary.Inserted != 4 {
		t.Fatalf("expected 4 inserted, got %d", result.Summary.Inserted)
	}
	if memoryStore.Count() != 4 {
		t.Fatalf("expected 4 rows in store, got %d", memoryStore.Count())
	}
}

// ============================================================================
// TEST 11: Cancelamento de contexto durante Sleep
// ============================================================================

func TestContextCancel_DuringSleep(t *testing.T) {
	_, fakeProvider, memoryStore, _ := setupTest()
	blockingClock := cancelableSleepClock{entered: make(chan struct{})}
	syncSvc := NewSyncService(fakeProvider, memoryStore, blockingClock, DefaultConfig(), slog.New(slog.NewTextHandler(os.Stdout, nil)))

	baseTime := time.Now().UTC()
	post1 := makePost("post1", baseTime, baseTime, 100, 1000, 10)
	fakeProvider.AddData("", []domain.PostMetrics{post1})

	// Configurar: 429 para forçar sleep
	fakeProvider.SetScenarios(provider.ScenarioRateLimit)
	fakeProvider.SetRateLimitSeconds(10)

	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}

	// Criar contexto cancelável
	ctx, cancel := context.WithCancel(context.Background())

	// Iniciar sync em goroutine
	done := make(chan error, 1)
	go func() {
		_, err := syncSvc.Sync(ctx, conn, window)
		done <- err
	}()

	// Esperar a tentativa entrar no Sleep e cancelar de forma determinística.
	<-blockingClock.entered
	cancel()

	// Deve retornar erro de contexto cancelado
	err := <-done
	if err == nil || err != context.Canceled {
		t.Fatalf("expected context.Canceled error, got %v", err)
	}
}

// ============================================================================
// TEST 12: Cancelamento de contexto durante fetchPosts (não retenta)
// ============================================================================

func TestContextCancel_DuringFetchPosts_NoRetry(t *testing.T) {
	syncSvc, fakeProvider, _, _ := setupTest()

	baseTime := time.Now().UTC()
	post1 := makePost("post1", baseTime, baseTime, 100, 1000, 10)
	fakeProvider.AddData("", []domain.PostMetrics{post1})

	// O FakeProvider já respeita o contexto
	fakeProvider.SetScenarios(provider.ScenarioOK)

	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}

	// Criar contexto já cancelado
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := syncSvc.Sync(ctx, conn, window)
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// ============================================================================
// TEST 13: Duas syncs concorrentes com -race
// ============================================================================

func TestConcurrentSyncs_OverlappingWindows(t *testing.T) {
	syncSvc, fakeProvider, memoryStore, _ := setupTest()

	baseTime := time.Now().UTC()
	post1 := makePost("post1", baseTime, baseTime, 100, 1000, 10)
	post2 := makePost("post2", baseTime, baseTime, 200, 2000, 20)

	// Adicionar dados para ambas as syncs
	fakeProvider.AddData("", []domain.PostMetrics{post1})
	fakeProvider.AddData("", []domain.PostMetrics{post2})
	fakeProvider.SetScenarios(provider.ScenarioOK, provider.ScenarioOK)

	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}

	// Executar duas syncs concorrentes
	done := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		go func() {
			ctx := context.Background()
			syncSvc.Sync(ctx, conn, window)
			done <- true
		}()
	}

	// Esperar ambas terminarem
	<-done
	<-done

	// Deve ter no máximo 2 linhas (não duplicar)
	count := memoryStore.Count()
	if count > 2 {
		t.Fatalf("expected at most 2 rows, got %d (possible race condition)", count)
	}
}

// ============================================================================
// TEST 14: Falha a meio da paginação e re-sync sem duplicar
// ============================================================================

func TestPartialFailure_ReSync_NoDuplication(t *testing.T) {
	syncSvc, fakeProvider, memoryStore, _ := setupTest()
	ctx := context.Background()

	baseTime := time.Now().UTC()

	// Página 1
	post1 := makePost("post1", baseTime, baseTime, 100, 1000, 10)
	fakeProvider.AddData("", []domain.PostMetrics{post1})

	// Página 2
	post2 := makePost("post2", baseTime, baseTime, 200, 2000, 20)
	fakeProvider.AddData("page2", []domain.PostMetrics{post2})

	// Primeira sync: falha na página 2.
	// A página 1 consome 1 chamada (OK); a página 2 precisa esgotar as
	// maxAttempts=4 tentativas (1 inicial + 3 retries) para virar PARTIAL.
	fakeProvider.SetScenarios(
		provider.ScenarioOK,
		provider.ScenarioTimeout,
		provider.ScenarioTimeout,
		provider.ScenarioTimeout,
		provider.ScenarioTimeout,
	)

	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}

	result1, _ := syncSvc.Sync(ctx, conn, window)
	if result1.Status != domain.PARTIAL {
		t.Fatalf("expected PARTIAL, got %s", result1.Status)
	}
	if result1.Summary.Inserted != 1 {
		t.Fatalf("expected 1 inserted, got %d", result1.Summary.Inserted)
	}

	// Re-sync: deve completar sem duplicar a página 1
	fakeProvider.SetScenarios(provider.ScenarioOK, provider.ScenarioOK)

	result2, _ := syncSvc.Sync(ctx, conn, window)
	if result2.Status != domain.COMPLETED {
		t.Fatalf("expected COMPLETED, got %s", result2.Status)
	}
	// Página 1 deve ser unchanged
	if result2.Summary.Unchanged != 1 {
		t.Fatalf("expected 1 unchanged (page1), got %d", result2.Summary.Unchanged)
	}
	// Página 2 deve ser inserted
	if result2.Summary.Inserted != 1 {
		t.Fatalf("expected 1 inserted (page2), got %d", result2.Summary.Inserted)
	}

	// Total deve ser 2 linhas
	if memoryStore.Count() != 2 {
		t.Fatalf("expected 2 rows, got %d", memoryStore.Count())
	}
}

// ============================================================================
// TEST 15: Conflito de snapshot incrementa conflicts
// ============================================================================

func TestSnapshotConflict_IncrementsConflicts(t *testing.T) {
	syncSvc, fakeProvider, memoryStore, _ := setupTest()
	ctx := context.Background()

	baseTime := time.Now().UTC()
	snapshotTime := baseTime

	// Primeiro sync: likes=100
	post1 := makePost("post1", baseTime, snapshotTime, 100, 1000, 10)
	fakeProvider.AddData("", []domain.PostMetrics{post1})
	fakeProvider.SetScenarios(provider.ScenarioOK)

	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}

	result1, _ := syncSvc.Sync(ctx, conn, window)
	if result1.Summary.Inserted != 1 {
		t.Fatalf("expected 1 inserted, got %d", result1.Summary.Inserted)
	}

	// Segundo sync: mesmo snapshotAt mas likes=200 (conflito)
	post1Conflict := makePost("post1", baseTime, snapshotTime, 200, 2000, 20)
	fakeProvider.AddData("", []domain.PostMetrics{post1Conflict})
	fakeProvider.SetScenarios(provider.ScenarioOK)

	result2, _ := syncSvc.Sync(ctx, conn, window)
	if result2.Summary.Conflicts != 1 {
		t.Fatalf("expected 1 conflict, got %d", result2.Summary.Conflicts)
	}
	if result2.Summary.Unchanged != 1 {
		t.Fatalf("expected 1 unchanged, got %d", result2.Summary.Unchanged)
	}

	// Verificar que manteve o valor original
	key := domain.Key{Platform: "instagram", AccountID: "ada", PostID: "post1"}
	row, _ := memoryStore.GetRow(key)
	if row.Metrics.Likes != 100 {
		t.Fatalf("expected likes=100 (original), got %d", row.Metrics.Likes)
	}
}

// ============================================================================
// TEST 16: Intra-página com mesmo postId e snapshotAt diferentes
// ============================================================================

func TestIntraPage_DifferentSnapshotAt_NewerWins(t *testing.T) {
	syncSvc, fakeProvider, memoryStore, _ := setupTest()
	ctx := context.Background()

	baseTime := time.Now().UTC()

	// Mesmo postId com snapshotAt diferentes na mesma página
	post1Old := makePost("post1", baseTime, baseTime, 100, 1000, 10)
	post1New := makePost("post1", baseTime, baseTime.Add(time.Hour), 200, 2000, 20)

	fakeProvider.AddData("", []domain.PostMetrics{post1Old, post1New})
	fakeProvider.SetScenarios(provider.ScenarioDuplicate) // duplica para simular

	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}

	result, _ := syncSvc.Sync(ctx, conn, window)

	// Deve ter inserido apenas 1 (deduped com o mais novo)
	if result.Summary.Inserted != 1 {
		t.Fatalf("expected 1 inserted, got %d", result.Summary.Inserted)
	}

	// Verificar que ficou com o mais novo
	key := domain.Key{Platform: "instagram", AccountID: "ada", PostID: "post1"}
	row, _ := memoryStore.GetRow(key)
	if row.Metrics.Likes != 200 {
		t.Fatalf("expected likes=200 (newer), got %d", row.Metrics.Likes)
	}
	if row.Snapshot.SnapshotAt != baseTime.Add(time.Hour) {
		t.Fatalf("expected newer snapshotAt, got %v", row.Snapshot.SnapshotAt)
	}
}

// ============================================================================
// TEST 17: GET /metrics reflete totais após re-sync
// ============================================================================

func TestGetMetrics_ReflectsTotalsAfterResync(t *testing.T) {
	syncSvc, fakeProvider, memoryStore, _ := setupTest()
	ctx := context.Background()

	baseTime := time.Now().UTC()

	// Primeira sync: 2 posts
	post1 := makePost("post1", baseTime, baseTime, 100, 1000, 10)
	post2 := makePost("post2", baseTime, baseTime, 200, 2000, 20)
	fakeProvider.AddData("", []domain.PostMetrics{post1, post2})
	fakeProvider.SetScenarios(provider.ScenarioOK)

	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}

	result1, _ := syncSvc.Sync(ctx, conn, window)
	if result1.Summary.Inserted != 2 {
		t.Fatalf("expected 2 inserted, got %d", result1.Summary.Inserted)
	}

	// Verificar totais
	totals1 := memoryStore.GetTotals("instagram", "ada")
	if totals1.Likes != 300 {
		t.Fatalf("expected likes=300, got %d", totals1.Likes)
	}
	if totals1.Count != 2 {
		t.Fatalf("expected count=2, got %d", totals1.Count)
	}

	// Re-sync com valores atualizados
	post1Updated := makePost("post1", baseTime, baseTime.Add(time.Hour), 150, 1500, 15)
	post2Updated := makePost("post2", baseTime, baseTime.Add(time.Hour), 250, 2500, 25)
	fakeProvider.AddData("", []domain.PostMetrics{post1Updated, post2Updated})
	fakeProvider.SetScenarios(provider.ScenarioOK)

	result2, _ := syncSvc.Sync(ctx, conn, window)
	if result2.Summary.Updated != 2 {
		t.Fatalf("expected 2 updated, got %d", result2.Summary.Updated)
	}

	// Verificar totais atualizados
	totals2 := memoryStore.GetTotals("instagram", "ada")
	if totals2.Likes != 400 {
		t.Fatalf("expected likes=400 after update, got %d", totals2.Likes)
	}
	if totals2.Count != 2 {
		t.Fatalf("expected count=2, got %d", totals2.Count)
	}
}

// ============================================================================
// TEST 18: syncId único por execução
// ============================================================================

func TestSyncId_UniquePerExecution(t *testing.T) {
	syncSvc, fakeProvider, _, _ := setupTest()
	ctx := context.Background()

	baseTime := time.Now().UTC()
	post1 := makePost("post1", baseTime, baseTime, 100, 1000, 10)
	fakeProvider.AddData("", []domain.PostMetrics{post1})
	fakeProvider.SetScenarios(provider.ScenarioOK)

	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}

	// Primeira sync
	result1, _ := syncSvc.Sync(ctx, conn, window)
	syncId1 := result1.SyncID

	// Segunda sync
	fakeProvider.AddData("", []domain.PostMetrics{post1})
	fakeProvider.SetScenarios(provider.ScenarioOK)
	result2, _ := syncSvc.Sync(ctx, conn, window)
	syncId2 := result2.SyncID

	// syncIds devem ser diferentes
	if syncId1 == syncId2 {
		t.Fatalf("expected different syncIds, got same: %s", syncId1)
	}

	// syncIds não devem estar vazios
	if syncId1 == "" || syncId2 == "" {
		t.Fatalf("syncIds should not be empty")
	}
}

// ============================================================================
// TEST 19: Timeout por tentativa (context.WithTimeout) vira TransientError
// ============================================================================

// slowProvider ignora a janela e bloqueia as primeiras `blockCalls` chamadas
// até ao contexto expirar, simulando um provider lento.
type slowProvider struct {
	mu         sync.Mutex
	calls      int
	blockCalls int
	posts      []domain.PostMetrics
}

func (p *slowProvider) FetchPosts(ctx context.Context, _ domain.Connection, _ domain.Window, _ string) (domain.Page, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()

	if n <= p.blockCalls {
		<-ctx.Done() // espera pelo attemptTimeout
		return domain.Page{}, ctx.Err()
	}
	return domain.Page{Items: p.posts}, nil
}

func (p *slowProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestAttemptTimeout_ExpiresIntoTransientError(t *testing.T) {
	baseTime := time.Now().UTC()
	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}
	post1 := makePost("post1", baseTime, baseTime, 100, 1000, 10)

	cases := []struct {
		name       string
		blockCalls int
		wantStatus domain.SyncStatus
		wantCalls  int
		wantSleeps []time.Duration
		wantRows   int
	}{
		{
			name:       "recupera depois de uma tentativa expirada",
			blockCalls: 1,
			wantStatus: domain.COMPLETED,
			wantCalls:  2,
			wantSleeps: []time.Duration{500 * time.Millisecond},
			wantRows:   1,
		},
		{
			name:       "desiste no teto de tentativas",
			blockCalls: 99,
			wantStatus: domain.FAILED,
			wantCalls:  4, // maxAttempts = 4
			wantSleeps: []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second},
			wantRows:   0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeClock := clock.NewFakeClock(baseTime)
			memoryStore := store.NewMemoryStore(nil)
			cfg := DefaultConfig()
			cfg.AttemptTimeout = 50 * time.Millisecond // tempo real, o resto é falso
			prov := &slowProvider{blockCalls: tc.blockCalls, posts: []domain.PostMetrics{post1}}

			syncSvc := NewSyncService(prov, memoryStore, fakeClock, cfg, nil)

			result, err := syncSvc.Sync(context.Background(), conn, window)
			if err != nil {
				t.Fatalf("unexpected sync error: %v", err)
			}
			if result.Status != tc.wantStatus {
				t.Fatalf("expected %s, got %s", tc.wantStatus, result.Status)
			}
			if got := prov.callCount(); got != tc.wantCalls {
				t.Fatalf("expected %d provider calls, got %d", tc.wantCalls, got)
			}
			// O tempo expirado é tratado como falha transitória: backoff normal.
			sleeps := fakeClock.GetSleeps()
			if len(sleeps) != len(tc.wantSleeps) {
				t.Fatalf("expected %d sleeps (%v), got %v", len(tc.wantSleeps), tc.wantSleeps, sleeps)
			}
			for i, want := range tc.wantSleeps {
				if sleeps[i] != want {
					t.Fatalf("sleep[%d]: expected %v, got %v", i, want, sleeps[i])
				}
			}
			if memoryStore.Count() != tc.wantRows {
				t.Fatalf("expected %d rows in store, got %d", tc.wantRows, memoryStore.Count())
			}
		})
	}
}

// ============================================================================
// TEST 20: Teto de backoff (maxBackoff = 5s)
// ============================================================================

func TestBackoff_CappedAtMaxBackoff(t *testing.T) {
	syncSvc, fakeProvider, _, fakeClock := setupTest()
	ctx := context.Background()

	baseTime := time.Now().UTC()
	post1 := makePost("post1", baseTime, baseTime, 100, 1000, 10)
	fakeProvider.AddData("", []domain.PostMetrics{post1})

	// maxAttempts = 6 para o backoff chegar ao teto: 500ms, 1s, 2s, 4s, 8s->5s
	fakeProvider.SetScenarios(
		provider.ScenarioTimeout,
		provider.ScenarioTimeout,
		provider.ScenarioTimeout,
		provider.ScenarioTimeout,
		provider.ScenarioTimeout,
		provider.ScenarioTimeout,
	)
	cfg := DefaultConfig()
	cfg.MaxAttempts = 6
	syncSvc = NewSyncService(fakeProvider, syncSvc.store, syncSvc.clock, cfg, syncSvc.logger)

	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	window := domain.Window{From: baseTime, To: baseTime.AddDate(0, 0, 1)}

	result, _ := syncSvc.Sync(ctx, conn, window)
	if result.Status != domain.FAILED {
		t.Fatalf("expected FAILED, got %s", result.Status)
	}

	// O 5º sleep seria 8s (500ms * 2^4), limitado a maxBackoff = 5s.
	expectedSleeps := []time.Duration{
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		5 * time.Second,
	}
	sleeps := fakeClock.GetSleeps()
	if len(sleeps) != len(expectedSleeps) {
		t.Fatalf("expected %d sleeps, got %d (%v)", len(expectedSleeps), len(sleeps), sleeps)
	}
	for i, expected := range expectedSleeps {
		if sleeps[i] != expected {
			t.Fatalf("sleep[%d]: expected %v, got %v", i, expected, sleeps[i])
		}
	}
	if DefaultConfig().MaxBackoff != 5*time.Second {
		t.Fatalf("expected maxBackoff=5s, got %v", DefaultConfig().MaxBackoff)
	}
}

// ============================================================================
// TEST 21: A Window filtra por publishedAt na regra de negócio
// ============================================================================

// rawProvider devolve sempre os mesmos itens, ignorando a janela: serve para
// provar que o filtro é da responsabilidade da regra de negócio.
type rawProvider struct {
	items []domain.PostMetrics
}

func (p rawProvider) FetchPosts(context.Context, domain.Connection, domain.Window, string) (domain.Page, error) {
	return domain.Page{Items: p.items}, nil
}

func TestWindow_FiltersByPublishedAt(t *testing.T) {
	baseTime := time.Now().UTC()
	conn := domain.Connection{Platform: "instagram", AccountID: "ada", Token: "fake"}
	memoryStore := store.NewMemoryStore(nil)
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	inside := makePost("inside", baseTime, baseTime, 100, 1000, 10)
	before := makePost("before", baseTime.AddDate(0, 0, -5), baseTime, 200, 2000, 20)
	after := makePost("after", baseTime.AddDate(0, 0, 5), baseTime, 300, 3000, 30)

	prov := rawProvider{items: []domain.PostMetrics{inside, before, after}}
	fakeClock := clock.NewFakeClock(baseTime)
	syncSvc := NewSyncService(prov, memoryStore, fakeClock, DefaultConfig(), logger)

	window := domain.Window{From: baseTime.AddDate(0, 0, -1), To: baseTime.AddDate(0, 0, 1)}
	result, err := syncSvc.Sync(context.Background(), conn, window)
	if err != nil {
		t.Fatalf("unexpected sync error: %v", err)
	}
	if result.Status != domain.COMPLETED {
		t.Fatalf("expected COMPLETED, got %s", result.Status)
	}
	if result.Summary.Inserted != 1 {
		t.Fatalf("expected 1 inserted (only the post inside the window), got %d", result.Summary.Inserted)
	}
	if memoryStore.Count() != 1 {
		t.Fatalf("expected 1 row in store, got %d", memoryStore.Count())
	}
	if _, ok := memoryStore.GetRow(domain.Key{Platform: "instagram", AccountID: "ada", PostID: "inside"}); !ok {
		t.Fatal("expected the post inside the window to be stored")
	}
}
