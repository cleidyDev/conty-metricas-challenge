package store

import (
	"bytes"
	"conty-challenge/internal/domain"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	t0 = time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	t1 = time.Date(2024, 1, 2, 10, 0, 0, 0, time.UTC)
	t2 = time.Date(2024, 1, 3, 10, 0, 0, 0, time.UTC)
	t3 = time.Date(2024, 1, 4, 10, 0, 0, 0, time.UTC)
)

// ============================================================================
// HELPERS
// ============================================================================

func key(postID string) domain.Key {
	return domain.Key{Platform: "instagram", AccountID: "ada", PostID: postID}
}

func snap(snapshotAt time.Time, likes int64) domain.Snapshot {
	return domain.Snapshot{
		PostID:      "post1",
		PublishedAt: t0,
		SnapshotAt:  snapshotAt,
		Likes:       likes,
		Views:       likes * 10,
		Comments:    likes / 10,
	}
}

func metrics(likes int64) domain.Metrics {
	return domain.Metrics{Likes: likes, Views: likes * 10, Comments: likes / 10}
}

func snapFor(postID string, publishedAt, snapshotAt time.Time, likes int64) domain.Snapshot {
	s := snap(snapshotAt, likes)
	s.PostID = postID
	s.PublishedAt = publishedAt
	return s
}

// ============================================================================
// UPSERT: A TABELA DA IDEMPOTÊNCIA
// ============================================================================

func TestUpsert_TabelaDeResultados(t *testing.T) {
	cases := []struct {
		name         string
		seed         *domain.Snapshot // nil = store vazio
		incoming     domain.Snapshot
		wantResult   domain.UpsertResult
		wantConflict bool
		wantLikes    int64
	}{
		{
			name:       "sem linha previa -> inserted",
			incoming:   snap(t1, 100),
			wantResult: domain.Inserted,
			wantLikes:  100,
		},
		{
			name:       "snapshot mais novo -> updated",
			seed:       ptr(snap(t1, 100)),
			incoming:   snap(t2, 200),
			wantResult: domain.Updated,
			wantLikes:  200,
		},
		{
			name:       "mesmo snapshotAt e mesmos numeros -> unchanged",
			seed:       ptr(snap(t1, 100)),
			incoming:   snap(t1, 100),
			wantResult: domain.Unchanged,
			wantLikes:  100,
		},
		{
			name:       "snapshot mais antigo -> stale",
			seed:       ptr(snap(t2, 200)),
			incoming:   snap(t1, 100),
			wantResult: domain.Stale,
			wantLikes:  200, // não regrediu
		},
		{
			name:         "mesmo snapshotAt com numeros diferentes -> unchanged + conflict",
			seed:         ptr(snap(t1, 100)),
			incoming:     snap(t1, 200),
			wantResult:   domain.Unchanged,
			wantConflict: true,
			wantLikes:    100, // no-op: mantém o valor original
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewMemoryStore(nil)
			if tc.seed != nil {
				res, conflict := s.Upsert(key("post1"), *tc.seed, metrics(tc.seed.Likes), t0)
				if res != domain.Inserted || conflict {
					t.Fatalf("seed upsert: expected inserted, got %s (conflict=%v)", res, conflict)
				}
			}

			res, conflict := s.Upsert(key("post1"), tc.incoming, metrics(tc.incoming.Likes), t1)
			if res != tc.wantResult {
				t.Fatalf("expected %s, got %s", tc.wantResult, res)
			}
			if conflict != tc.wantConflict {
				t.Fatalf("expected conflict=%v, got %v", tc.wantConflict, conflict)
			}

			row, ok := s.GetRow(key("post1"))
			if !ok {
				t.Fatal("expected the row to exist")
			}
			if row.Metrics.Likes != tc.wantLikes {
				t.Fatalf("expected likes=%d, got %d", tc.wantLikes, row.Metrics.Likes)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

// ============================================================================
// UPSERT: RASTREIO DE TEMPO
// ============================================================================

func TestUpsert_FetchedAtMudaApenasEmInsertedEUpdated(t *testing.T) {
	s := NewMemoryStore(nil)

	// inserted: FetchedAt = LastCheckedAt = instante do insert.
	if res, _ := s.Upsert(key("post1"), snap(t1, 100), metrics(100), t1); res != domain.Inserted {
		t.Fatalf("expected inserted, got %s", res)
	}
	row, _ := s.GetRow(key("post1"))
	if !row.FetchedAt.Equal(t1) || !row.LastCheckedAt.Equal(t1) {
		t.Fatalf("inserted: expected both at %v, got fetched=%v lastChecked=%v", t1, row.FetchedAt, row.LastCheckedAt)
	}

	// unchanged: FetchedAt fica, LastCheckedAt avança.
	if res, _ := s.Upsert(key("post1"), snap(t1, 100), metrics(100), t2); res != domain.Unchanged {
		t.Fatalf("expected unchanged, got %s", res)
	}
	row, _ = s.GetRow(key("post1"))
	if !row.FetchedAt.Equal(t1) {
		t.Fatalf("unchanged: FetchedAt must stay at %v, got %v", t1, row.FetchedAt)
	}
	if !row.LastCheckedAt.Equal(t2) {
		t.Fatalf("unchanged: LastCheckedAt must be %v, got %v", t2, row.LastCheckedAt)
	}

	// updated: ambos avançam.
	if res, _ := s.Upsert(key("post1"), snap(t3, 200), metrics(200), t3); res != domain.Updated {
		t.Fatalf("expected updated, got %s", res)
	}
	row, _ = s.GetRow(key("post1"))
	if !row.FetchedAt.Equal(t3) || !row.LastCheckedAt.Equal(t3) {
		t.Fatalf("updated: expected both at %v, got fetched=%v lastChecked=%v", t3, row.FetchedAt, row.LastCheckedAt)
	}
}

func TestUpsert_StaleAtualizaLastCheckedAtEPreservaFetchedAt(t *testing.T) {
	s := NewMemoryStore(nil)
	s.Upsert(key("post1"), snap(t3, 200), metrics(200), t3)

	res, conflict := s.Upsert(key("post1"), snap(t1, 100), metrics(100), t0)
	if res != domain.Stale {
		t.Fatalf("expected stale, got %s", res)
	}
	if conflict {
		t.Fatal("a stale snapshot is not a conflict")
	}

	row, _ := s.GetRow(key("post1"))
	if row.Snapshot.SnapshotAt != t3 {
		t.Fatalf("expected the newer snapshot %v to be kept, got %v", t3, row.Snapshot.SnapshotAt)
	}
	if !row.FetchedAt.Equal(t3) {
		t.Fatalf("expected FetchedAt preserved at %v, got %v", t3, row.FetchedAt)
	}
	if !row.LastCheckedAt.Equal(t0) {
		t.Fatalf("expected LastCheckedAt to advance to %v, got %v", t0, row.LastCheckedAt)
	}
}

func TestUpsert_ConflitoPreservaValoresEAtualizaLastCheckedAt(t *testing.T) {
	s := NewMemoryStore(nil)
	s.Upsert(key("post1"), snap(t1, 100), metrics(100), t1)

	res, conflict := s.Upsert(key("post1"), snap(t1, 999), metrics(999), t2)
	if res != domain.Unchanged || !conflict {
		t.Fatalf("expected unchanged with conflict, got %s (conflict=%v)", res, conflict)
	}

	row, _ := s.GetRow(key("post1"))
	if row.Metrics.Likes != 100 {
		t.Fatalf("expected the stored value to win (likes=100), got %d", row.Metrics.Likes)
	}
	if !row.FetchedAt.Equal(t1) {
		t.Fatalf("expected FetchedAt preserved at %v, got %v", t1, row.FetchedAt)
	}
	if !row.LastCheckedAt.Equal(t2) {
		t.Fatalf("expected LastCheckedAt=%v, got %v", t2, row.LastCheckedAt)
	}
}

func TestUpsert_ConflitoDetetadoPorCadaMetrica(t *testing.T) {
	cases := []struct {
		name     string
		old      domain.Metrics
		incoming domain.Metrics
		want     bool
	}{
		{"identicas", domain.Metrics{Likes: 1, Views: 2, Comments: 3}, domain.Metrics{Likes: 1, Views: 2, Comments: 3}, false},
		{"likes diferente", domain.Metrics{Likes: 1, Views: 2, Comments: 3}, domain.Metrics{Likes: 9, Views: 2, Comments: 3}, true},
		{"views diferente", domain.Metrics{Likes: 1, Views: 2, Comments: 3}, domain.Metrics{Likes: 1, Views: 9, Comments: 3}, true},
		{"comments diferente", domain.Metrics{Likes: 1, Views: 2, Comments: 3}, domain.Metrics{Likes: 1, Views: 2, Comments: 9}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewMemoryStore(nil)
			snapshot := snap(t1, 1)
			snapshot.Likes, snapshot.Views, snapshot.Comments = tc.old.Likes, tc.old.Views, tc.old.Comments
			s.Upsert(key("post1"), snapshot, tc.old, t1)

			res, conflict := s.Upsert(key("post1"), snap(t1, 1), tc.incoming, t2)
			if res != domain.Unchanged {
				t.Fatalf("expected unchanged, got %s", res)
			}
			if conflict != tc.want {
				t.Fatalf("expected conflict=%v, got %v", tc.want, conflict)
			}
		})
	}
}

// ============================================================================
// UPSERT: CHAVES E ISOLAMENTO
// ============================================================================

func TestUpsert_ChavesDiferentesSaoLinhasDiferentes(t *testing.T) {
	s := NewMemoryStore(nil)
	keys := []domain.Key{
		{Platform: "instagram", AccountID: "ada", PostID: "post1"},
		{Platform: "tiktok", AccountID: "ada", PostID: "post1"},
		{Platform: "instagram", AccountID: "bob", PostID: "post1"},
		{Platform: "instagram", AccountID: "ada", PostID: "post2"},
	}

	for i, k := range keys {
		snapshot := snap(t1, int64(100*(i+1)))
		snapshot.PostID = k.PostID
		res, _ := s.Upsert(k, snapshot, metrics(snapshot.Likes), t1)
		if res != domain.Inserted {
			t.Fatalf("key %v: expected inserted, got %s", k, res)
		}
	}

	if got := s.Count(); got != 4 {
		t.Fatalf("expected 4 rows, got %d", got)
	}
}

func TestUpsert_JanelasSobrepostasNaoDuplicam(t *testing.T) {
	// O mesmo post.sync chega três vezes (três janelas sobrepostas).
	s := NewMemoryStore(nil)
	first, conflict := s.Upsert(key("post1"), snap(t1, 100), metrics(100), t1)
	if first != domain.Inserted || conflict {
		t.Fatalf("expected inserted, got %s (conflict=%v)", first, conflict)
	}
	for i := 0; i < 2; i++ {
		res, conflict := s.Upsert(key("post1"), snap(t1, 100), metrics(100), t2)
		if res != domain.Unchanged || conflict {
			t.Fatalf("repeat %d: expected unchanged, got %s (conflict=%v)", i, res, conflict)
		}
	}

	if got := s.Count(); got != 1 {
		t.Fatalf("expected 1 row (no duplication), got %d", got)
	}
	totals := s.GetTotals("instagram", "ada")
	if totals.Likes != 100 || totals.Count != 1 {
		t.Fatalf("expected likes=100 count=1, got %+v", totals)
	}
}

func TestUpsert_OrdemDeChegadaNaoRegrede(t *testing.T) {
	// Snapshots fora de ordem (retry, paginação, re-sync) não podem regredir.
	s := NewMemoryStore(nil)
	for _, snapshotAt := range []time.Time{t3, t1, t2, t3, t0} {
		s.Upsert(key("post1"), snap(snapshotAt, 200), metrics(200), snapshotAt)
	}

	row, _ := s.GetRow(key("post1"))
	if row.Snapshot.SnapshotAt != t3 {
		t.Fatalf("expected the newest snapshot %v to survive, got %v", t3, row.Snapshot.SnapshotAt)
	}
	if row.Metrics.Likes != 200 {
		t.Fatalf("expected likes=200, got %d", row.Metrics.Likes)
	}
}

// ============================================================================
// TOTAIS
// ============================================================================

func TestGetTotals(t *testing.T) {
	s := NewMemoryStore(nil)

	post1 := snap(t1, 10)
	s.Upsert(key("post1"), post1, metrics(10), t1)

	post2 := snap(t1, 20)
	post2.PostID = "post2"
	post2.PublishedAt = t1
	s.Upsert(key("post2"), post2, metrics(20), t1)

	// Outra conta e outra plataforma não entram nos totais.
	other := snap(t1, 1000)
	other.PostID = "post1"
	s.Upsert(domain.Key{Platform: "tiktok", AccountID: "bob", PostID: "post1"}, other, metrics(1000), t1)

	totals := s.GetTotals("instagram", "ada")
	if totals.Likes != 30 || totals.Views != 300 || totals.Comments != 3 || totals.Count != 2 {
		t.Fatalf("unexpected totals: %+v", totals)
	}
}

func TestGetTotals_StoreVazioOuContaDesconhecida(t *testing.T) {
	s := NewMemoryStore(nil)
	if got := s.GetTotals("instagram", "ada"); got != (domain.Totals{}) {
		t.Fatalf("expected zero totals on an empty store, got %+v", got)
	}

	post1 := snap(t1, 10)
	s.Upsert(key("post1"), post1, metrics(10), t1)
	if got := s.GetTotals("instagram", "conta-que-nao-existe"); got != (domain.Totals{}) {
		t.Fatalf("expected zero totals for an unknown account, got %+v", got)
	}
}

func TestGetTotals_SomaLinhasNaoRespostas(t *testing.T) {
	// Um update substitui: o total muda para o valor novo, nunca soma.
	s := NewMemoryStore(nil)
	s.Upsert(key("post1"), snap(t1, 100), metrics(100), t1)
	s.Upsert(key("post1"), snap(t1, 100), metrics(100), t1) // unchanged
	s.Upsert(key("post1"), snap(t2, 250), metrics(250), t2) // updated

	totals := s.GetTotals("instagram", "ada")
	if totals.Likes != 250 {
		t.Fatalf("expected likes=250 (row value, not the sum of responses), got %d", totals.Likes)
	}
	if totals.Count != 1 {
		t.Fatalf("expected count=1, got %d", totals.Count)
	}
}

// ============================================================================
// GET POSTS
// ============================================================================

func TestGetPosts_OrdenadoEDeterministico(t *testing.T) {
	s := NewMemoryStore(nil)
	s.Upsert(key("post3"), snapFor("post3", t2, t2, 30), metrics(30), t2)
	s.Upsert(key("post1"), snapFor("post1", t0, t0, 10), metrics(10), t0)
	s.Upsert(key("post2"), snapFor("post2", t1, t1, 20), metrics(20), t1)

	// Várias leituras têm de devolver a mesma ordem (map de Go é aleatório).
	for i := 0; i < 20; i++ {
		posts := s.GetPosts("instagram", "ada")
		if len(posts) != 3 {
			t.Fatalf("expected 3 posts, got %d", len(posts))
		}
		want := []string{"post1", "post2", "post3"}
		for j, w := range want {
			if posts[j].Snapshot.PostID != w {
				t.Fatalf("read %d: expected %s at %d, got %s", i, w, j, posts[j].Snapshot.PostID)
			}
		}
	}
}

func TestGetPosts_MesmoPublishedAtOrdenaPorPostID(t *testing.T) {
	s := NewMemoryStore(nil)
	s.Upsert(key("b"), snapFor("b", t0, t0, 1), metrics(1), t0)
	s.Upsert(key("a"), snapFor("a", t0, t0, 2), metrics(2), t0)

	posts := s.GetPosts("instagram", "ada")
	if posts[0].Snapshot.PostID != "a" || posts[1].Snapshot.PostID != "b" {
		t.Fatalf("expected a then b, got %s then %s", posts[0].Snapshot.PostID, posts[1].Snapshot.PostID)
	}
}

func TestGetPosts_ContaVaziaDevolveSliceNaoNulo(t *testing.T) {
	s := NewMemoryStore(nil)

	posts := s.GetPosts("instagram", "ada")
	if posts == nil {
		t.Fatal("expected an empty slice, not nil (would serialize as null)")
	}
	if len(posts) != 0 {
		t.Fatalf("expected 0 posts, got %d", len(posts))
	}
}

func TestGetPosts_TrazFetchedAtELastCheckedAt(t *testing.T) {
	s := NewMemoryStore(nil)
	s.Upsert(key("post1"), snap(t1, 100), metrics(100), t1)
	s.Upsert(key("post1"), snap(t1, 100), metrics(100), t3)

	posts := s.GetPosts("instagram", "ada")
	if len(posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(posts))
	}
	if !posts[0].FetchedAt.Equal(t1) {
		t.Fatalf("expected fetchedAt=%v, got %v", t1, posts[0].FetchedAt)
	}
	if !posts[0].LastCheckedAt.Equal(t3) {
		t.Fatalf("expected lastCheckedAt=%v, got %v", t3, posts[0].LastCheckedAt)
	}
	if posts[0].Metrics.Likes != 100 {
		t.Fatalf("expected likes=100, got %d", posts[0].Metrics.Likes)
	}
}

func TestGetPosts_ApenasDaContaPedida(t *testing.T) {
	s := NewMemoryStore(nil)
	s.Upsert(key("post1"), snap(t1, 10), metrics(10), t1)
	other := snap(t1, 99)
	s.Upsert(domain.Key{Platform: "x", AccountID: "outra", PostID: "post1"}, other, metrics(99), t1)

	posts := s.GetPosts("instagram", "ada")
	if len(posts) != 1 || posts[0].Metrics.Likes != 10 {
		t.Fatalf("expected only the requested account's post, got %+v", posts)
	}
}

// ============================================================================
// GET ROW / COUNT / CLEAR
// ============================================================================

func TestGetRow(t *testing.T) {
	s := NewMemoryStore(nil)

	if _, ok := s.GetRow(key("post1")); ok {
		t.Fatal("expected the row to be absent on an empty store")
	}

	s.Upsert(key("post1"), snap(t1, 100), metrics(100), t1)
	row, ok := s.GetRow(key("post1"))
	if !ok {
		t.Fatal("expected the row to exist")
	}
	if row.Snapshot.SnapshotAt != t1 || row.Metrics.Likes != 100 {
		t.Fatalf("unexpected row: %+v", row)
	}
}

func TestCount(t *testing.T) {
	s := NewMemoryStore(nil)
	if got := s.Count(); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}

	post1 := snap(t1, 1)
	post2 := snap(t1, 2)
	post2.PostID = "post2"
	s.Upsert(key("post1"), post1, metrics(1), t1)
	s.Upsert(key("post2"), post2, metrics(2), t1)
	s.Upsert(key("post1"), post1, metrics(1), t1) // unchanged, não cria linha

	if got := s.Count(); got != 2 {
		t.Fatalf("expected 2, got %d", got)
	}
}

func TestClear(t *testing.T) {
	s := NewMemoryStore(nil)
	post1 := snap(t1, 10)
	s.Upsert(key("post1"), post1, metrics(10), t1)

	s.Clear()

	if got := s.Count(); got != 0 {
		t.Fatalf("expected 0 after clear, got %d", got)
	}
	// Depois de limpar, o mesmo post volta a ser inserted.
	res, _ := s.Upsert(key("post1"), post1, metrics(10), t1)
	if res != domain.Inserted {
		t.Fatalf("expected inserted after clear, got %s", res)
	}
}

// ============================================================================
// LOG
// ============================================================================

func TestUpsert_ConflitoEhRegistadoNoLog(t *testing.T) {
	// O requisito é contar o conflito no resumo E registá-lo no log, porque
	// esconde uma inconsistência do provedor.
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := NewMemoryStore(logger)

	s.Upsert(key("post1"), snap(t1, 100), metrics(100), t1)
	if buf.Len() != 0 {
		t.Fatalf("expected no log on a clean insert, got %s", buf.String())
	}

	if _, conflict := s.Upsert(key("post1"), snap(t1, 200), metrics(200), t2); !conflict {
		t.Fatal("expected a conflict")
	}

	logged := buf.String()
	if !strings.Contains(logged, "conflict") {
		t.Fatalf("expected the conflict to be logged, got %s", logged)
	}
	if !strings.Contains(logged, "post1") {
		t.Fatalf("expected the key in the log, got %s", logged)
	}
}

func TestUpsert_NilLoggerNaoPanica(t *testing.T) {
	// O store é usado sem logger nos testes e em biblioteca.
	s := NewMemoryStore(nil)
	res, conflict := s.Upsert(key("post1"), snap(t1, 100), metrics(100), t1)
	if res != domain.Inserted || conflict {
		t.Fatalf("expected inserted, got %s (conflict=%v)", res, conflict)
	}
}

// ============================================================================
// CONCORRÊNCIA (corre com -race)
// ============================================================================

func TestUpsert_ConcorrenteMesmaChaveNaoDuplica(t *testing.T) {
	s := NewMemoryStore(nil)
	post1 := snap(t1, 100)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Upsert(key("post1"), post1, metrics(100), t1)
		}()
	}
	wg.Wait()

	if got := s.Count(); got != 1 {
		t.Fatalf("expected exactly 1 row after 100 concurrent upserts, got %d", got)
	}
}

func TestUpsert_ConcorrenteChavesDiferentes(t *testing.T) {
	s := NewMemoryStore(nil)
	const n = 100

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			postID := string(rune('a'+i%26)) + string(rune('a'+i/26))
			k := key(postID)
			s.Upsert(k, snapFor(postID, t0, t1, int64(i+1)), metrics(int64(i+1)), t1)
		}(i)
	}
	wg.Wait()

	if got := s.Count(); got != n {
		t.Fatalf("expected %d rows, got %d", n, got)
	}
	if totals := s.GetTotals("instagram", "ada"); totals.Count != n {
		t.Fatalf("expected count=%d, got %d", n, totals.Count)
	}
}

func TestUpsert_ConcorrenteSnapshotMaisNovoVence(t *testing.T) {
	// Várias threads gravam snapshots de tempos diferentes; só o maior sobrevive.
	s := NewMemoryStore(nil)
	times := []time.Time{t1, t3, t2, t3, t0, t2}

	var wg sync.WaitGroup
	for _, snapshotAt := range times {
		wg.Add(1)
		go func(snapshotAt time.Time) {
			defer wg.Done()
			s.Upsert(key("post1"), snap(snapshotAt, 200), metrics(200), snapshotAt)
		}(snapshotAt)
	}
	wg.Wait()

	row, _ := s.GetRow(key("post1"))
	if row.Snapshot.SnapshotAt != t3 {
		t.Fatalf("expected the newest snapshot %v to win, got %v", t3, row.Snapshot.SnapshotAt)
	}
}

func TestLeituraConcorrenteComEscritas(t *testing.T) {
	s := NewMemoryStore(nil)
	stop := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			k := key(string(rune('a' + i%26)))
			s.Upsert(k, snap(t1, int64(i+1)), metrics(int64(i+1)), t1)
		}
	}()

	for i := 0; i < 200; i++ {
		s.GetTotals("instagram", "ada")
		s.GetPosts("instagram", "ada")
		s.Count()
	}
	close(stop)
	wg.Wait()
}
