# Conty Sync — Serviço de Sincronização de Métricas

![Testes](https://github.com/cleidyDev/conty-metricas-challenge/actions/workflows/testes.yml/badge.svg)

Serviço em Go que recebe uma conexão já autorizada (o token pode ser fictício) e
sincroniza as métricas de posts de redes sociais. O foco é sobreviver a
timeout, resposta duplicada e limite de taxa do provedor, sem nunca contar o
mesmo post duas vezes.

## Arquitetura

```
challenge-conty/
├── server/main.go          # ponto de entrada do servidor HTTP
├── internal/
│   ├── domain/             # tipos, erros e contratos JSON
│   ├── clock/              # abstração de tempo (SystemClock + FakeClock)
│   ├── provider/           # interface do provedor + FakeProvider
│   ├── store/              # MemoryStore idempotente
│   ├── syncer/             # regra de negócio (SyncService)
│   └── httpapi/            # handlers HTTP
└── go.mod
```

O nome de cada pasta bate com o do pacote. `syncer` e `httpapi` existem para
não colidir com `sync` e `net/http` da biblioteca padrão.

### Princípios

1. **O provedor fica atrás de uma interface.** A regra de negócio só conhece
   `FetchPosts` e dois erros: `TransientError` e `RateLimitError`.
2. **O tempo é injetado.** Um `Clock` com `Now()` e `Sleep(ctx, d)` permite
   testar esperas sem esperar.
3. **A idempotência vive no store.** Uma linha por post; o total soma linhas e
   nunca respostas.
4. **Toda a repetição é limitada.** Timeouts e 429 partilham o mesmo teto de
   tentativas, por página.

## Como a idempotência funciona quando o mesmo post chega de novo

A chave única é `(plataforma, conta, postId)`. Cada `Upsert` no store decide o
que fazer comparando o `snapshotAt` que chega com o que já está gravado:

| Situação | Resultado | Efeito |
|---|---|---|
| Não havia linha | `inserted` | Cria a linha |
| Mesmo `snapshotAt`, mesmos números | `unchanged` | Nada muda |
| Mesmo `snapshotAt`, números diferentes | `unchanged` + `conflicts` | No-op e registado no log |
| `snapshotAt` mais novo | `updated` | Substitui, sem somar |
| `snapshotAt` mais antigo | `stale` | Ignorado, para não regredir |

Isto cobre as quatro formas de o mesmo post voltar:

- **Janelas sobrepostas** — a segunda sincronização dá `unchanged` e o store
  continua com uma linha.
- **Retry de página** — a mesma resposta repetida dá `unchanged`.
- **Re-sync** — `unchanged`, sem trabalho.
- **Duplicado dentro da mesma resposta** — descartado antes do store, em
  `dedupePage`; se vier com `snapshotAt` diferentes, ganha o mais novo.

O `Upsert` corre sob `sync.Mutex`, por isso "comparar e gravar" é atómico e
duas sincronizações concorrentes não duplicam.

### Rastreio de tempo

`FetchedAt` muda só em `inserted` e `updated` — é quando o valor atual foi
obtido. `LastCheckedAt` muda sempre, incluindo em `unchanged` — é a última vez
que o serviço confirmou o post. Os dois campos saem em `GET /metrics`.

## O que acontece quando o provedor responde 429

O `RetryAfter` vem dentro do `RateLimitError` e o syncer decide por tabela:

| `Retry-After` | Comportamento |
|---|---|
| `0` | Retry imediato, sem `Sleep`, conta como tentativa |
| `> 0` e `<= maxRetryAfter` (30s) | `Clock.Sleep(ctx, d)` — dorme exatamente o valor pedido |
| `> maxRetryAfter` | Falha sem esperar (esperar menos só daria outro 429) |
| Negativo / inválido | Vira `TransientError`, com backoff exponencial |

O 429 conta para `maxAttempts` (4 por página, partilhado com timeouts) e na
última tentativa falha sem dormir, porque não há chamada seguinte.
`context.Canceled` do chamador nunca é repetido.

O teste que prova isto é `TestRateLimit_WaitsExactlyRetryAfter`: o
`FakeProvider` devolve 429 com 7s, o `FakeClock` regista o `Sleep` e avança o
relógio sem gastar tempo real. Verifica que o relógio do fake avançou 7s e que
o sleep registado foi 7000ms.

## Configuração de retry

| Parâmetro | Valor |
|---|---|
| `baseBackoff` | 500ms, ×2, sem jitter |
| `maxBackoff` | 5s |
| `maxAttempts` | 4 por página (1 inicial + 3 retries) |
| `maxRetryAfter` | 30s |
| `attemptTimeout` | 2s por tentativa, via `context.WithTimeout` |

## API HTTP

### POST /sync

```json
{
  "platform": "instagram",
  "accountId": "ada",
  "from": "2024-01-01T00:00:00Z",
  "to": "2024-01-31T23:59:59Z"
}
```

**200 — COMPLETED:**
```json
{
  "syncId": "d4e26f9fafced0cfb04be22dc2b46d8a",
  "status": "COMPLETED",
  "startedAt": "2024-01-01T10:00:00Z",
  "finishedAt": "2024-01-01T10:00:05Z",
  "summary": { "inserted": 10, "updated": 5, "unchanged": 3, "stale": 2, "conflicts": 0 }
}
```

**502 — PARTIAL / FAILED**, com o resumo do que ficou gravado e `error`:
```json
{
  "syncId": "d4e26f9fafced0cfb04be22dc2b46d8a",
  "status": "PARTIAL",
  "startedAt": "2024-01-01T10:00:00Z",
  "finishedAt": "2024-01-01T10:00:03Z",
  "summary": { "inserted": 5, "updated": 0, "unchanged": 0, "stale": 0, "conflicts": 0 },
  "error": "rate limit exceeded, retry after 31s"
}
```

**400** — janela inválida (`from` depois de `to`, `from`/`to` ausentes),
`platform` ou `accountId` vazios, ou JSON malformado. Texto puro via `http.Error`.

Estados: `COMPLETED` (todas as páginas gravadas), `PARTIAL` (falhou depois de
gravar pelo menos uma página), `FAILED` (falhou antes de gravar qualquer uma).

### GET /metrics/{platform}/{accountId}

```json
{
  "platform": "instagram",
  "accountId": "ada",
  "posts": [
    {
      "snapshot": {
        "postId": "post1",
        "publishedAt": "2024-01-01T12:00:00Z",
        "snapshotAt": "2024-01-02T10:00:00Z",
        "likes": 100,
        "views": 1000,
        "comments": 10
      },
      "metrics": { "likes": 100, "views": 1000, "comments": 10 },
      "fetchedAt": "2024-01-02T10:00:00Z",
      "lastCheckedAt": "2024-01-03T09:00:00Z"
    }
  ],
  "totals": { "likes": 100, "views": 1000, "comments": 10, "count": 1 }
}
```

`posts` vem ordenado por `publishedAt` e depois por `postId`. Uma conta sem
métricas devolve `200` com `"posts": []` e totais a zero.

## Como rodar

Requer Go 1.22 ou superior.

```bash
go mod download
go run server/main.go     # http://localhost:8081
```

## Testes

112 testes, com deteção de races:

```bash
go test ./...          # tudo
go test -race ./...    # com detector de races
```

Cada módulo tem testes que correm sozinhos, sem depender do `syncer` nem do HTTP:

```bash
go test ./internal/clock/      # relógio
go test ./internal/store/      # idempotência e concorrência
go test ./internal/provider/   # fake, paginação e janela
go test ./internal/domain/     # contratos JSON e erros
go test -run TestUpsert -v ./internal/store/   # um teste só
```

| Módulo | Testes | O que prova |
|---|---|---|
| `clock` | 17 | `FakeClock` regista sleeps e avança sem tempo real; `SystemClock` aborta no cancelamento |
| `domain` | 15 | Contratos JSON, `error` a `omitempty`, `errors.As` sobre os dois erros |
| `store` | 25 | Tabela `inserted/updated/unchanged/stale`, `conflicts`, totais, concorrência com 100 goroutines |
| `provider` | 22 | Cenários `ok`/`timeout`/`duplicate`/`rateLimit`, filtro de janela, paginação de 3 páginas |
| `syncer` | 22 | Idempotência, retry, 429, paginação, cancelamento, `PARTIAL`, concorrência |
| `httpapi` | 11 | Contratos 200/502/400 e rotas Go 1.22 no `ServeMux` real |

`server` fica a 0% por ser só wiring (monta as dependências e sobe o `ServeMux`);
esse mesmo wiring é exercitado nos testes do `httpapi`.

### Testes automáticos

O workflow `.github/workflows/testes.yml` corre em cada push para `main` e em
cada pull request. Para cada versão da matrix (Go 1.22, a mínima do `go.mod`, e
1.23) verifica:

1. `gofmt` — falha e mostra os ficheiros se algum não estiver formatado
2. `go vet`
3. `go test -race ./...`
4. `go test -cover ./...`

Um segundo job faz apenas `go build ./...`. O badge no topo deste README mostra
o estado da última execução.

## Limitações conhecidas

1. **MemoryStore** — em memória. Em produção seria PostgreSQL com
   `UNIQUE(platform, account_id, post_id)` para garantir a idempotência também
   entre processos.
2. **FakeProvider** — a paginação é fixa a três páginas (cursor `""` → `page2`
   → `page3`). Um provider real usaria os cursores do próprio backend.
3. **Store em memória** — reiniciar o processo perde as métricas.
4. **Cancelamento do cliente** — se o contexto do pedido HTTP for cancelado, a
   sync termina com erro e a API responde 500 sem `SyncResult` no corpo: não é
   um estado `COMPLETED`/`PARTIAL`/`FAILED` porque não houve gravação para
   reportar.

---

## Nota de uso de IA

### Feito com assistência de IA

- Estrutura de pastas, `go.mod` e todos os ficheiros de código: tipos e erros
  de domínio, `Clock`/`SystemClock`/`FakeClock`, `MetricsProvider` com
  `FakeProvider`, `MemoryStore`, `SyncService` com retry e 429, handlers HTTP e
  `server/main.go`.
- Todos os testes (112) e este README.
- Comentários no código escritos em português.

### Feito por mim (revisão manual)

- Estrutura de pastas e nomes de pacotes (`sync`, `api`, `syncer`), decididos
  para não colidir com `sync` e `net/http` da stdlib.
- `FakeClock` com mutex e registo de sleeps, para poder afirmar as esperas.
- Separação de `FetchedAt` e `LastCheckedAt`: no `unchanged` o `fetchedAt` não
  pode mexer, senão mostrava a primeira busca e não a última.
- Decidir que o `Retry-After` acima do teto falha sem esperar, e que 429 conta
  para o mesmo teto de tentativas que os timeouts.
- Revisão do código de cada módulo depois de gerado.

### Corrigido durante a revisão

1. **429 na última tentativa** — o syncer dormia o `Retry-After` mesmo sem
   tentativa seguinte (espera inútil). Passou a falhar direto.
2. **`Retry-After` negativo** — passou a ser tratado como `TransientError` com
   backoff, detetado por `errors.As`.
3. **`syncId` fixo** — estava literal `"1234567"`; passou a ser gerado por
   execução com `crypto/rand`, para o identificador ser mesmo único.
4. **Rota com typo** — `GET /metrics/{plataform}/{accountId}` não casava com o
   `r.PathValue("platform")` do handler, portanto a rota devolvia 404.
5. **Nomes das pastas** — a pasta chamava-se `sync` mas o pacote era `syncer`,
   o que obrigava a alias em todos os imports; e a da API era `api` com
   `package appi` dentro. Passaram a `syncer` e `httpapi`, com o nome da pasta
   igual ao do pacote, para não colidir com `sync` e `net/http` da stdlib.
6. **Versão de Go** — o `go.mod` pedia 1.26.5 sem necessidade; o código usa
  rotas com path values do Go 1.22, por isso a exigência foi posta em 1.22.
7. **Filtro da janela** — estava só no `FakeProvider`, o que não garantia nada
   num provider real. Passou também a estar no `syncer`, depois do dedupe e
   antes do `Upsert`.
8. **Ordem não determinística** — `GetPosts` e `dedupePage` iteravam mapas, pelo
   que a ordem dos posts variava entre chamadas; ambos passaram a ordenar.
9. **Código morto** — removidos um helper JSON nunca usado e um `IsRateLimitError`
   nunca usado.
10. **Testes em falta** — repostos e acrescentados testes para 502 em `FAILED`,
    para o timeout por tentativa, para o teto de backoff, para o filtro de janela
    e para `GET /metrics`.

### O que não está feito

Não há provider real (Instagram, TikTok, YouTube, X): o enunciado pedia
explicitamente um provedor simulado e um token fictício. Não há persistência em
base de dados — a nota da limitação 1 é o passo seguinte.