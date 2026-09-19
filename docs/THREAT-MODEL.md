# Threat model — Slice 1 (`a2actl`)

Scope: local A2A 1.0 discovery, one authenticated `message/send`, and
`tasks/get` polling. No cluster mutation, no Telegram, no Temporal, no paste
service.

## Trust boundaries

- The peer agent is **partially trusted**: it holds a distinct directional
  credential but its task payloads are untrusted input. Nothing a peer
  returns grants operator or deployment authority.
- The bearer token environment is trusted; process arguments, stdout logs,
  and shell history are not. Hence tokens travel only via named env vars and
  are never printed, logged, or accepted as flags.
- `httptest` mock servers are the only test peers. Tests never contact the
  live cluster and use synthetic tokens.

## Controls in this slice

1. **Directional credentials.** Each invocation resolves one bearer from
   `--token-env`. There is no shared global secret in code; Hura-to-Koura
   and Koura-to-Hura use different env vars/Secrets at deploy time.
2. **Version pinning and tenant routing.** The client sends
   `A2A-Version: 1.0`, selects only a JSONRPC `supportedInterfaces` entry
   whose `protocolVersion` is `1.0`, and rejects Agent Cards or responses that
   declare another protocol version. The well-known card is always fetched at
   the URL origin root, so an endpoint path such as `/a2a/v1` cannot redirect
   discovery. The selected interface's `tenant`, when present, is carried into
   every `SendMessage`/`GetTask` request via `--tenant` (omitted when absent);
   it is routing metadata, never a credential.
3. **Bounded network use.** Every call has a context deadline plus an HTTP
   client timeout; bodies are capped (default 1 MiB) and oversize responses
   fail closed; `wait` uses bounded exponential backoff (200 ms to 2 s) and
   stops at the context deadline. `SendMessage` requests
   `configuration.returnImmediately: true` so a long task cannot pin the
   connection and hide its task ID.
4. **Strict response checks.** JSON content type required, JSON-RPC
   `2.0` envelope required, and the response `id` must exactly echo the
   request `id` (missing/null/numeric/mismatched IDs fail closed). Envelopes
   carrying both `result` and `error`, and bodies carrying trailing JSON after
   the first value (card or RPC response), are rejected. Minimum fields are
   validated (`name`/`version` plus a 1.0 JSONRPC interface on cards;
   `id`/`contextId`/`status.state` on tasks), and states/roles must use the
   A2A 1.0 ProtoJSON enums (`TASK_STATE_*`, `ROLE_*`). Card validation is a
   deliberate narrow operational subset and does not assert every field the
   1.0 schema marks required; unknown fields are preserved for output but
   never executed.
5. **No identity spoofing.** Peer identity comes from the URL under test and
   the presented credential outcome, never from a caller-supplied author
   field in a task.
6. **Failure isolation.** Terminal `failed`/`rejected`/`canceled` states exit
   4 with the payload on stdout and the reason on stderr; they never trigger
   retries, sidecars, or follow-up sends in this slice.

## Residual risks (later slices)

- No durable ordering/retries/HITL yet (Temporal slice owns that).
- No server-side rate limits, loop guards, or cost budgets in this CLI; the
   gateway slice must enforce hop/turn/wall-time/concurrency bounds.
- Kubernetes Secret plumbing, Service ports, and NetworkPolicy arrive in
  Slice 2; until then credentials live only in the invoking shell.
