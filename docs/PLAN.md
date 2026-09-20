---
title: Native A2A bridge delivery plan
status: approved-for-bounded-implementation
updated: 2026-09-19
participants: [tuomas, hura, koura]
protocol: A2A-1.0
---

# Native A2A bridge delivery plan

## Verified starting point

- Hermes Agent `0.21.3` contains bidirectional A2A support, currently disabled.
- OpenClaw `2026.9.4` contains its stock A2A 1.0 channel plugin, currently disabled.
- Hermes will serve A2A on port `9900`; OpenClaw serves A2A at `/a2a/v1` on its existing port `18789`.
- The live cluster already has a custom authenticated `/peer-message` proof of concept with deduplication, bounded message size, one in-flight wake, persisted JSONL evidence, and non-recursive auto-replies.
- Existing workloads use distinct ServiceAccounts with `automountServiceAccountToken: false` and no Kubernetes Roles or RoleBindings.
- The native path must be proven before the custom peer sidecars are removed.

## Target topology

```text
Telegram (human UI)
        |
future conversation gateway / Temporal workflow
        |
        +-- A2A 1.0 --> Hura  (Hermes, :9900/)
        |
        +-- A2A 1.0 --> Koura (OpenClaw, :18789/a2a/v1)

Hura <------------- native A2A 1.0 -------------> Koura
                      |
               append-only paste
```

Telegram must not become the reliable agent transport. A2A carries peer tasks;
the future gateway owns human routing, transcript projection, budgets, loop
limits, and approvals. Temporal later makes those controls durable.

## Security invariants

1. Separate high-entropy credentials for Hura-to-Koura and Koura-to-Hura.
2. Credentials come from Kubernetes Secrets and never enter Git or logs.
3. Peer identity is derived from authenticated configuration, never a caller-supplied author field.
4. Agent pods receive no Kubernetes API credential.
5. NetworkPolicy allows only the exact peer-to-peer ports and selectors plus required DNS/provider egress.
6. Agent Cards expose only intended capabilities and contain no internal secrets.
7. A2A input cannot execute operator slash commands or approve privileged actions.
8. One human-originated request has bounded hops, turns, wall time, cost, and concurrent work.
9. Every request has message, conversation/context, correlation, and idempotency identifiers.
10. Native A2A runs in parallel with the existing bridge until restart, negative-auth, dedupe, and loop tests pass.

## Slices

### Slice 1 — protocol client and contract tests

Build a small Go CLI/library that:

- fetches `/.well-known/agent-card.json` with a timeout and size limit;
- validates the minimum fields needed for a call;
- sends an authenticated A2A 1.0 `SendMessage` request;
- polls `GetTask` until a terminal state or deadline;
- emits stable machine-readable JSON without leaking bearer tokens;
- has deterministic mock-server tests for success, working-to-completed,
  rejection, malformed payload, oversized response, authentication failure,
  timeout, and protocol-version failure.

No live cluster mutation belongs in this slice.

### Slice 2 — declarative native enablement

- Add config renderers or Helm/Kustomize resources for both native plugins.
- Add two directional Secret references without committed values.
- Add Hermes `9900` Service port and exact peer NetworkPolicies.
- Preserve tokenless ServiceAccounts.
- Add probes and read-only preflight/diff scripts.
- Validate rendered YAML and server-side dry-run before any reviewed apply.

### Slice 3 — migration proof

- Discover both Agent Cards inside the namespace.
- Test Hura to Koura and Koura to Hura.
- Continue a multi-turn exchange with the same A2A context ID.
- Prove wrong-token rejection, rate limits, bounded ping-pong, idempotency, and pod-restart behavior.
- Keep the old bridge available for rollback; remove it only in a later reviewed change.

### Slice 4 — append-only paste

Provide create/read/list/search only. PostgreSQL roles get `INSERT` and `SELECT`,
not `UPDATE` or `DELETE`. Records retain author identity, scope, timestamp,
content hash, references, and optional expiry. Writing a paste never wakes or
messages another agent.

An initial local conversation store now exists as a separate concern: SQLite
records immutable conversations, messages, and transport events. It is not the
shared bulletin board and writing to it never wakes a peer. The Store interface
is the migration boundary for PostgreSQL and later Temporal integration.

### Slice 5 — shared human room

Route `Hura`, `Koura`, and `both` from a Telegram group. For `both`, a deterministic
coordinator fans out bounded A2A tasks and renders speaker-labelled results.
Telegram is a projection and approval surface, not the durable source of truth.

### Slice 6 — Temporal durability

One workflow execution owns each conversation. Telegram messages become
Signals or Updates; A2A calls are Activities; task IDs and compact metadata are
stored in history while full transcripts/artifacts live in durable storage.
The workflow enforces ordering, retries, timeouts, budgets, max turns, and HITL.
