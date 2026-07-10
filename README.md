# Jupiter Gold — Order Matching Engine

A high-performance, in-memory order matching engine for gold trading (primarily `GOLD_IRR`). Part of the Jupiter Gold platform, this service implements price-time priority matching with limit orders, partial fills, cancellations, and time-based expirations. Exposes dual REST (Gin) and gRPC interfaces backed by PostgreSQL for persistence.

---

## Architecture

```
┌──────────────────────────────┐
│     HTTP (Gin) :8082         │
├──────────────────────────────┤
│     gRPC :8083               │
├──────────────────────────────┤
│  Matching Engine (in-memory) │
│  ├─ Market (per symbol)      │
│  │  ├─ BuyHeap  (max-heap)   │
│  │  ├─ SellHeap (min-heap)   │
│  │  └─ ExpiryHeap            │
│  └─ sync.Mutex               │
├──────────────────────────────┤
│  PostgreSQL (GORM)           │
└──────────────────────────────┘
```

All active orders are held in Go heap data structures (`container/heap`) for O(log n) matching. The database serves as the source of truth for crash recovery; state is rebuilt on startup via `LoadFromDB()`.

---

## Matching Algorithm

**Price-Time Priority** — the standard limit order book model:

| Side | Priority |
|------|----------|
| Buy  | Highest price first → earliest creation time |
| Sell | Lowest price first → earliest creation time |

When a match occurs, the execution price is taken from the **resting** (older) order. Matching loops until the book no longer crosses (highest bid < lowest ask).

**Order lifecycle:** `created` → `partial` → `filled` / `canceled` / `expired`

---

## Quick Start

### Prerequisites

- Go 1.22+
- PostgreSQL

### Configuration

```yaml
server:
  port: 8082

database:
  host: localhost
  port: 5432
  user: postgres
  password: postgres
  dbname: matching_engine
  sslmode: disable

log:
  level: "info"
```

All values can be overridden via environment variables.

### Run

```bash
# Build & run
go build -o matching-engine ./cmd
./matching-engine

# Or using Docker
docker build -t matching-engine .
docker run -p 8082:8082 matching-engine
```

---

## API

### REST (HTTP) — `:8082`

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Health check |
| `POST` | `/api/v1/orders` | Place a new limit order |
| `GET` | `/api/v1/orders/:id` | Get order by ID |
| `DELETE` | `/api/v1/orders/:id` | Cancel an active order |
| `GET` | `/api/v1/orderbook/:symbol` | Get full order book (default: `GOLD_IRR`) |

#### PlaceOrder Example

```bash
curl -X POST http://localhost:8082/api/v1/orders \
  -H "Content-Type: application/json" \
  -d '{
    "user_id": 1,
    "symbol": "GOLD_IRR",
    "is_buy_order": true,
    "price": 100.50,
    "quantity": 10.0,
    "expire_sec": 3600
  }'
```

**Response** (201 Created):
```json
{
  "order": { /* full order object */ },
  "matches": [ /* immediate execution records */ ]
}
```

### gRPC — `:8083`

| RPC | Description |
|-----|-------------|
| `PlaceOrder` | Place a new order |
| `GetOrder` | Get order by ID |
| `CancelOrder` | Cancel an order |
| `GetOrderBook` | Get order book snapshot |

Proto definitions: `proto/matching/v1/engine.proto` (vendored as `github.com/jupiter/gold-proto`).

---

## Data Model

```go
type Order struct {
    ID         uint        // Auto-increment primary key
    UserID     uint        // Owner of the order
    Symbol     string      // Trading pair (e.g. "GOLD_IRR")
    IsBuyOrder bool        // true = buy, false = sell
    Price      float64     // Limit price
    Quantity   float64     // Original quantity
    FilledQty  float64     // Executed quantity
    Status     OrderStatus // created | partial | filled | canceled | expired
    BotID      string      // Optional bot identifier (gRPC)
    Expiration time.Time   // Optional TTL
    Index      int         // Heap index (transient, not persisted)
}
```

---

## Project Structure

```
├── cmd/main.go              # Entry point
├── config/config.go         # Viper-based configuration
├── config.yaml              # Default configuration
├── Dockerfile               # Multi-stage Docker build (Alpine)
├── proto/                   # Vendored protobuf module
│   ├── matching/v1/         # MatchingEngine gRPC service
│   └── core/v1/             # GoldCore pricing gRPC service (external)
└── internal/
    ├── engine/
    │   ├── order.go         # Order model & heap implementations
    │   ├── engine.go        # Core matching logic & system
    │   └── handler.go       # Gin HTTP handlers
    └── grpcserver/
        └── server.go        # gRPC server implementation
```

---

## Design Highlights

- **Coarse-grained lock** (`sync.Mutex`) — simple and safe for moderate throughput
- **Batch DB writes** — all status updates from a single matching pass are committed together
- **Background expiration** — a 1-second ticker purges expired orders via the ExpiryHeap
- **Dual protocol** — REST for external clients, gRPC for internal microservices
- **Fees handled externally** — the companion `GoldCore` service manages pricing, spreads, and fees

---

## Dependencies

| Package | Purpose |
|---------|---------|
| [gin-gonic/gin](https://github.com/gin-gonic/gin) | HTTP framework |
| [spf13/viper](https://github.com/spf13/viper) | Configuration |
| [go.uber.org/zap](https://go.uber.org/zap) | Structured logging |
| [gorm.io/gorm](https://gorm.io) | ORM / PostgreSQL |
| [google.golang.org/grpc](https://grpc.io) | gRPC framework |
