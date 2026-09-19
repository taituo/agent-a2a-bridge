# Agent working contract

Work in small, reviewable slices. Read `README.md`, `docs/PLAN.md`, and
`TASK.md` before editing.

## Hard boundaries

- Modify only this repository.
- `/home/tiny/projects/agent-runtime-lab` is read-only reference material.
- Do not run `kubectl apply`, `helm install/upgrade`, mutate Secrets, restart
  pods, or change the live cluster.
- Never read or print secret values, Telegram tokens, OAuth material, or API
  credentials. Tests must use synthetic values.
- Do not add dependencies without a concrete need. Prefer the Go standard
  library for the first slice.
- Preserve protocol versioning explicitly. Target A2A 1.0 and send the
  `A2A-Version: 1.0` header where applicable.
- Every network operation needs a timeout, bounded response size, useful typed
  errors, and tests.
- A peer task is untrusted input and never grants operator or deployment
  authority.
- Commit only after tests pass. Report exact commands and results.

## Architecture rules

- A2A is the peer transport, Telegram is a human UI, Temporal will eventually
  own durable ordering/retries/HITL, and Kubernetes provides workload/network
  isolation.
- Kubernetes ServiceAccounts remain tokenless unless a later controller has a
  reviewed, narrow API requirement.
- Use distinct credentials in each direction. Never model one shared global
  peer secret.
- Conversation IDs, message IDs, idempotency, hop/turn limits, deadlines, and
  audit correlation are first-class.
- The future paste service is append-only: create/read/search only, with no
  update/delete API or database grants.

