package domain

import "time"

// Connection representa uma conexão autorizada com uma plataforma
// O token pode ser fictício para testes
type Connection struct {
	Platform  string // "instagram", "tiktok", "youtube", "x"
	AccountID string
	Token     string // token de acesso (pode ser fictício)
}

// Window define o intervalo de tempo para buscar posts
// Filtra por publishedAt: From <= PublishedAt <= To (inclusivo)
type Window struct {
	From time.Time
	To   time.Time
}

// Snapshot contém os dados de um post em um momento específico
// SnapshotAt é quando o provedor mediu as métricas
type Snapshot struct {
	PostID      string    `json:"postId"`
	PublishedAt time.Time `json:"publishedAt"`
	SnapshotAt  time.Time `json:"snapshotAt"`
	Likes       int64     `json:"likes"`
	Views       int64     `json:"views"`
	Comments    int64     `json:"comments"`
}

// Metrics representa os valores numéricos das métricas
type Metrics struct {
	Likes    int64 `json:"likes"`
	Views    int64 `json:"views"`
	Comments int64 `json:"comments"`
}

// PostMetrics combina um Snapshot com suas Metrics
// É o formato que trafega entre o provider e a regra de negócio

type PostMetrics struct {
	Snapshot Snapshot `json:"snapshot"`
	Metrics  Metrics  `json:"metrics"`
}

// PostView é um post como devolvido pela API, já com o rastreio de tempo:
// FetchedAt = quando o valor atual foi obtido; LastCheckedAt = última vez que
// o serviço confirmou o post. Sem estes campos o cliente não consegue saber
// quando a métrica foi buscada.
type PostView struct {
	Snapshot      Snapshot  `json:"snapshot"`
	Metrics       Metrics   `json:"metrics"`
	FetchedAt     time.Time `json:"fetchedAt"`
	LastCheckedAt time.Time `json:"lastCheckedAt"`
}

// Page representa uma página de resultados com paginação por cursor
type Page struct {
	Items      []PostMetrics
	NextCursor string
	HasMore    bool
}

// Key é a chave única para idempotência no store
// Combina plataforma, conta e ID do post
type Key struct {
	Platform  string
	AccountID string
	PostID    string
}

// Totals representa a soma das métricas de todos os posts de uma conta
type Totals struct {
	Likes    int64 `json:"likes"`
	Views    int64 `json:"views"`
	Comments int64 `json:"comments"`
	Count    int64 `json:"count"`
}

// UpsertResult indica o resultado de uma operação de upsert no store
type UpsertResult string

const (
	// Inserted - nova linha criada
	Inserted UpsertResult = "inserted"
	// Updated - linha atualizada com snapshot mais novo
	Updated UpsertResult = "updated"
	// Unchanged - mesmo snapshotAt, nada mudou
	Unchanged UpsertResult = "unchanged"
	// Stale - snapshotAt mais antigo, ignorado para não regredir
	Stale UpsertResult = "stale"
)

// SyncStatus indica o estado final de uma sincronização
type SyncStatus string

const (
	// COMPLETED - todas as páginas gravadas com sucesso
	COMPLETED SyncStatus = "COMPLETED"
	// PARTIAL - falhou depois de gravar pelo menos uma página
	PARTIAL SyncStatus = "PARTIAL"
	// FAILED - falhou antes de gravar qualquer página
	FAILED SyncStatus = "FAILED"
)

// SyncSummary contém o resumo de operações durante o sync
type SyncSummary struct {
	Inserted  int64 `json:"inserted"`
	Updated   int64 `json:"updated"`
	Unchanged int64 `json:"unchanged"`
	Stale     int64 `json:"stale"`
	Conflicts int64 `json:"conflicts"` // quando snapshotAt igual mas números diferentes
}

// SyncResult é o resultado completo de uma sincronização
type SyncResult struct {
	SyncID     string      `json:"syncId"`
	Status     SyncStatus  `json:"status"`
	StartedAt  time.Time   `json:"startedAt"`
	FinishedAt time.Time   `json:"finishedAt"`
	Summary    SyncSummary `json:"summary"`
	Error      string      `json:"error,omitempty"`
}

// SyncRequest é o payload para POST /sync
type SyncRequest struct {
	Platform  string    `json:"platform"`
	AccountID string    `json:"accountId"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
}

// MetricsResponse é o response para GET /metrics/{platform}/{accountId}
type MetricsResponse struct {
	Platform  string     `json:"platform"`
	AccountID string     `json:"accountId"`
	Posts     []PostView `json:"posts"`
	Totals    Totals     `json:"totals"`
}
