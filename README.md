# Robinhood Chain Autonomous Trading Bot

> An open-source, self-hosted Web3 trading bot for Robinhood Chain focused on automated token discovery, security analysis, market intelligence, wallet/insider analysis, risk-controlled execution, capital recovery, and Telegram-based operations.

[![Status](https://img.shields.io/badge/status-experimental-orange)](#project-status)
[![Chain](https://img.shields.io/badge/chain-Robinhood%20Chain-111827)](https://docs.robinhood.com/chain/)
[![Language](https://img.shields.io/badge/backend-Go-00ADD8)](https://go.dev/)

## ⚠️ Disclaimer

This project is experimental software for educational and research purposes.

It is **not financial advice** and it does not guarantee profit. Automated crypto trading can lose some or all trading capital. Smart-contract, liquidity, RPC, wallet, execution, market-data, and infrastructure risks may result in unexpected losses.

Never deploy a new strategy directly with meaningful capital. Start with testnet and paper trading, then use a small amount of capital that you can afford to lose.

---

## Overview

The bot is designed to operate autonomously:

```text
/start
   ↓
Connect / authorize wallet
   ↓
Verify Robinhood Chain
   ↓
Configure risk policy
   ↓
START AUTO TRADING
   ↓
Automatic token discovery
   ↓
Security analysis
   ↓
Market / liquidity analysis
   ↓
Insider / wallet graph analysis
   ↓
Website / community analysis
   ↓
Opportunity scoring
   ↓
Risk Guard
   ├── SKIP
   └── BUY
          ↓
       Monitor
       ├── -5%  → STOP LOSS
       ├── +100% → CAPITAL RECOVERY
       │              ↓
       │           RUNNER
       └── emergency risk → EXIT
          ↓
       PnL / accounting
          ↓
       Telegram
          ↓
       feedback loop
```

The system is designed so that **Telegram is the control and alerting plane, while the actual financial rules are enforced by the backend risk engine**.

---

## Core Trading Policy

### Stop Loss

```text
-5% from entry
```

### Capital Recovery

```text
+100% from entry

Example:

Initial position: $10
Position reaches approximately: $20

Sell enough to recover the original $10 capital
after accounting for execution costs where applicable.

Remaining position becomes the runner.
```

### Runner

After capital recovery, the residual position may continue to run.

The runner can exit due to:

- trailing/risk logic,
- liquidity deterioration,
- severe insider dumping,
- contract/security anomalies,
- strategy invalidation,
- emergency risk conditions.

There is **no hard +20% take-profit** in the current strategy.

### Insider Analysis

Insider analysis is primarily a **risk intelligence signal**, not a fixed profit-taking trigger.

Normal accumulation does not automatically close a winning trade.

Strong coordinated selling, deployer exits, liquidity drains, or other severe anomalies can override the strategy and trigger an emergency exit.

---

## Architecture

![System Architecture](docs/architecture.svg)

The architecture is based on Clean Architecture and a modular-monolith-first approach.

```text
Interface Adapters
    ├── Telegram Bot
    ├── Telegram Mini App
    ├── HTTP API
    └── CLI / operational tools
             ↓
Application / Use Cases
    ├── Discover Tokens
    ├── Analyze Token
    ├── Rank Opportunities
    ├── Evaluate Risk
    ├── Execute Trade
    ├── Recover Capital
    ├── Manage Runner
    └── Calculate PnL
             ↓
Domain
    ├── Token
    ├── MarketSnapshot
    ├── WalletGraph
    ├── Signal
    ├── RiskScore
    ├── Order
    ├── Trade
    ├── Position
    ├── TradingPolicy
    └── PnL
             ↑
Infrastructure
    ├── Robinhood Chain RPC/WebSocket
    ├── DEX adapters
    ├── PostgreSQL
    ├── Redis
    ├── Asynq
    ├── Telegram API
    ├── Asynqmon
└── External market/security providers
```

### Important design rule

Financial business logic must not depend directly on Telegram SDKs, database drivers, RPC providers, HTTP frameworks, or vendor APIs. External systems are adapters.

### Architecture decisions

| Decision | Why |
|---|---|
| **PostgreSQL is the source of truth** | Money needs transactions, constraints and durable history. Balances use `NUMERIC`/exact integers, never floating point — `Wei` wraps `big.Int` so a wei is never rounded away. Timestamps are `TIMESTAMPTZ` and connections are pinned to UTC. |
| **Redis is cache + Asynq backing store, never financial truth** | It holds queues, locks, rate limits and ephemeral state. Losing Redis must cost throughput, not accounting. |
| **Asynq for background jobs** | Redis-backed, boring, well-supported, and it ships an inspection API. No Kafka, RabbitMQ or NATS: a personal bot on one VPS does not need a broker cluster. |
| **Asynqmon for queue monitoring** | The official UI for Asynq. Admin-only, no authentication of its own, so compose binds it to `127.0.0.1`. |
| **pg_partman for time-series partitions** | Child creation and retention are automated rather than hand-run in production. Only genuinely unbounded tables are partitioned. |
| **Modular monolith, one deployable unit** | Two binaries (`api`, `worker`) share one codebase. No Kubernetes, no microservices, until an actual scaling or isolation problem appears. |
| **Chain client is read-only** | Phase 1 has no signer and no broadcast path. Execution cannot be triggered by accident because the capability is absent, not merely disabled. |
| **Retries are bounded everywhere** | Every RPC call has a timeout, a retry ceiling and exponential backoff. Only transport-level errors retry; `not found` and reverts are answers, not failures. |
| **Task handlers are idempotent** | Asynq delivers at-least-once. Sync-state writes only advance, so a replayed task cannot rewind progress. Task IDs give financial operations a deduplication key. |
| **Liveness and readiness are separate** | `/healthz` is dependency-free so a database blip cannot get a correctly-degraded process killed; `/readyz` reports the real dependency state. Wrong chain ID is `down`, a stale head is `degraded`. |

---

## Auto-Trading Lifecycle

```text
WALLET AUTHORIZATION
        ↓
NETWORK VERIFICATION
        ↓
RISK POLICY
        ↓
START AUTO TRADING
        ↓
TOKEN DISCOVERY
        ↓
CANDIDATE FILTER
        ↓
SECURITY ANALYSIS
        ↓
MARKET ANALYSIS
        ↓
INSIDER ANALYSIS
        ↓
SOCIAL / PROJECT ANALYSIS
        ↓
SCORING
        ↓
RISK GUARD
        ↓
QUOTE + SIMULATION
        ↓
SIGN
        ↓
BROADCAST
        ↓
MONITOR FILL
        ↓
POSITION
        ↓
      ┌───────────────┐
      │               │
    -5%             +100%
      │               │
      ↓               ↓
   STOP LOSS    CAPITAL RECOVERY
                      ↓
                   RUNNER
                      ↓
                TRAILING / EXIT
                      ↓
                    PnL
                      ↓
                 TELEGRAM
```

---

## Automatic Token Discovery

The bot is not limited to:

```text
/scan 0xTOKEN
```

It can continuously discover candidates from on-chain and market-data sources such as:

- new contracts,
- new DEX pools,
- liquidity events,
- swap activity,
- indexed token/market sources,
- configurable third-party data providers.

A discovered token is only a **candidate**. It must pass the filtering and analysis pipeline before it becomes tradable.

---

## Token Security Analysis

The security layer can evaluate:

- contract verification,
- proxy/implementation structure,
- owner/admin privileges,
- minting capability,
- pause/freeze capability,
- blacklist/whitelist behavior,
- transfer restrictions,
- buy/sell taxes where observable,
- fee mutation permissions,
- trading controls,
- suspicious contract behavior,
- LP ownership/locking information,
- holder concentration,
- deployer behavior.

A hard security failure must override a high opportunity score.

```text
honeypot suspected
    ↓
REJECT
```

---

## Market Intelligence

The market engine evaluates more than market cap.

Important features include:

```text
market cap
liquidity
volume
volatility
price impact
liquidity / market-cap ratio
holder distribution
pool age
trading frequency
buy/sell pressure
```

A token with:

```text
high market cap
+
tiny liquidity
+
suspicious volume
```

should not be treated as healthy just because the market cap is large.

---

## Insider / Wallet Intelligence

The bot maintains a graph of relevant wallet activity.

Potential relationships include:

```text
Deployer
   ├── funding source
   ├── early buyer A
   ├── early buyer B
   ├── whale
   └── coordinated wallet cluster
```

The system can look for:

- common funding sources,
- early-buyer clusters,
- coordinated entries,
- coordinated exits,
- deployer activity,
- whale accumulation,
- suspicious transfers,
- liquidity-provider actions.

Signals should explain **why** a risk score changed instead of returning only a number.

---

## Website / Community Intelligence

Where data is available, the bot can evaluate:

- website,
- documentation,
- GitHub,
- X,
- Telegram,
- Discord,
- project metadata,
- contract address consistency,
- development activity,
- suspicious or inconsistent project information.

Community data is treated as a **confidence signal**, not proof of legitimacy.

---

## Background Jobs & Queue Architecture

The project uses **Asynq** as the background task queue.

Official project:

- [Asynq](https://github.com/hibiken/asynq)
- Go module: `github.com/hibiken/asynq`

Redis is the backing store for Asynq.

Typical background tasks include:

```text
token:discover
token:analyze
token:security_scan
token:market_scan
token:insider_scan
token:social_scan

signal:score
signal:notify

trade:simulate
trade:submit
trade:monitor

position:update
position:recover_capital
position:update_runner

pnl:update

system:reconcile
system:health_check
```

Suggested queues:

```text
critical
default
analysis
market
notifications
maintenance
```

Tasks must be retry-safe and idempotent where practical. Financial execution must never assume exactly-once task delivery.

### Asynqmon

Queue monitoring uses the official [Asynqmon](https://github.com/hibiken/asynqmon).

Asynqmon is an operational/admin interface for inspecting:

- pending tasks
- active tasks
- scheduled tasks
- retries
- failed tasks
- archived tasks
- queue state

Asynqmon is not a source of financial truth and should not be exposed publicly without appropriate access controls.

---

## Database & Time-Series Storage

PostgreSQL is the persistent source of truth.

Redis is used for:

- Asynq backing storage
- cache
- locks
- rate limiting
- short-lived state

For large time-series workloads, the project uses native PostgreSQL declarative partitioning with **pg_partman** for partition creation and maintenance.

The project does **not** partition every table.

**Partitioned today:** `timeseries.audit_logs` — monthly RANGE partitions on
`occurred_at`, 12-month retention, children dropped rather than detached.
Append-only and unbounded: every command, state transition and operator action
lands there.

**Deliberately not partitioned:** `chain_sync_state`, `users`,
`telegram_users`, `wallets`, `wallet_policies`, and later `tokens`,
`token_contracts`, `orders`, `positions`. These are bounded current-state
tables. Partitioning them would add child-table management cost and
cross-partition joins for no gain.

**Future candidates,** to be partitioned when the feature that writes them lands
and the volume justifies it: `market_snapshots`, `wallet_events`,
`insider_events`, `pnl_snapshots`, `daily_performance`.

pg_partman is pinned to 5.5.0. Its background worker runs as
`partman_maintainer`, a non-superuser role created by migration `00002`.
Version 5.5 changed the BGW default away from a superuser specifically to
mitigate privilege-escalation CVEs, and this project follows that: partition
maintenance is routine, and routine operations should not hold superuser.

All schema changes use versioned migrations (goose), embedded in the binary.
Every migration has a tested `Down`: a migration you cannot roll back is one you
cannot undo during an incident. `make db-recreate` proves the schema rebuilds
from zero.

---

## Telegram Control Plane

Core commands:

```text
/start
/connect

/status
/start_auto
/stop_auto
/pause
/resume

/scan <token>
/details <token>
/watch <token>

/balance
/positions
/orders
/pnl
/stats
/risk
/limits
/health

/sellall
```

Dangerous commands should require explicit confirmation and server-side authorization.

---

## Telegram Mini App

The Mini App is used for interactive flows such as:

- wallet authorization,
- network verification,
- risk-policy configuration,
- trading status,
- compact portfolio view,
- start/stop controls.

Telegram Mini Apps can run directly inside Telegram and can replace a standalone website for many app flows. The Mini App must send `initData` to the backend for validation; `initDataUnsafe` must not be trusted directly.

---

## Wallet & Security Model

Never expose a user's main-wallet private key to Telegram.

Preferred:

```text
OWNER / MAIN WALLET
        ↓
fund / authorize
        ↓
DEDICATED BOT WALLET
        ↓
policy-bound signer
        ↓
Robinhood Chain
```

The bot wallet should hold only the capital intended for automated trading.

Recommended controls:

```text
max position size
max open positions
daily loss limit
allowed contracts / routers
slippage limit
liquidity minimum
emergency pause
```

Future hardening can evaluate programmable wallets, ERC-4337, session keys, spending controls, and gas sponsorship.

---

## Robinhood Chain

Current mainnet baseline:

| Property | Value |
|---|---|
| Network | Robinhood Chain |
| Type | Arbitrum Layer-2 |
| Chain ID | `4663` |
| Testnet Chain ID | `46630` |
| Gas Token | ETH |
| Explorer | `https://robinhoodchain.blockscout.com` |

Robinhood Chain is EVM-compatible and supports standard Ethereum tooling. Official documentation currently recommends Alchemy for production RPC access; public RPC endpoints are rate-limited and are not recommended for production workloads.

Always re-check official documentation before relying on network endpoints, contract addresses, service availability, or provider limits.

---

## Tech Stack

### Backend

- Go
- Clean Architecture
- PostgreSQL 17
- pg_partman
- Redis
- Asynq (`github.com/hibiken/asynq`)
- Asynqmon (`github.com/hibiken/asynqmon`)
- `go-ethereum`

### Web3

- EVM JSON-RPC
- WebSocket
- ABI bindings
- DEX adapters
- transaction simulation
- wallet/signer abstraction

### Telegram

- Telegram Bot API
- Telegram Mini App

### Infrastructure

- Docker
- Docker Compose
- VPS
- GitHub Actions

### Background Jobs

- Asynq (`github.com/hibiken/asynq`)
- Asynqmon (`github.com/hibiken/asynqmon`)
- Redis backing store

### Observability

- structured logs
- health checks
- metrics
- Telegram alerts

Optional later:

- Prometheus
- Grafana
- OpenTelemetry
- Loki

---

## Repository Structure

Packages are created when something needs them, not in advance. What exists
today (Phase 0 + Phase 1):

```text
.
├── cmd/
│   ├── api/               operational HTTP + Mini App backend + head subscriber
│   ├── bot/               Telegram control plane
│   ├── worker/            Asynq worker + periodic scheduler
│   ├── migrate/           migration CLI (up/down/reset/status/version)
│   └── healthcheck/       container HEALTHCHECK probe (distroless has no curl)
│
├── internal/
│   ├── domain/            Address, Hash, BlockRef, Wei, User, Wallet, policy,
│   │                      allowlist, audit, health — no infrastructure imports
│   ├── application/       use cases: health, chain sync, onboarding,
│   │                      wallet linking/verification, Mini App auth
│   ├── bootstrap/         dependency wiring + signal handling
│   ├── chain/             go-ethereum adapter, retry policy, head subscriber
│   ├── config/            environment loading and validation
│   ├── httpapi/           /healthz, /readyz, /version, /api/miniapp/*
│   ├── telegram/          router, commands, bot and notifier transports
│   │   └── initdata/      Mini App signature verification
│   ├── persistence/
│   │   ├── postgres/      pool, migrator, user/wallet/audit/sync repositories
│   │   └── redis/         client, replay guard, rate limiter
│   ├── queue/             Asynq client/server, enqueuer, queue and task names
│   │   └── tasks/         handlers
│   └── observability/
│       ├── logging/       slog setup
│       └── buildinfo/     version metadata
│
├── migrations/            embedded goose SQL migrations
├── deployments/postgres/  PostgreSQL 17 + pg_partman image
├── tests/integration/     tests against real Postgres, Redis, Asynq and RPC
├── docs/
├── docker-compose.yml
├── Dockerfile
├── Makefile
└── README.md
```

Later phases add `token/`, `discovery/`, `security/`, `market/`, `insider/`,
`social/`, `scoring/`, `strategy/`, `risk/`, `execution/`, `position/` and
`pnl/` as the features that need them land.

---

## Development Philosophy — Lazy Dev by Ponytail

> Build less, reuse more, automate repetitive work, and make the boring path reliable.

That means:

- modular monolith first,
- no unnecessary microservices,
- no Kubernetes until needed,
- prefer proven libraries,
- automate tests and deployment,
- keep interfaces narrow,
- use configuration instead of magic numbers,
- avoid speculative abstractions,
- optimize for maintainability.

“Lazy” means reducing unnecessary work — **not reducing engineering quality**.

---

## Safety Philosophy

The strategy engine may propose a trade, but the risk engine has final authority.

```text
Strategy says BUY
       ↓
Risk Guard
       ├── REJECT
       └── ALLOW
              ↓
        Simulation
              ↓
            Sign
```

Hard risk controls must never be bypassed by Telegram commands, AI output, scoring models, or third-party market signals.

---

## PnL Accounting

Track separately:

```text
Realized PnL
Unrealized PnL
Gross PnL
Net PnL
ROI
Drawdown
```

Net PnL should account for:

```text
gross PnL
- gas
- DEX fees
- slippage
= net PnL
```

The system must never report an unrealized price increase as realized cash profit.

---

## Paper Trading First

Before using real funds, the bot should support paper trading with the same:

- discovery,
- analysis,
- scoring,
- risk engine,
- strategy,
- position logic,
- capital recovery logic,
- PnL accounting.

Only signing/broadcasting is replaced by simulated execution.

Recommended rollout:

```text
1. Unit tests
2. Integration tests
3. Testnet
4. Paper trading
5. Small live capital
6. Measure 100–300+ trades
7. Improve strategy
8. Scale only if the measured edge survives costs and drawdown
```

---

## Performance Metrics

Track:

```text
Win rate
Average win
Average loss
Expectancy / trade
Profit factor
Net PnL
ROI
Maximum drawdown
Trade count
Gas cost
Fee cost
Slippage
Capital recovery rate
Runner contribution
Signal accuracy
```

A strategy that earns more while taking disproportionate tail risk is not automatically an improvement.

---

## Configuration

Create `.env.example` and never commit production secrets.

```env
APP_ENV=development

RH_CHAIN_ID=4663
RH_RPC_URL=
RH_WS_URL=

POSTGRES_URL=
REDIS_URL=
Asynq_URL=

TELEGRAM_BOT_TOKEN=
TELEGRAM_ALLOWED_USER_IDS=

BOT_WALLET_ADDRESS=
BOT_WALLET_SIGNER_REF=

STOP_LOSS_PERCENT=5
CAPITAL_RECOVERY_PERCENT=100

MAX_POSITION_PERCENT=5
MAX_OPEN_POSITIONS=5
DAILY_LOSS_LIMIT_PERCENT=10

MAX_SLIPPAGE_BPS=
MIN_LIQUIDITY_USD=
MIN_SCORE=
```

---

## Local Development

### Prerequisites

- Go 1.26+
- Docker and Docker Compose
- Git
- An RPC endpoint (the public one works for development; see below)

Telegram and a wallet are not needed yet — those arrive in Phase 2 and Phase 3.

### Quick start

```bash
cp .env.example .env     # then edit if ports 5432/6379/8080/8081 are taken
make up                  # postgres + redis + asynqmon, then migrations
make test                # unit tests, no external dependencies
make run-api             # http://localhost:8080/readyz
make run-worker          # in another shell
```

`make help` lists every target.

### Ports

If a port is already in use on your machine, set it in `.env` — both `make` and
`docker compose` read that file, so they stay in sync:

```env
POSTGRES_PORT=55432
REDIS_PORT=56379
ASYNQMON_PORT=8091
POSTGRES_URL=postgres://hoodalpha:hoodalpha@localhost:55432/hoodalpha?sslmode=disable
REDIS_ADDR=localhost:56379
```

### Endpoints

| URL | Purpose |
|---|---|
| `http://localhost:8080/healthz` | Liveness. Dependency-free on purpose: a database blip must not make an orchestrator kill a process that is correctly refusing to trade. |
| `http://localhost:8080/readyz` | Readiness. Probes Postgres, Redis, chain RPC and the websocket subscription. Returns 503 when any is down. |
| `http://localhost:8080/version` | Build metadata. |
| `http://127.0.0.1:8081` | Asynqmon. Bound to loopback because it has no authentication of its own. |

### Migrations

```bash
make migrate-up        # apply pending
make migrate-down      # roll back exactly one
make migrate-status    # what is applied, what is pending
make db-recreate       # roll back to zero, then rebuild (development only)
```

Migrations are embedded in the binary, so a container migrates itself without
the repository being present. `migrate reset` refuses to run when
`APP_ENV=production`.

### RPC configuration

```env
RH_CHAIN_ID=4663
RH_RPC_URL=https://rpc.mainnet.chain.robinhood.com
RH_WS_URL=
```

The public endpoint is rate-limited and not recommended for production; the
official docs recommend Alchemy (`https://robinhood-mainnet.g.alchemy.com/v2/{API_KEY}`,
`wss://...` for the websocket). `RH_WS_URL` is optional — without it the bot
still reads the chain over HTTP, it just loses push-based head updates.

The chain ID is verified at startup. Connecting to the wrong network aborts the
process rather than producing balances that silently refer to another chain.

### Telegram setup

```env
TELEGRAM_BOT_TOKEN=<from @BotFather>
TELEGRAM_ALLOWED_USER_IDS=123456789      # your numeric ID, from @userinfobot
TELEGRAM_MINIAPP_URL=                    # optional
TELEGRAM_INITDATA_TTL=15m
TELEGRAM_RATE_LIMIT=20
TELEGRAM_RATE_WINDOW=1m
```

```bash
make run-bot                              # locally
docker compose --profile telegram up -d   # or as a container
```

Leaving the token blank is supported: `api` and `worker` run without it, and the
Mini App routes are then not mounted at all rather than exposed and unusable.

`TELEGRAM_ALLOWED_USER_IDS` is required whenever a token is set, and startup
fails without it. An empty allowlist would answer nobody, which is
indistinguishable from a broken deployment — and defaulting to open on a process
that will eventually hold funds is not an acceptable failure mode.

### Security model

| Control | Behaviour |
|---|---|
| **Identity** | Telegram numeric user ID, never the username. Usernames are mutable and reusable, so authorizing on one would let a released username inherit access. The database mirrors this: `telegram_id` is the primary key, `username` is a nullable display cache with no unique constraint. |
| **Allowlist** | Closed. Unknown IDs are refused before any row is written, so an unauthorized user cannot populate the database by sending messages. Empty means nobody. |
| **Suspension** | An account can be suspended in the database to revoke access without editing configuration and restarting. |
| **Rejection replies** | One terse "Not authorized." Nothing about whether the command exists, who is allowed, or why — a rejection is not a debugging aid for an attacker. |
| **Mini App** | Treated as an untrusted client. Only the signed `initData` is verified; `initDataUnsafe` is never read. Identity comes from the verified payload, so the allowlist is checked against a signed user ID rather than a claimed one. |
| **initData verification** | HMAC-SHA256 against a key derived from the bot token, in constant time, plus an `auth_date` TTL and a future-date bound with small clock-skew tolerance. Ed25519 third-party verification is available for a component that must not hold the token. |
| **Replay protection** | Redis `SETNX`, so a captured payload is refused on second use even if it reaches a different replica. Fails **closed**: if Redis is unavailable, the request is refused rather than silently proceeding with replay protection disabled. |
| **Credential handling** | `initData` is accepted from headers only, never the query string — URLs end up in proxy logs and browser history. |
| **Rate limiting** | Fixed window per user, in Redis. Fails **open**: a Redis outage must not lock the operator out of their own control plane, and the allowlist still gates access independently. |
| **Ownership** | Checked server-side on every wallet read and write. A client can send any wallet ID; "not yours" and "does not exist" return the same 404 so the response is not an enumeration oracle. |
| **Policy limits** | Validated in the use case, in the repository, and by database `CHECK` constraints. A limit that bounds real money is asserted at all three layers, so no single bypassed path can widen it. |
| **Trading flag** | `trading_enabled` defaults to false and linking a wallet never sets it, regardless of configured defaults. Enabling it is always a separate, audited decision. |
| **Key material** | Never stored, and there is no column for it. `/connect` additionally refuses input shaped like a private key or BIP-39 mnemonic before it is parsed, logged or stored, and tells the user to rotate the wallet. |
| **Audit trail** | Onboarding, authorization failures, rate limiting, every command, Mini App auth and rejections, wallet linking and verification, and policy changes — with before/after values, because during an incident the question is always which limit moved. Secrets are never written to it. |
| **Error messages** | Handler errors are logged, not echoed. An error string can carry a connection string or an internal path. |

### Tests

```bash
make test               # unit tests only, no infrastructure needed
make test-integration   # against real Postgres, Redis, Asynq and RPC
make test-all
```

Integration tests are not mocked. Each one skips unless its endpoint is
configured, so `go test ./...` stays green on a machine without Docker:

| Variable | Enables |
|---|---|
| `TEST_POSTGRES_URL` | database, migration and pg_partman tests |
| `TEST_REDIS_ADDR` | Redis, Asynq and Asynqmon tests |
| `TEST_RH_RPC_URL` | chain RPC tests |
| `TEST_RH_WS_URL` | websocket subscription and reconnect tests |

`make test-integration` sets these from your `.env`. Websocket tests need a real
`wss://` JSON-RPC endpoint; the public `feed.mainnet.chain.robinhood.com` is a
sequencer feed, not JSON-RPC, so use a provider endpoint or a local node
(`anvil --chain-id 4663 --block-time 2`).

---

## Security Checklist

- [ ] Dedicated bot wallet
- [ ] Main wallet private key is not stored by the bot
- [ ] Telegram user IDs are allowlisted
- [ ] Telegram bot token is stored as a secret
- [ ] Mini App `initData` is validated server-side
- [ ] Command replay protection
- [ ] Dangerous commands require confirmation
- [ ] Deterministic risk engine
- [ ] Position limits enforced server-side
- [ ] Daily loss limit enforced
- [ ] Transaction simulation
- [ ] Duplicate-order prevention
- [ ] Pending transactions reconciled after restart
- [ ] PnL accounting tested
- [ ] Paper trading exercised
- [ ] Kill switch tested
- [ ] VPS backups configured
- [ ] Logs contain no secrets

---

## Project Status

**Experimental / Early Development — Phases 0 through 3 implemented.**

What runs today: configuration, logging, health checks, PostgreSQL with
migrations and pg_partman, Redis, Asynq with Asynqmon, a read-only Robinhood
Chain client with websocket head subscription and reconnect, the Telegram
control plane (`/start`, `/status`, `/health`, `/connect`) with an allowlist and
audit trail, the Mini App backend with server-side `initData` verification, and
wallet onboarding with per-wallet policies.

What does not exist yet: token discovery, analysis, scoring, strategy, risk
engine, and execution. There is **no signer and no broadcast path** in the
codebase — the chain client is read-only by construction and the schema has no
column for key material, so this build cannot move funds.

The architecture and specification remain ahead of the implementation.

Expect:

- incomplete modules,
- changing APIs,
- strategy iteration,
- temporary adapters,
- limited test coverage during early development.

This repository should not be treated as production-safe until security, execution, accounting, and recovery tests have been completed.

---

## Roadmap

### Phase 0 — Foundation ✅

- [x] Go module
- [x] Clean Architecture skeleton
- [x] configuration loading + validation
- [x] structured logging, build metadata
- [x] graceful shutdown, context propagation
- [x] Docker Compose (postgres, redis, asynqmon, api, worker, migrate)
- [x] PostgreSQL 17 + connection pooling + health
- [x] versioned migrations with tested rollback
- [x] pg_partman 5.5.0, non-superuser maintenance role
- [x] Redis
- [x] Asynq client/server, queues, retry, idempotency keys
- [x] Asynqmon
- [x] CI (fmt, vet, unit, integration, image build)
- [x] health endpoints

### Phase 1 — Chain ✅

- [x] Robinhood Chain RPC client (go-ethereum)
- [x] chain ID verification at startup
- [x] bounded retries, exponential backoff, timeouts
- [x] WebSocket head subscription with reconnect
- [x] block listener + persisted sync state
- [x] balance reads
- [x] transaction and receipt reads
- [x] event log retrieval
- [x] address/hash validation

### Phase 2 — Telegram ✅

- [x] `/start`, `/status`, `/health`, `/connect`, `/help`
- [x] authorization on Telegram numeric user ID, closed allowlist
- [x] command router with rate limiting and audit logging
- [x] alert delivery via the notifications queue

### Phase 3 — Wallet ✅

- [x] Mini App backend with server-side `initData` verification
- [x] replay protection and auth-date TTL
- [x] wallet linking, on-chain verification, status state machine
- [x] per-wallet policy persisted and enforced server-side

### Phase 4 — Discovery

- [ ] new token detection
- [ ] new pool detection
- [ ] candidate queue
- [ ] deduplication

### Phase 5 — Analysis

- [ ] token security
- [ ] liquidity
- [ ] market data
- [ ] insider graph
- [ ] social/project analysis
- [ ] scoring

### Phase 6 — Strategy

- [ ] entry rules
- [ ] position sizing
- [ ] SL -5%
- [ ] capital recovery +100%
- [ ] runner
- [ ] emergency exits

### Phase 7 — Paper Trading

- [ ] simulated orders
- [ ] simulated fills
- [ ] PnL
- [ ] performance analytics
- [ ] strategy evaluation

### Phase 8 — Live Trading

- [ ] signer
- [ ] DEX adapter
- [ ] transaction simulation
- [ ] execution
- [ ] reconciliation

### Phase 9 — Hardening

- [ ] kill switch
- [ ] backups
- [ ] monitoring
- [ ] recovery
- [ ] security review
- [ ] operational runbooks

---

## Contributing

Contributions are welcome.

Useful areas include:

- Robinhood Chain adapters,
- DEX integrations,
- security analyzers,
- wallet graph analysis,
- market-data adapters,
- paper-trading infrastructure,
- backtesting,
- PnL/accounting,
- testing,
- observability,
- documentation.

Before opening a PR:

1. Explain the problem.
2. Explain the proposed change.
3. Include tests where practical.
4. Avoid unnecessary dependencies.
5. Keep financial/risk behavior explicit.
6. Do not introduce secrets or private keys.

For strategy changes, include evidence where possible:

```text
before
after
trade count
net PnL
drawdown
profit factor
slippage sensitivity
```

---

## Security Issues

Do not publicly post exploitable security issues, private keys, bot tokens, or wallet-draining vulnerabilities in a normal issue.

Use a private security-reporting mechanism once configured for the repository.

---

## License

**TBD**

Add an explicit open-source license before accepting external contributions. MIT or Apache-2.0 are reasonable candidates depending on project goals.

---

## Useful Documentation

### Robinhood Chain

- [Robinhood Chain Documentation](https://docs.robinhood.com/chain/)
- [Connecting to Robinhood Chain](https://docs.robinhood.com/chain/connecting/)
- [Add Robinhood Chain to a Wallet](https://docs.robinhood.com/chain/add-network-to-wallet/)
- [Deploy Smart Contracts](https://docs.robinhood.com/chain/deploy-smart-contracts/)
- [Chainlink Data Streams](https://docs.robinhood.com/chain/data-streams/)

### Telegram

- [Telegram Bot API](https://core.telegram.org/bots/api)
- [Telegram Mini Apps](https://core.telegram.org/bots/webapps)

---

## Philosophy

This project uses Asynq for background jobs. It does not use Asynq, RabbitMQ, or Kafka as the background-job layer.

The goal is not:

```text
make money every day
```

The goal is:

```text
SURVIVE
    ↓
COLLECT DATA
    ↓
FIND EDGE
    ↓
EXECUTE CONSISTENTLY
    ↓
PROTECT CAPITAL
    ↓
MEASURE
    ↓
IMPROVE
    ↓
SCALE ONLY WITH EVIDENCE
```

If the strategy is wrong, the system should tell us.

If the strategy works, the data should demonstrate it.

---

## Architecture Source Files

Keep both the editable architecture source and rendered image:

```text
docs/
├── architecture.excalidraw
└── architecture.svg
```

The SVG is displayed in this README; the `.excalidraw` file remains editable.
