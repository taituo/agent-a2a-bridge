# agent-a2a-bridge

`agent-a2a-bridge` is a small, strict A2A 1.0 client and interoperability
toolkit for connecting independently operated AI agents.

It currently provides a Go library and the `a2actl` CLI for:

- discovering an agent through its standard Agent Card;
- validating required A2A 1.0 fields and JSON-RPC envelopes;
- sending authenticated `SendMessage` requests;
- polling asynchronous work with `GetTask`;
- continuing a conversation with a stable context ID;
- optionally recording an append-only transcript and audit trail in SQLite;
- distinguishing authentication, protocol, remote-task and transport errors;
- keeping credentials out of command-line arguments and output.

The client has been tested with Hermes Agent and OpenClaw, but it is not tied
to either runtime. Any conforming A2A 1.0 JSON-RPC agent can be tested with it.

> Status: the client and contract-test layer is implemented. Kubernetes
> deployment helpers, the bulletin board and Temporal orchestration are roadmap
> work and are not claimed as complete.

## Why this exists

Connecting two agents with an ad-hoc webhook proves connectivity, but leaves
protocol drift, unbounded polling, ambiguous failures and credential leakage
unresolved. This project provides a deliberately narrow boundary:

```text
Agent A
   │
   │  A2A 1.0 + directional bearer credential
   ▼
Agent B

The peer may request work.
The peer does not receive operator authority.
```

The implementation fails closed on malformed cards, invalid message roles or
parts, conflicting JSON-RPC result/error members, response-ID mismatches,
oversized bodies and unknown task states.

## Install

Go 1.24 or newer is recommended.

```sh
git clone https://github.com/taituo/agent-a2a-bridge.git
cd agent-a2a-bridge
go build -o bin/a2actl ./cmd/a2actl
make check
```

The A2A transport uses the Go standard library. Optional durable conversation
storage uses the pure-Go `modernc.org/sqlite` driver.

## Quick start

```sh
# Discover the Agent Card and selected JSON-RPC interface.
bin/a2actl discover --url http://localhost:9900

# Tokens are read only from the named environment variable.
export PEER_TOKEN='replace-with-a-directional-secret'
bin/a2actl send \
  --url http://localhost:18789/a2a/v1 \
  --token-env PEER_TOKEN \
  --message 'Review this proposal' \
  --context review-42 \
  --sender human --recipient reviewer \
  --store ./conversations.db

# Poll a returned task ID to completion.
bin/a2actl wait \
  --url http://localhost:18789/a2a/v1 \
  --token-env PEER_TOKEN \
  --task TASK_ID \
  --conversation review-42 \
  --store ./conversations.db \
  --timeout 2m

# Query the immutable transcript and audit events.
bin/a2actl messages --store ./conversations.db --conversation review-42
bin/a2actl events --store ./conversations.db --conversation review-42
```

Reuse `--context` for later turns in the same conversation. If discovery
reports a tenant, pass it with `--tenant` on `send` and `wait`.

## Commands

```text
a2actl discover --url URL [--timeout 15s]
a2actl send --url URL --token-env ENV --message TEXT [--context ID] [--tenant T] [--store DB]
a2actl wait --url URL --token-env ENV --task ID [--tenant T] [--timeout 60s] [--store DB --conversation ID]
a2actl conversations --store DB [--limit N]
a2actl messages --store DB --conversation ID [--limit N]
a2actl events --store DB --conversation ID [--limit N]
```

Successful output is stable indented JSON. Diagnostics go to stderr.

| Exit | Meaning |
| ---: | --- |
| 0 | Success |
| 1 | Invalid usage or local configuration |
| 2 | Authentication failure |
| 3 | Protocol or schema failure |
| 4 | Remote task failed, was rejected or was canceled |
| 5 | Transport error, timeout or oversized response |
| 6 | Conversation store error |

See [`docs/USAGE.md`](docs/USAGE.md) for complete CLI behavior.

## Safety properties

- Explicit A2A protocol version `1.0`.
- Official origin-root `/.well-known/agent-card.json` discovery.
- Required A2A 1.0 Agent Card field validation.
- Strict JSON-RPC 2.0 response correlation and result/error exclusivity.
- Validated roles and `Part` one-of content.
- Server messages must be `ROLE_AGENT` and carry a context ID.
- Context-aware HTTP requests with deadlines and bounded bodies.
- Polling backoff designed not to trip common peer rate limits.
- Bearer values are accepted only through named environment variables.
- Transcript rows and audit events are append-only through the public API.
- Common bearer, token, API-key, password and Telegram-token shapes are
  redacted before persistence.
- Tests use synthetic credentials and local `httptest` servers only.

See [`docs/THREAT-MODEL.md`](docs/THREAT-MODEL.md).

## Tested interoperability

The client has been exercised in both directions between native Hermes Agent
and OpenClaw A2A endpoints:

```text
Hermes Agent ── SendMessage/GetTask ──▶ OpenClaw
Hermes Agent ◀─ SendMessage/GetTask ─── OpenClaw
```

The proof covered discovery, directional authentication, asynchronous
completion, a second turn in the same context, wrong-token rejection and
runtime restart. No credentials or environment-specific manifests are stored
in this repository.

## Roadmap

### Declarative runtime integration

Reviewed Kubernetes/Helm examples for native endpoints, directional Secrets,
exact NetworkPolicies, probes and rollback-safe migration from custom hooks.

### Append-only agent bulletin board

A slow shared surface where agents publish and query notes without waking one
another. It will allow create/read/list/search, but no mutation API, and retain
author identity, scope, timestamp, content hash and references.

### Human-visible shared room

Route messages to one agent or a bounded set of agents from a human chat
surface, with labelled speakers, bounded fan-out and deterministic loop guards.

### Temporal durability

Move conversation ownership into durable workflows: messages become Signals
or Updates, A2A calls become Activities, and retries, deadlines, budgets,
maximum turns and human approvals become deterministic policy. Full transcripts
live in durable storage while workflow history keeps compact references.

### Agent teams and reviewers

Support personal agents, project-specific domain agents and temporary review
juries without sharing private memory or tool authority. A peer can request
analysis; it cannot inherit another agent's credentials or operator role.

## Non-goals

- Treating peer text as an operator command.
- Sharing one global bearer token among every agent.
- Storing provider credentials or conversation secrets in Git.
- Using a human chat service as the reliable agent transport.
- Hiding protocol incompatibilities behind permissive decoding.

## Development

```sh
gofmt -w .
go vet ./...
go test -count=1 ./...
go build ./...
```

Contract tests cover success, asynchronous completion, terminal rejection,
authentication failure, timeouts, oversized responses, malformed messages,
strict Agent Cards, tenant propagation and JSON-RPC envelope edge cases.

## License

MIT. See [`LICENSE`](LICENSE).
