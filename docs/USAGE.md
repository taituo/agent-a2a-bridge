# a2actl usage

`a2actl` is a minimal A2A 1.0 client for Slice 1. It discovers an Agent Card,
sends one message, polls one task, and optionally stores a queryable transcript.

Build:

```sh
go build -o bin/a2actl ./cmd/a2actl
```

## Commands

```sh
a2actl discover --url URL [--timeout 15s]
a2actl send --url URL --token-env ENV_NAME --message TEXT [--context ID] [--tenant T] [--timeout 30s]
a2actl wait --url URL --token-env ENV_NAME --task ID [--tenant T] [--timeout 60s]
a2actl conversations --store DB [--limit 100]
a2actl messages --store DB --conversation ID [--limit 100]
a2actl events --store DB --conversation ID [--limit 100]
```

- `--url` for `discover` may be a base URL or an endpoint URL; the client
  always fetches the card at the URL **origin root**
  `scheme://host[:port]/.well-known/agent-card.json`. Paths such as `/a2a/v1`
  and any query/fragment are discarded rather than appended to. For
  `send`/`wait` `--url` is the JSON-RPC endpoint (the selected
  `supportedInterfaces[].url` from the Agent Card, e.g. `http://host:9900`
  or `http://host:18789/a2a/v1`).
- Agent Cards follow A2A 1.0. `discover` requires the schema's required
  identity, capability, media-mode and skill declarations plus a
  `supportedInterfaces` entry with `protocolBinding: "JSONRPC"` and
  `protocolVersion: "1.0"`. It prints the selected interface's `url`,
  `protocolBinding`, `protocolVersion`, and `tenant`.
- If the selected interface declares a `tenant`, A2A 1.0 requires it on every
  request. Pass `--tenant T` to `send`/`wait` (the value printed by
  `discover`); it is sent in the request `params` and omitted when empty.
- Bearer tokens come **only** from the named environment variable, e.g.
  `A2A_TOKEN=... a2actl send --token-env A2A_TOKEN ...`. There is no token
  flag and tokens are never printed.
- Every call sends `A2A-Version: 1.0`, `Content-Type: application/json`, and
  `Accept: application/json`. Responses with a conflicting `A2A-Version` or a
  non-JSON content type are rejected.
- JSON-RPC 1.0 methods are `SendMessage` and `GetTask`; responses must carry a
  `jsonrpc: "2.0"` envelope and an `id` that exactly echoes the request.
  An envelope with both `result` and `error`, and any body with trailing JSON
  after the first value (card or RPC response), are rejected. Task states and
  roles use ProtoJSON enum names (`TASK_STATE_*`, `ROLE_*`).
- `send` requests `configuration.returnImmediately: true` so a Task response
  returns a task ID immediately for `wait` to poll.
- Output is stable indented JSON on stdout; errors go to stderr.

## Durable transcript

Add `--store PATH`, `--sender NAME`, `--recipient NAME`, and optionally
`--source telegram|voice|cli|temporal` to `send`. If `--context` is omitted,
the CLI creates one and uses it as both the A2A context and durable
conversation ID. Add `--store PATH --conversation ID` to `wait` to record its
terminal state.

The schema separates conversations, immutable messages, and immutable audit
events. There are intentionally no update or delete commands. Duplicate IDs
fail instead of overwriting prior history. Common credential shapes are
redacted before persistence, but callers must still avoid placing secrets in
prompts. SQLite is an initial local implementation behind a Store interface;
it can later be replaced by PostgreSQL without changing A2A routing.

Examples (synthetic values only):

```sh
a2actl discover --url http://localhost:9900
A2A_TOKEN=hura-to-koura-synthetic a2actl send \
  --url http://localhost:18789/a2a/v1 \
  --token-env A2A_TOKEN --message "ping"
A2A_TOKEN=hura-to-koura-synthetic a2actl wait \
  --url http://localhost:18789/a2a/v1 \
  --token-env A2A_TOKEN --task t1 --timeout 60s
```

## Exit codes

| Code | Meaning |
| ---- | ------- |
| 0 | Success (`wait` reached `TASK_STATE_COMPLETED`). |
| 1 | Usage/configuration (bad flags, missing env, invalid URL). |
| 2 | Authentication (401/403 or missing bearer). |
| 3 | Protocol (version mismatch, bad content type, malformed envelope, missing fields, response-ID mismatch). |
| 4 | Remote terminal task failure (`TASK_STATE_FAILED`, `TASK_STATE_REJECTED`, or `TASK_STATE_CANCELED`; payload still printed). |
| 5 | Transport/timeout (network error, deadline exceeded, oversized body). |
| 6 | Durable conversation store failure. |

`send` prints `{"type":"task","task":{...}}` or
`{"type":"message","message":{...}}` using the peer's raw object so unknown
fields survive. `wait` prints `{"task":{...}}` for the final state.
`TASK_STATE_INPUT_REQUIRED` and `TASK_STATE_AUTH_REQUIRED` are interrupted,
non-terminal states: `wait` keeps polling until the overall deadline.
