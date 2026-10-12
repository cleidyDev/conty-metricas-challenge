package httpapi

import (
	"bytes"
	"conty-challenge/internal/clock"
	"conty-challenge/internal/domain"
	"conty-challenge/internal/provider"
	"conty-challenge/internal/store"
	"conty-challenge/internal/syncer"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func setupHandler() (*Handler, *provider.FakeProvider, *store.MemoryStore, *clock.FakeClock) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fakeClock := clock.NewFakeClock(time.Now().UTC())
	fakeProvider := provider.NewFakeProvider()
	memoryStore := store.NewMemoryStore(logger)
	syncSvc := syncer.NewSyncService(fakeProvider, memoryStore, fakeClock, syncer.DefaultConfig(), logger)
	return NewHandler(syncSvc, memoryStore, logger), fakeProvider, memoryStore, fakeClock
}

func postSync(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/sync", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.SyncHandler(rec, req)
	return rec
}

func mkPost(postID string, ts time.Time, likes int64) domain.PostMetrics {
	return domain.PostMetrics{
		Snapshot: domain.Snapshot{PostID: postID, PublishedAt: ts, SnapshotAt: ts, Likes: likes, Views: likes * 10, Comments: likes / 10},
		Metrics:  domain.Metrics{Likes: likes, Views: likes * 10, Comments: likes / 10},
	}
}

func TestSyncHandler_Completed_Returns200(t *testing.T) {
	h, fakeProvider, _, _ := setupHandler()
	baseTime := time.Now().UTC()
	fakeProvider.AddData("", []domain.PostMetrics{mkPost("post1", baseTime, 100)})
	fakeProvider.SetScenarios(provider.ScenarioOK)
	from := baseTime.Format(time.RFC3339)
	to := baseTime.AddDate(0, 0, 1).Format(time.RFC3339)
	rec := postSync(t, h, `{"platform":"instagram","accountId":"ada","from":"`+from+`","to":"`+to+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var result domain.SyncResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("expected JSON SyncResult, got error: %v", err)
	}
	if result.Status != domain.COMPLETED {
		t.Fatalf("expected COMPLETED, got %s", result.Status)
	}
	if result.SyncID == "" {
		t.Fatal("expected non-empty syncId")
	}
	if result.Summary.Inserted != 1 {
		t.Fatalf("expected 1 inserted, got %d", result.Summary.Inserted)
	}
}

func TestSyncHandler_Partial_Returns502(t *testing.T) {
	h, fakeProvider, _, _ := setupHandler()
	baseTime := time.Now().UTC()
	fakeProvider.AddData("", []domain.PostMetrics{mkPost("post1", baseTime, 100)})
	fakeProvider.AddData("page2", []domain.PostMetrics{mkPost("post2", baseTime, 200)})
	fakeProvider.SetScenarios(provider.ScenarioOK, provider.ScenarioTimeout, provider.ScenarioTimeout, provider.ScenarioTimeout, provider.ScenarioTimeout)
	from := baseTime.Format(time.RFC3339)
	to := baseTime.AddDate(0, 0, 1).Format(time.RFC3339)
	rec := postSync(t, h, `{"platform":"instagram","accountId":"ada","from":"`+from+`","to":"`+to+`"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d (%s)", rec.Code, rec.Body.String())
	}
	var result domain.SyncResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("expected JSON SyncResult, got error: %v", err)
	}
	if result.Status != domain.PARTIAL {
		t.Fatalf("expected PARTIAL, got %s", result.Status)
	}
	if result.Error == "" {
		t.Fatal("expected non-empty error on PARTIAL")
	}
}

func TestSyncHandler_Failed_Returns502(t *testing.T) {
	h, fakeProvider, _, _ := setupHandler()
	baseTime := time.Now().UTC()
	fakeProvider.AddData("", []domain.PostMetrics{mkPost("post1", baseTime, 100)})
	// Falha na primeira página: esgota maxAttempts sem gravar nada -> FAILED.
	fakeProvider.SetScenarios(
		provider.ScenarioTimeout,
		provider.ScenarioTimeout,
		provider.ScenarioTimeout,
		provider.ScenarioTimeout,
	)
	from := baseTime.Format(time.RFC3339)
	to := baseTime.AddDate(0, 0, 1).Format(time.RFC3339)
	rec := postSync(t, h, `{"platform":"instagram","accountId":"ada","from":"`+from+`","to":"`+to+`"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d (%s)", rec.Code, rec.Body.String())
	}
	var result domain.SyncResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("expected JSON SyncResult, got error: %v", err)
	}
	if result.Status != domain.FAILED {
		t.Fatalf("expected FAILED, got %s", result.Status)
	}
	if result.Summary.Inserted != 0 {
		t.Fatalf("expected 0 inserted, got %d", result.Summary.Inserted)
	}
	if result.Error == "" {
		t.Fatal("expected non-empty error on FAILED")
	}
}

func TestSyncHandler_InvalidWindow_Returns400(t *testing.T) {
	h, _, _, _ := setupHandler()
	baseTime := time.Now().UTC()
	from := baseTime.Format(time.RFC3339)
	to := baseTime.Add(time.Hour).Format(time.RFC3339)
	cases := []struct{ name, body string }{
		{"from depois de to", `{"platform":"instagram","accountId":"ada","from":"` + to + `","to":"` + from + `"}`},
		{"from ausente", `{"platform":"instagram","accountId":"ada","to":"` + to + `"}`},
		{"to ausente", `{"platform":"instagram","accountId":"ada","from":"` + from + `"}`},
		{"json invalido", `{"platform":`},
		{"platform ausente", `{"accountId":"ada","from":"` + from + `","to":"` + to + `"}`},
		{"accountId ausente", `{"platform":"instagram","from":"` + from + `","to":"` + to + `"}`},
		{"corpo vazio", ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postSync(t, h, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSyncHandler_RoutesWithGo122Patterns(t *testing.T) {
	h, fakeProvider, _, _ := setupHandler()
	baseTime := time.Now().UTC()
	fakeProvider.AddData("", []domain.PostMetrics{mkPost("post1", baseTime, 50)})
	fakeProvider.SetScenarios(provider.ScenarioOK)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sync", h.SyncHandler)
	mux.HandleFunc("GET /metrics/{platform}/{accountId}", h.MetricsHandler)
	from := baseTime.Format(time.RFC3339)
	to := baseTime.AddDate(0, 0, 1).Format(time.RFC3339)
	syncReq := httptest.NewRequest(http.MethodPost, "/sync", bytes.NewBufferString(`{"platform":"instagram","accountId":"ada","from":"`+from+`","to":"`+to+`"}`))
	syncReq.Header.Set("Content-Type", "application/json")
	syncRec := httptest.NewRecorder()
	mux.ServeHTTP(syncRec, syncReq)
	if syncRec.Code != http.StatusOK {
		t.Fatalf("POST /sync via mux: expected 200, got %d (%s)", syncRec.Code, syncRec.Body.String())
	}
	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics/instagram/ada", nil)
	metricsRec := httptest.NewRecorder()
	mux.ServeHTTP(metricsRec, metricsReq)
	if metricsRec.Code != http.StatusOK {
		t.Fatalf("GET /metrics via mux: expected 200, got %d (%s)", metricsRec.Code, metricsRec.Body.String())
	}
	var resp domain.MetricsResponse
	if err := json.NewDecoder(metricsRec.Body).Decode(&resp); err != nil {
		t.Fatalf("expected JSON MetricsResponse, got error: %v", err)
	}
	if resp.Platform != "instagram" || resp.AccountID != "ada" {
		t.Fatalf("unexpected platform/account: %+v", resp)
	}
	if resp.Totals.Likes != 50 || resp.Totals.Count != 1 {
		t.Fatalf("unexpected totals: %+v", resp.Totals)
	}
	if len(resp.Posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(resp.Posts))
	}
}

// ============================================================================
// VALIDAÇÕES DEFENSIVAS
// ============================================================================

func TestSyncHandler_CorpoNil(t *testing.T) {
	h, _, _, _ := setupHandler()
	// Pedido construído à mão para ter Body == nil (o httptest.NewRequest
	// substitui por http.NoBody).
	req := &http.Request{Method: http.MethodPost, Header: http.Header{}}
	rec := httptest.NewRecorder()

	h.SyncHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestSyncHandler_CorpoComDoisObjetos(t *testing.T) {
	h, _, _, _ := setupHandler()
	body := `{"platform":"instagram","accountId":"ada","from":"2024-01-01T00:00:00Z","to":"2024-01-02T00:00:00Z"} {"extra":true}`

	rec := postSync(t, h, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for trailing content, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestSyncHandler_ContentTypeNaoValidado(t *testing.T) {
	// O body é JSON válido: o handler não depende do Content-Type.
	h, fakeProvider, _, _ := setupHandler()
	baseTime := time.Now().UTC()
	fakeProvider.AddData("", []domain.PostMetrics{mkPost("post1", baseTime, 10)})
	fakeProvider.SetScenarios(provider.ScenarioOK)

	from := baseTime.Format(time.RFC3339)
	to := baseTime.AddDate(0, 0, 1).Format(time.RFC3339)
	body := `{"platform":"instagram","accountId":"ada","from":"` + from + `","to":"` + to + `"}`
	req := httptest.NewRequest(http.MethodPost, "/sync", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()

	h.SyncHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestMetricsHandler_ValidaPathValues(t *testing.T) {
	// Chamada direta ao handler, sem o mux: os path values não existem e a
	// validação tem de devolver 400 em vez de responder com conta vazia.
	h, _, _, _ := setupHandler()

	cases := []struct {
		name      string
		platform  string
		accountID string
	}{
		{"sem platform", "", "ada"},
		{"sem accountId", "instagram", ""},
		{"sem ambos", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			req.SetPathValue("platform", tc.platform)
			req.SetPathValue("accountId", tc.accountID)
			rec := httptest.NewRecorder()

			h.MetricsHandler(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestMetricsHandler_OrdenacaoEstavel(t *testing.T) {
	h, fakeProvider, _, _ := setupHandler()
	baseTime := time.Now().UTC()
	// Inseridos por ordem inversa: a resposta tem de vir por publishedAt.
	fakeProvider.AddData("", []domain.PostMetrics{
		mkPost("post3", baseTime.AddDate(0, 0, 3), 30),
		mkPost("post1", baseTime, 10),
		mkPost("post2", baseTime.AddDate(0, 0, 1), 20),
	})
	fakeProvider.SetScenarios(provider.ScenarioOK)

	from := baseTime.Format(time.RFC3339)
	to := baseTime.AddDate(0, 0, 5).Format(time.RFC3339)
	if rec := postSync(t, h, `{"platform":"instagram","accountId":"ada","from":"`+from+`","to":"`+to+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("POST /sync: expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics/{platform}/{accountId}", h.MetricsHandler)

	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/metrics/instagram/ada", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		var resp domain.MetricsResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("unexpected decode error: %v", err)
		}
		want := []string{"post1", "post2", "post3"}
		if len(resp.Posts) != len(want) {
			t.Fatalf("expected %d posts, got %d", len(want), len(resp.Posts))
		}
		for j, w := range want {
			if resp.Posts[j].Snapshot.PostID != w {
				t.Fatalf("read %d: expected %s at %d, got %s", i, w, j, resp.Posts[j].Snapshot.PostID)
			}
		}
	}
}

func TestMetricsHandler_ExposesFetchTimesAndEmptyAccount(t *testing.T) {
	h, fakeProvider, _, _ := setupHandler()
	baseTime := time.Now().UTC()
	fakeProvider.AddData("", []domain.PostMetrics{mkPost("post1", baseTime, 50)})
	fakeProvider.SetScenarios(provider.ScenarioOK)

	from := baseTime.Format(time.RFC3339)
	to := baseTime.AddDate(0, 0, 1).Format(time.RFC3339)
	if rec := postSync(t, h, `{"platform":"instagram","accountId":"ada","from":"`+from+`","to":"`+to+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("POST /sync: expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	// posts[] traz fetchedAt e lastCheckedAt: o cliente sabe quando foi buscado.
	// Usa-se o ServeMux real porque os path values só existem lá.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sync", h.SyncHandler)
	mux.HandleFunc("GET /metrics/{platform}/{accountId}", h.MetricsHandler)

	req := httptest.NewRequest(http.MethodGet, "/metrics/instagram/ada", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics: expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var resp domain.MetricsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("expected JSON MetricsResponse, got error: %v", err)
	}
	if len(resp.Posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(resp.Posts))
	}
	if resp.Posts[0].FetchedAt.IsZero() || resp.Posts[0].LastCheckedAt.IsZero() {
		t.Fatalf("expected non-zero fetchedAt/lastCheckedAt, got %+v", resp.Posts[0])
	}

	// Conta sem métricas: 200 com posts vazio (não null) e totals a zero.
	emptyReq := httptest.NewRequest(http.MethodGet, "/metrics/instagram/desconhecida", nil)
	emptyRec := httptest.NewRecorder()
	mux.ServeHTTP(emptyRec, emptyReq)
	if emptyRec.Code != http.StatusOK {
		t.Fatalf("GET /metrics (conta vazia): expected 200, got %d", emptyRec.Code)
	}
	if !strings.Contains(emptyRec.Body.String(), `"posts":[]`) {
		t.Fatalf("expected empty posts array, got %s", emptyRec.Body.String())
	}
	if !strings.Contains(emptyRec.Body.String(), `"count":0`) {
		t.Fatalf("expected zero totals, got %s", emptyRec.Body.String())
	}
}
