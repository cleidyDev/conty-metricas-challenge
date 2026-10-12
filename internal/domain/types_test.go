package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var (
	jan1 = time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	jan2 = time.Date(2024, 1, 2, 10, 0, 0, 0, time.UTC)
	jan3 = time.Date(2024, 1, 3, 8, 0, 0, 0, time.UTC)
)

// ============================================================================
// CHAVE DE IDEMPOTÊNCIA
// ============================================================================

func TestKey_ValoresDistintosNaoColidem(t *testing.T) {
	// Duas chaves com o mesmo PostID mas plataformas/contas diferentes têm de
	// ser linhas distintas no store.
	base := Key{Platform: "instagram", AccountID: "ada", PostID: "post1"}
	keys := map[Key]string{
		base: "original",
		{Platform: "tiktok", AccountID: "ada", PostID: "post1"}:    "outra plataforma",
		{Platform: "instagram", AccountID: "bob", PostID: "post1"}: "outra conta",
		{Platform: "instagram", AccountID: "ada", PostID: "post2"}: "outro post",
	}

	if len(keys) != 4 {
		t.Fatalf("expected 4 distinct keys, got %d", len(keys))
	}
	if keys[base] != "original" {
		t.Fatalf("expected the original key to be preserved, got %q", keys[base])
	}
}

// ============================================================================
// CONTRATOS JSON
// ============================================================================

func TestSyncResult_JSON_Completo(t *testing.T) {
	result := SyncResult{
		SyncID:     "550e8400-e29b-41d4-a716-446655440000",
		Status:     COMPLETED,
		StartedAt:  jan1,
		FinishedAt: jan2,
		Summary: SyncSummary{
			Inserted:  10,
			Updated:   5,
			Unchanged: 3,
			Stale:     2,
			Conflicts: 1,
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}

	want := `{"syncId":"550e8400-e29b-41d4-a716-446655440000","status":"COMPLETED",` +
		`"startedAt":"2024-01-01T12:00:00Z","finishedAt":"2024-01-02T10:00:00Z",` +
		`"summary":{"inserted":10,"updated":5,"unchanged":3,"stale":2,"conflicts":1}}`
	if string(data) != want {
		t.Fatalf("unexpected JSON:\n got: %s\nwant: %s", data, want)
	}
}

func TestSyncResult_JSON_OmiteErrorVazio(t *testing.T) {
	// `error` tem omitempty: um COMPLETED bem-sucedido não leva o campo.
	data, err := json.Marshal(SyncResult{SyncID: "x", Status: COMPLETED})
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	if got := string(data); contains(got, `"error"`) {
		t.Fatalf("expected no error field, got %s", got)
	}

	// Um PARTIAL/FAILED leva o erro para o cliente saber porque falhou.
	data, err = json.Marshal(SyncResult{SyncID: "x", Status: PARTIAL, Error: "rate limit exceeded"})
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	if got := string(data); !contains(got, `"error":"rate limit exceeded"`) {
		t.Fatalf("expected the error field, got %s", got)
	}
}

func TestSyncRequest_JSON_Parse(t *testing.T) {
	var req SyncRequest
	body := `{"platform":"instagram","accountId":"ada","from":"2024-01-01T00:00:00Z","to":"2024-01-31T23:59:59Z"}`

	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	if req.Platform != "instagram" || req.AccountID != "ada" {
		t.Fatalf("unexpected platform/account: %+v", req)
	}
	if want := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC); !req.From.Equal(want) {
		t.Fatalf("expected from=%v, got %v", want, req.From)
	}
	if want := time.Date(2024, 1, 31, 23, 59, 59, 0, time.UTC); !req.To.Equal(want) {
		t.Fatalf("expected to=%v, got %v", want, req.To)
	}
}

func TestSyncRequest_JSON_CamposAusentesViramZero(t *testing.T) {
	// from/to ausentes resultam em zero time: o handler transforma isso em 400.
	var req SyncRequest
	if err := json.Unmarshal([]byte(`{"platform":"instagram"}`), &req); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	if !req.From.IsZero() || !req.To.IsZero() {
		t.Fatalf("expected zero times, got from=%v to=%v", req.From, req.To)
	}
}

func TestSyncRequest_JSON_DataInvalida(t *testing.T) {
	var req SyncRequest
	if err := json.Unmarshal([]byte(`{"from":"31-01-2024"}`), &req); err == nil {
		t.Fatal("expected an error for an invalid RFC3339 date")
	}
}

func TestMetricsResponse_JSON(t *testing.T) {
	resp := MetricsResponse{
		Platform:  "instagram",
		AccountID: "ada",
		Posts: []PostView{
			{
				Snapshot:      Snapshot{PostID: "post1", PublishedAt: jan1, SnapshotAt: jan2, Likes: 100, Views: 1000, Comments: 10},
				Metrics:       Metrics{Likes: 100, Views: 1000, Comments: 10},
				FetchedAt:     jan2,
				LastCheckedAt: jan3,
			},
		},
		Totals: Totals{Likes: 100, Views: 1000, Comments: 10, Count: 1},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}

	got := string(data)
	for _, want := range []string{
		`"platform":"instagram"`,
		`"accountId":"ada"`,
		`"postId":"post1"`,
		`"fetchedAt":"2024-01-02T10:00:00Z"`,
		`"lastCheckedAt":"2024-01-03T08:00:00Z"`,
		`"count":1`,
	} {
		if !contains(got, want) {
			t.Fatalf("expected %s in %s", want, got)
		}
	}
}

func TestTotals_JSON(t *testing.T) {
	data, err := json.Marshal(Totals{Likes: 1, Views: 2, Comments: 3, Count: 4})
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	want := `{"likes":1,"views":2,"comments":3,"count":4}`
	if string(data) != want {
		t.Fatalf("expected %s, got %s", want, data)
	}
}

func TestPostMetrics_JSON_SemTemposDeBusca(t *testing.T) {
	// PostMetrics é o formato provider -> regra de negócio: sem tempos de busca.
	// Quem os expõe é PostView.
	data, err := json.Marshal(PostMetrics{
		Snapshot: Snapshot{PostID: "p1", Likes: 1},
		Metrics:  Metrics{Likes: 1},
	})
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	got := string(data)
	if contains(got, "fetchedAt") || contains(got, "lastCheckedAt") {
		t.Fatalf("expected no fetch times in PostMetrics, got %s", got)
	}
}

// ============================================================================
// CONSTANTES
// ============================================================================

func TestConstantes_ValoresEsperados(t *testing.T) {
	if Inserted != "inserted" || Updated != "updated" ||
		Unchanged != "unchanged" || Stale != "stale" {
		t.Fatalf("unexpected UpsertResult values: %q %q %q %q", Inserted, Updated, Unchanged, Stale)
	}
	if COMPLETED != "COMPLETED" || PARTIAL != "PARTIAL" || FAILED != "FAILED" {
		t.Fatalf("unexpected SyncStatus values: %q %q %q", COMPLETED, PARTIAL, FAILED)
	}
}

// contains é um atalho para as asserções de JSON.
func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// ============================================================================
// ERROS DO PROVIDER
// ============================================================================

func TestTransientError_ErrorEUnwrap(t *testing.T) {
	inner := errors.New("connection reset")
	err := &TransientError{Err: inner}

	if got, want := err.Error(), "transient error: connection reset"; got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
	if !errors.Is(err, inner) {
		t.Fatal("expected errors.Is to find the wrapped error")
	}
}

func TestTransientError_NilInterno(t *testing.T) {
	err := &TransientError{}
	if got, want := err.Error(), "transient error: <nil>"; got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestRateLimitError_Error(t *testing.T) {
	err := &RateLimitError{RetryAfter: 7 * time.Second}
	if got, want := err.Error(), "rate limit exceeded, retry after 7s"; got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestIsTransientError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"transient direto", &TransientError{Err: errors.New("x")}, true},
		{"transient envolvido em fmt", fmt.Errorf("page 2: %w", &TransientError{Err: errors.New("x")}), true},
		{"rate limit", &RateLimitError{RetryAfter: time.Second}, false},
		{"erro comum", errors.New("boom"), false},
		{"nil", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransientError(tc.err); got != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func TestErrorsAs_DistingueOsDoisErros(t *testing.T) {
	// A regra de negócio separa os dois erros com errors.As; ambos podem
	// aparecer embrulhados por mensagens de contexto.
	var err error = fmt.Errorf("fetch: %w", &RateLimitError{RetryAfter: 30 * time.Second})

	var rle *RateLimitError
	if !errors.As(err, &rle) {
		t.Fatal("expected errors.As to extract the RateLimitError")
	}
	if rle.RetryAfter != 30*time.Second {
		t.Fatalf("expected retryAfter=30s, got %v", rle.RetryAfter)
	}
	if IsTransientError(err) {
		t.Fatal("a RateLimitError must not be treated as transient")
	}

	var te *TransientError
	if errors.As(err, &te) {
		t.Fatal("expected no TransientError in the chain")
	}
}
