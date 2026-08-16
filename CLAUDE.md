# CLAUDE.md — Robinhood Chain Autonomous Crypto Trading Bot

## Mission

You are the lead software architect and senior Web3 engineer responsible for building this project from zero to a production-grade personal automated trading system on Robinhood Chain.

Your job is not merely to write code. You own the architecture, engineering decisions, integration strategy, testing strategy, operational safety, observability, and iterative improvement of the system.

The goal is to build a **small-capital, personal-use, autonomous trading bot** that:

1. connects/authorizes a user's EVM wallet,
2. discovers potentially tradable tokens automatically,
3. analyzes token safety, market structure, liquidity, insider/wallet behavior, and project/community legitimacy,
4. scores opportunities,
5. automatically enters and exits positions when all hard risk rules pass,
6. runs 24/7 on a VPS,
7. reports status, trades, warnings, and PnL to Telegram,
8. continuously records outcomes so the strategy can be improved from real data.

Do not treat profitability as guaranteed. Optimize the system for **positive expectancy, disciplined risk, capital preservation, and measurable improvement**.

---

# 1. Core Product Concept

The final product is an autonomous trading engine, not a manual scanner.

### User experience

```text
/start
   ↓
Connect / authorize wallet
   ↓
Verify Robinhood Chain
   ↓
Configure trading policy
   ↓
START AUTO TRADING
   ↓
Bot continuously discovers tokens
   ↓
Analyze token
   ↓
Score opportunity
   ↓
Risk Guard
   ↓
BUY / SKIP
   ↓
Monitor position
   ├── -5% → STOP LOSS / EXIT
   ├── +100% → CAPITAL RECOVERY
   │             ├── recover initial capital
   │             └── leave remaining profit as RUNNER
   └── emergency risk event → EXIT
   ↓
PnL + accounting
   ↓
Telegram report
   ↓
Repeat
```

The bot must be able to run without a human manually supplying token contract addresses.

Manual scanning remains available:

```text
/scan 0xTOKEN
```

but it is a secondary capability.

---

# 2. Trading Philosophy

## 2.1 Capital Recovery + Runner

Do NOT use the old fixed `TP = +20%` model.

The current strategy policy is:

```text
STOP LOSS       = -5% from entry
CAPITAL RECOVERY = +100%
RUNNER           = profit after capital recovery
```

Example:

```text
Initial position = $10

Price reaches +100%
Position value ≈ $20

Sell enough to recover the initial $10 capital.

Remaining position ≈ $10
This becomes the runner.
```

The runner is not automatically capped at +100%, +200%, etc.

The runner may continue to appreciate until:

- trailing/risk logic exits,
- liquidity deteriorates,
- insider/liquidity/security emergency triggers,
- strategy exit conditions trigger.

The capital recovery operation must account for:

- actual fill price,
- gas,
- DEX fees,
- slippage,
- token decimals,
- partial fills where applicable.

Never assume `+100%` means exactly 2x net cash after costs.

---

# 3. Insider Analysis Policy

Insider analysis is a **risk intelligence signal**, not a fixed take-profit trigger.

Normal insider accumulation does not force an exit.

Potential hard-risk events can still force an emergency exit:

- coordinated insider dumping,
- deployer wallet exiting aggressively,
- suspicious wallet cluster selling together,
- LP removal / liquidity drain,
- abnormal transfer patterns,
- contract permission changes,
- extreme price impact / inability to exit.

The system must separate:

```text
INSIDER SIGNAL
    ↓
risk adjustment

from

EXIT CONDITION
    ↓
hard risk / strategy rule
```

Do not sell a winning position merely because an insider score changed slightly.

---

# 4. “Lazy Dev by Ponytail” Engineering Philosophy

Treat this as the project's operating philosophy, not as a formal external methodology.

Interpret it as:

> Build less, reuse more, automate repetitive work, avoid premature complexity, and make the boring path extremely reliable.

Rules:

1. Prefer boring, well-supported libraries over custom infrastructure.
2. Do not build a system manually when an audited/proven protocol or service solves it.
3. Avoid Kubernetes until there is a real operational reason.
4. Avoid microservices unless a boundary is justified by scaling, isolation, ownership, or fault containment.
5. Prefer a modular monolith first.
6. Automate tests, migrations, formatting, linting, deployment, backups, and health checks.
7. Add abstractions only when they remove meaningful complexity.
8. Keep interfaces narrow.
9. Do not create speculative “future” packages that have no current consumer.
10. Every feature should have a measurable purpose.
11. Make failure behavior explicit.
12. Prefer deterministic calculations for trading/risk logic.
13. Never hide business rules inside framework glue.
14. Minimize operational moving parts.
15. Build the smallest correct version first, then iterate using observed production data.

“Lazy” does NOT mean careless.

The intended result is:

```text
minimum moving parts
+
maximum automation
+
strong safety boundaries
+
easy maintenance
```

---

# 5. Architecture Principle — Clean Architecture

Use Clean Architecture with clear dependency direction.

Recommended shape:

```text
interface adapters
        ↓
application/use cases
        ↓
domain
        ↑
infrastructure adapters
```

The domain must not depend on:

- Telegram SDK,
- PostgreSQL driver,
- Redis,
- RPC provider,
- HTTP framework,
- DEX SDK,
- vendor-specific security API.

The domain should know about concepts such as:

- Token
- Wallet
- MarketSnapshot
- Signal
- RiskScore
- Order
- Trade
- Position
- PnL
- TradingPolicy
- ExitDecision

Infrastructure implements interfaces required by use cases.

---

# 6. Recommended Technology Stack

## Backend

- Go
- Go modules
- structured logging
- context-aware APIs
- idiomatic error handling

## HTTP/API

Use a lightweight Go framework or standard `net/http`.

Preferred default:

- `chi` OR standard library

Do not add a framework only because it is popular.

## Blockchain

Preferred:

- `go-ethereum`
- standard EVM JSON-RPC/WebSocket
- ABI bindings where useful

TypeScript may be used where a specific Web3 library or ecosystem integration is significantly better than Go.

Do not force one language where another is clearly superior.

## Database

- PostgreSQL 17

Use:

- SQL migrations
- indexes based on actual query patterns
- partitioning only when justified by data volume
- transactions for trading/accounting invariants

## Cache / coordination

- Redis
- Asynq (`github.com/hibiken/asynq`)
- Asynqmon (`github.com/hibiken/asynqmon`)

Use Redis for:

- short-lived cache,
- distributed locks,
- rate limiting,
- ephemeral state.

Do not make Redis the source of truth for financial records.

## Background Jobs — Asynq

Use the official Asynq project only:

- Repository: https://github.com/hibiken/asynq
- Go module: `github.com/hibiken/asynq`

Use Asynq as the background task queue for this project. Redis is the backing store. PostgreSQL remains the source of truth for persistent/financial state.

Use Asynq for asynchronous work such as token discovery, analysis, security scans, market refreshes, insider analysis, social analysis, notifications, transaction monitoring, position reconciliation, PnL updates, scheduled maintenance, and health/reconciliation jobs.

Suggested task names:

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

Tasks must be retry-safe and idempotent where practical. Never assume exactly-once execution. Use task IDs/idempotency keys and persisted state transitions for financial operations.

Do not use Asynq, Asynq, RabbitMQ, Kafka, or another queue system for the current architecture.

## Asynqmon

Use the official Asynqmon project only:

- Repository: https://github.com/hibiken/asynqmon

Asynqmon is the operational monitoring UI for Asynq. Use it to inspect pending, active, scheduled, retried, failed, and archived tasks plus queue state. Run it in Docker Compose for local development. Do not expose it publicly in production without authentication/network restrictions.

Before implementation, inspect the official repositories and verify the current compatible API/version for the project Go version. Pin versions in `go.mod` and infrastructure configuration.

## Telegram

- Telegram Bot API
- Telegram Mini App for wallet authorization / configuration UI where needed

Telegram is the primary control plane and alerting interface.

## Frontend

No dashboard is required for MVP.

Use a Telegram Mini App only for flows that are awkward or unsafe inside plain Telegram messages, especially wallet connection / authorization and policy configuration.

A standalone public website is not a requirement.

## Deployment

- Docker
- Docker Compose initially
- VPS
- GitHub Actions

Do not introduce Kubernetes for this personal MVP.

## Observability

Minimum:

- structured logs
- metrics
- health endpoint
- heartbeat
- error alerts to Telegram

Optional later:

- Prometheus
- Grafana
- OpenTelemetry
- Loki

---

# 7. Robinhood Chain Baseline

Use official documentation as the source of truth for network configuration.

Current baseline:

- Mainnet chain ID: `4663`
- Testnet chain ID: `46630`
- Native gas token: ETH
- Mainnet Blockscout: `https://robinhoodchain.blockscout.com`
- Official public RPC exists but is rate-limited and should not be treated as production infrastructure.
- Alchemy is currently the recommended infrastructure provider in the official docs.
- QuickNode, Blockdaemon, dRPC, and Validation Cloud are also listed as providers.
- Robinhood Chain is EVM compatible.
- Standard tooling such as Foundry, Hardhat, ethers.js, viem, and Wagmi is supported.
- Robinhood Chain supports ERC-4337 account abstraction, including programmable wallet, batching, gas sponsorship, and session-key use cases.
- Chainlink Data Streams is available on Robinhood Chain for fast market data use cases.

Official references:

- https://docs.robinhood.com/chain/
- https://docs.robinhood.com/chain/connecting/
- https://docs.robinhood.com/chain/add-network-to-wallet/
- https://docs.robinhood.com/chain/deploy-smart-contracts/
- https://docs.robinhood.com/chain/data-streams/

Never hardcode network assumptions without checking the official docs when network behavior may have changed.

---

# 8. Core Modules

Start as a modular monolith with clear packages.

Suggested structure:

```text
crypto-trading-bot/
├── cmd/
│   ├── api/
│   ├── bot/
│   ├── scanner/
│   └── worker/
│
├── internal/
│   ├── domain/
│   ├── application/
│   ├── token/
│   ├── discovery/
│   ├── market/
│   ├── security/
│   ├── insider/
│   ├── social/
│   ├── scoring/
│   ├── strategy/
│   ├── risk/
│   ├── wallet/
│   ├── execution/
│   ├── position/
│   ├── pnl/
│   ├── telegram/
│   ├── chain/
│   ├── persistence/
│   └── observability/
│
├── migrations/
├── contracts/
├── deployments/
├── tests/
├── docs/
├── docker-compose.yml
└── Makefile
```

Keep boundaries practical.

If a package becomes a meaningless wrapper around another package, simplify it.

---

# 9. Token Discovery

The system must discover tokens automatically.

Primary discovery sources may include:

- new contract activity,
- new DEX pool creation,
- swaps / liquidity events,
- indexed token lists,
- ecosystem-specific APIs,
- configurable third-party market-data sources.

Do not assume every newly deployed token is tradable.

Every candidate must pass a filtering pipeline.

Example:

```text
NEW CANDIDATE
    ↓
is token?
    ↓
has liquidity?
    ↓
has active trading?
    ↓
minimum liquidity?
    ↓
basic contract safety?
    ↓
market sanity?
    ↓
deep analysis?
```

Deduplicate aggressively.

Do not repeatedly analyze the same token every second.

Use:

- token cache,
- analysis TTL,
- event-driven refresh,
- cooldowns.

---

# 10. Token Security Analyzer

Analyze at minimum:

- contract verification status,
- implementation/proxy structure,
- owner/admin privileges,
- mint capabilities,
- pause/freeze capabilities,
- blacklist/whitelist behavior,
- transfer restrictions,
- buy/sell tax where observable,
- fee mutation permissions,
- trading enable/disable controls,
- suspicious external calls,
- LP ownership/locking where observable,
- holder concentration,
- deployer behavior.

A security failure may be a **hard reject**.

Examples:

```text
honeypot suspected → REJECT
cannot sell safely → REJECT
dangerous owner privilege → REJECT / HIGH RISK
liquidity too low → REJECT
```

Never let a high “overall score” override a hard contract-safety failure.

---

# 11. Market Intelligence

Collect and normalize:

- price,
- market cap estimate,
- liquidity,
- volume,
- buy/sell volume,
- volatility,
- price impact,
- liquidity-to-market-cap ratio,
- holder distribution,
- pool age,
- trading frequency,
- depth.

“Stable market cap” is NOT a single metric.

Evaluate the relationship between:

```text
market cap
liquidity
volume
volatility
price impact
holder concentration
```

Reject misleading situations such as:

```text
high market cap
+
tiny liquidity
+
artificial volume
```

---

# 12. Insider / Wallet Intelligence

Build a wallet graph.

Track:

- deployer,
- deployer funding source,
- first buyers,
- same-funding clusters,
- early entry groups,
- whale wallets,
- synchronized wallets,
- coordinated buys,
- coordinated sells,
- suspicious wallet transfers,
- LP interactions.

Create:

```text
insiderRiskScore
```

and explain WHY.

Do not output only a number.

Every signal should contain evidence such as:

```text
Wallet cluster:
5 wallets funded by same source

Activity:
4 bought within first N blocks

Current:
3 wallets started selling simultaneously
```

---

# 13. Website / Community / Project Intelligence

Evaluate when data is available:

- official website,
- documentation,
- social links,
- GitHub,
- team/project consistency,
- contract address consistency,
- social activity,
- community age,
- suspicious engagement patterns,
- dead links,
- copied metadata.

Community legitimacy must be treated as a confidence signal, never as proof of safety.

A real website does not make a token safe.

A missing website does not automatically prove a scam.

---

# 14. Strategy Engine

The strategy engine determines whether a candidate is worth trading.

Separate:

1. eligibility,
2. scoring,
3. position sizing,
4. entry,
5. exit.

Do not mix these into one giant function.

Example concept:

```text
securityScore
marketScore
insiderScore
communityScore
liquidityScore
momentumScore
confidence
```

Then:

```text
candidate = strategy.evaluate(...)
```

Return an explicit decision:

```text
SKIP
WATCH
BUY
EXIT
EMERGENCY_EXIT
```

---

# 15. Risk Engine

Risk rules are absolute.

The strategy is not allowed to override them.

Initial policy:

```text
stopLossPercent        = 5%
capitalRecoveryAt      = 100%
maxPositionPercent     = 5%
maxOpenPositions       = 5
dailyLossLimitPercent  = 10%
```

These are configuration values, not magic constants buried in code.

The risk engine must enforce:

- maximum position size,
- maximum daily loss,
- maximum token exposure,
- maximum open positions,
- liquidity minimum,
- slippage limit,
- price impact limit,
- cooldown after loss,
- duplicate-order prevention,
- emergency pause.

---

# 16. Capital Recovery Engine

Implement a dedicated use case.

Input:

```text
entry position
current mark/fill
initial capital
fees
gas
realized recovery
```

When the position reaches the recovery threshold, calculate how much quantity must be sold to recover the original capital net of relevant costs.

Do NOT assume:

```text
sell 50% = recover capital
```

That is only true in a frictionless +100% scenario.

Use actual execution data.

The capital recovery event must be idempotent.

If the bot restarts after detecting +100%, it must not recover capital twice.

Persist:

- recovery target,
- recovery requested,
- recovery transaction,
- recovery fill,
- recovered amount,
- runner amount,
- recovery completed timestamp.

---

# 17. Runner / Trailing Logic

After capital recovery:

```text
runner = residual position
```

The runner can continue.

Potential controls:

- trailing stop,
- liquidity deterioration,
- insider emergency signal,
- volatility exit,
- strategy invalidation,
- maximum holding time if justified.

Do not hard-code a trailing percentage unless data supports it.

Start conservatively and make it configurable.

---

# 18. Wallet Architecture

Never expose the user's main wallet private key to Telegram.

Preferred model:

```text
USER / OWNER WALLET
        ↓
authorization / funding
        ↓
DEDICATED BOT WALLET
        ↓
policy-bound signer
        ↓
Robinhood Chain
```

For future hardening, evaluate:

- ERC-4337,
- programmable wallets,
- session keys,
- spending limits,
- allowed target contracts,
- batched transactions,
- gas sponsorship.

Private keys must not be committed to git.

Never store production private keys in plaintext files.

Use:

- secret manager,
- encrypted keystore,
- or another hardened signer.

---

# 19. Telegram Control Plane

Required commands:

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

Commands must have authorization.

Use Telegram user/chat IDs.

Do not trust usernames as the primary identity mechanism.

Dangerous commands require confirmation:

```text
/sellall
/stop_auto
/change_limits
```

All commands should be logged.

---

# 20. Telegram Mini App

Use a Mini App for:

- wallet connection / authorization,
- chain verification,
- risk policy form,
- trading status,
- wallet address display,
- start/stop trading,
- compact portfolio UI.

Do not use Telegram chat messages for private-key handling.

Do not upload secrets into Telegram.

The Mini App should be treated as an untrusted UI client.

All actual authorization and validation happen server-side.

---

# 21. Auto-Trading State Machine

Use explicit bot state:

```text
DISCONNECTED
AUTHORIZED
READY
RUNNING
PAUSED
STOPPING
STOPPED
ERROR
```

Position state:

```text
DISCOVERED
ELIGIBLE
BUY_PENDING
OPEN
RECOVERY_PENDING
RECOVERY_COMPLETED
RUNNER
EXIT_PENDING
CLOSED
FAILED
```

Order state:

```text
CREATED
VALIDATING
SIMULATING
APPROVED
SUBMITTED
PENDING
FILLED
PARTIALLY_FILLED
REVERTED
CANCELLED
```

Never represent complex trading state as a few booleans.

---

# 22. Execution Engine

Trade flow:

```text
SIGNAL
  ↓
RISK CHECK
  ↓
QUOTE
  ↓
SIMULATION
  ↓
POSITION SIZE
  ↓
DUPLICATE CHECK
  ↓
SIGN
  ↓
BROADCAST
  ↓
MONITOR
  ↓
FILL
  ↓
POSITION UPDATE
  ↓
PNL UPDATE
```

Always record:

- tx hash,
- chain ID,
- nonce,
- gas parameters,
- quote,
- expected output,
- actual output,
- slippage,
- fees,
- block number,
- timestamps,
- failure reason.

Every operation must be idempotent.

---

# 23. Data Model

At minimum plan for:

```text
users
telegram_users

wallets
wallet_policies

tokens
token_contracts
token_holders

liquidity_pools
market_snapshots

wallet_entities
wallet_edges
wallet_events
insider_signals

project_metadata
social_profiles

risk_scores
signals

orders
trades
positions

position_events
capital_recovery_events

pnl_snapshots
daily_performance

alerts
audit_logs
```

Financial data must be immutable where appropriate.

Do not “edit history” to fix accounting bugs.

Prefer compensating records.

---

# 24. PnL Accounting

Net PnL:

```text
Net PnL
= realized gains
- realized losses
- gas
- DEX fees
- slippage
- other execution costs
```

Track separately:

- realized PnL,
- unrealized PnL,
- gross PnL,
- net PnL,
- ROI,
- drawdown,
- win rate,
- average win,
- average loss,
- profit factor,
- expectancy per trade.

Never report unrealized gains as cash profit.

---

# 25. Profit Optimization / Improvisation

You are explicitly allowed to improve the strategy if the change is justified by data and tests.

You may introduce improvements such as:

- better candidate filtering,
- dynamic position sizing,
- liquidity-weighted sizing,
- volatility-adjusted thresholds,
- adaptive cooldowns,
- better wallet clustering,
- better market-cap normalization,
- dynamic trailing logic,
- time-of-day filters if evidence supports them,
- execution routing improvements,
- transaction simulation,
- event-driven indexing,
- improved opportunity ranking,
- online/offline feature analysis,
- backtesting infrastructure,
- paper trading,
- parameter optimization.

However:

**Never optimize only for raw backtest profit.**

Evaluate at least:

```text
net return
max drawdown
profit factor
expectancy
win/loss distribution
trade count
slippage sensitivity
gas sensitivity
out-of-sample behavior
```

Avoid overfitting.

If a strategy improvement makes backtest profit larger but significantly increases tail risk, do not silently ship it.

---

# 26. AI / ML Policy

Do not begin with an LLM making direct buy/sell decisions.

First build deterministic:

- data pipeline,
- security checks,
- features,
- scoring,
- strategy,
- risk engine,
- execution,
- accounting.

AI/ML can later be used for:

- anomaly detection,
- project/social classification,
- feature generation,
- clustering,
- ranking,
- regime detection,
- offline research.

The final risk guard remains deterministic.

LLM output must never bypass:

```text
hard security reject
position limits
daily loss limit
slippage limit
liquidity minimum
```

---

# 27. Reliability Requirements

The system runs 24/7.

It must survive:

- VPS restart,
- bot restart,
- process crash,
- RPC timeout,
- websocket reconnect,
- duplicated event,
- delayed transaction receipt,
- chain reorg where relevant,
- database reconnect,
- Telegram API timeout,
- third-party API outage,
- stale market data,
- DEX quote failure.

On restart:

```text
load open positions
load pending orders
reconcile chain state
reconcile balances
recompute derived state
resume safely
```

Never blindly start issuing new trades before reconciliation.

---

# 28. Safety / Kill Switch

Immediate trading halt triggers include:

- daily loss threshold reached,
- repeated transaction failures,
- abnormal slippage,
- RPC inconsistency,
- stale market data,
- liquidity collapse,
- detected contract/security change,
- suspicious deployer/LP activity,
- internal state corruption.

Kill switch behavior:

```text
STOP NEW ENTRIES
      ↓
continue monitoring
      ↓
notify Telegram
      ↓
optionally exit existing positions according to policy
```

The default should NOT be “panic sell everything” unless the event specifically warrants it.

---

# 29. Security

Threat model at minimum:

- Telegram account compromise,
- Telegram bot token compromise,
- VPS compromise,
- private key compromise,
- malicious token contract,
- malicious DEX router,
- RPC manipulation/outage,
- fake market data,
- oracle/data provider failure,
- replayed command,
- duplicate order,
- nonce race,
- compromised third-party API.

Apply:

- least privilege,
- allowlists,
- spending caps,
- idempotency,
- rate limits,
- command authorization,
- secret isolation,
- audit logging,
- transaction simulation,
- safe defaults.

---

# 30. Testing Strategy

Before real money:

## Unit tests

Test:

- scoring,
- position sizing,
- SL,
- capital recovery,
- runner,
- PnL,
- fee accounting,
- risk policies.

## Integration tests

Test:

- PostgreSQL,
- Redis,
- Asynq,
- Telegram,
- RPC,
- DEX adapters.

## Chain tests

Use Robinhood Chain testnet where possible.

Current testnet chain ID:

```text
46630
```

## Simulation / paper trading

Build a paper-trading mode before live execution.

It should process the exact same strategy/risk pipeline while replacing signing/broadcasting with simulated fills.

---

# 31. Development Order

Build in this order unless evidence suggests a better path:

### Phase 0 — Repository foundation

- Go project
- config
- logging
- Docker Compose
- PostgreSQL
- Redis
- migrations
- CI
- Makefile
- health checks

### Phase 1 — Chain connectivity

- RPC
- websocket
- chain verification
- block listener
- balance reads
- transaction reads

### Phase 2 — Telegram

- `/start`
- authentication
- status
- health
- command framework

### Phase 3 — Wallet onboarding

- Mini App
- wallet authorization
- chain verification
- bot wallet design
- policy persistence

### Phase 4 — Token discovery

- new token candidates
- new pool candidates
- deduplication
- candidate queue

### Phase 5 — Token security

- contract analysis
- holder analysis
- liquidity checks
- hard rejects

### Phase 6 — Market analysis

- price
- liquidity
- volume
- market-cap estimation
- volatility
- price impact

### Phase 7 — Insider analysis

- wallet graph
- clustering
- deployer flows
- early buyers
- coordinated activity

### Phase 8 — Social/community analysis

- project metadata
- website
- docs
- GitHub
- social presence

### Phase 9 — Scoring + strategy

- normalized features
- signal engine
- candidate ranking
- position sizing

### Phase 10 — Paper trading

- virtual orders
- virtual fills
- PnL
- performance analytics

### Phase 11 — Live execution

- dedicated bot wallet
- DEX adapter
- simulation
- signer
- execution
- reconciliation

### Phase 12 — Capital recovery / runner

- +100% recovery trigger
- capital recovery execution
- residual runner
- trailing/risk exit

### Phase 13 — Operational hardening

- monitoring
- alerts
- backups
- recovery
- audit
- VPS deployment

Do not skip paper trading simply because the user wants live trading quickly.

---

# 32. Environment / Configuration

Use a clear config structure.

Example:

```env
APP_ENV=development

RH_CHAIN_ID=4663
RH_RPC_URL=
RH_WS_URL=

POSTGRES_URL=
REDIS_URL=
REDIS_URL=

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

Do not commit real secrets.

Provide `.env.example`, never real production secrets.

---

# 33. Engineering Standards

Every production change should have:

- tests,
- validation,
- useful logs,
- migration if needed,
- documentation where behavior changes.

Prefer:

```text
small diff
clear name
clear ownership
clear failure mode
```

Avoid:

```text
giant service files
god objects
global mutable state
hidden side effects
silent retries
magic numbers
vendor lock-in where unnecessary
```

---

# 34. Observability

At minimum expose:

```text
bot_running
last_block_seen
last_scan_at
last_trade_at

open_positions
daily_pnl
daily_loss
capital
available_balance

signals_detected
signals_rejected
trades_attempted
trades_filled
trades_failed

rpc_errors
telegram_errors
quote_failures

win_rate
profit_factor
drawdown
```

Send high-value alerts to Telegram.

Do not spam Telegram for every internal event.

---

# 35. Operational Budget Assumption

This is a personal bot with small capital.

Optimize the architecture for:

- low monthly infrastructure cost,
- one VPS,
- one primary RPC provider,
- inexpensive/free data sources initially,
- no mandatory paid dashboard,
- no mandatory domain,
- no Kubernetes.

Only add paid providers when the free/cheap path is shown to be insufficient by measured needs.

---

# 36. Architecture Decision Rules

When you face an architectural choice:

1. Prefer simplicity.
2. Prefer standard protocols.
3. Prefer deterministic behavior in financial logic.
4. Prefer interfaces at external boundaries.
5. Prefer replaceable adapters for external providers.
6. Prefer one deployable unit until scaling requires separation.
7. Prefer event-driven behavior where it removes polling or race conditions.
8. Prefer persisted state for anything related to money.
9. Prefer reconciliation over assumptions.
10. Prefer observability over cleverness.

---

# 37. Autonomous Architect Behavior

You have authority to make sensible implementation decisions without repeatedly asking the user for trivial confirmation.

When requirements are ambiguous:

1. infer the safest reasonable default,
2. implement a configurable version,
3. document the assumption,
4. continue.

Do not stop implementation for questions that can be resolved from the architecture.

If the requirement is dangerous, financially unsafe, or technically impossible, explain the constraint and implement the safest viable alternative.

---

# 38. Research Policy

When a fact may have changed, verify current official documentation before coding against it.

Prioritize:

1. official Robinhood Chain docs,
2. official protocol docs,
3. official SDK docs,
4. source repository / specification,
5. reputable third-party docs.

Do not rely on stale assumptions about:

- chain IDs,
- RPC endpoints,
- supported protocols,
- wallet behavior,
- DEX addresses,
- API limits,
- AA/session-key behavior.

For production integrations, pin versions.

---

# 39. Definition of Done

A feature is not done merely because it compiles.

A feature is done when:

- code exists,
- tests exist,
- errors are handled,
- state is persisted correctly,
- logs are useful,
- failure behavior is explicit,
- configuration is documented,
- secrets are safe,
- recovery after restart is considered,
- observability exists,
- the feature works in the intended integration environment.

For trading features, add:

- simulation / paper-trading validation,
- deterministic risk checks,
- transaction reconciliation,
- PnL impact verification.

---

# 40. Final Product Principles

The finished system should feel like:

```text
24/7 autonomous discovery
        +
high-quality token filtering
        +
security intelligence
        +
market intelligence
        +
wallet / insider graph
        +
disciplined risk
        +
automatic execution
        +
capital recovery
        +
runner upside
        +
precise accounting
        +
Telegram control
```

The bot should be **greedy only when the system's measured edge justifies it**.

The bot must never become greedy simply because a position is winning.

Optimize for:

```text
SURVIVE
→ DISCOVER EDGE
→ EXECUTE CONSISTENTLY
→ PROTECT CAPITAL
→ SCALE ONLY WITH EVIDENCE
```

Do not promise profit.

Build the machine that gives the user the best technical chance of discovering and exploiting a repeatable edge while controlling downside.

---

## Current official technical references

Robinhood Chain:
https://docs.robinhood.com/chain/

Connection / RPC:
https://docs.robinhood.com/chain/connecting/

Wallet:
https://docs.robinhood.com/chain/add-network-to-wallet/

Smart contracts:
https://docs.robinhood.com/chain/deploy-smart-contracts/

Data Streams:
https://docs.robinhood.com/chain/data-streams/
