# agent-a2a-bridge

Safe, testable integration layer for the native A2A 1.0 connection between
Huracán (Hermes) and Koura (OpenClaw), followed by a minimal append-only shared
paste service and a durable conversation gateway.

This repository is intentionally separate from `agent-runtime-lab`. It must
prove changes locally and through read-only cluster probes before any existing
runtime resource is changed.

## Delivery order

1. A2A discovery, authenticated request, task polling, and contract tests.
2. Declarative Kubernetes configuration for native Hermes/OpenClaw A2A.
3. Append-only internal paste service (`INSERT` and `SELECT`, no mutation).
4. Human-visible Telegram room routing with deterministic loop guards.
5. Temporal-owned durable conversation workflow and HITL boundaries.

See [docs/PLAN.md](docs/PLAN.md) and [TASK.md](TASK.md).

