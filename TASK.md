---
title: Slice 1 - A2A client and contract tests
status: ready
model: opencode/muse-spark-1.3-contributor-free
scope: this-repository-only
---

# Task

Implement Slice 1 from `docs/PLAN.md` as a clean, idiomatic Go module.

Deliver a reusable internal package plus a CLI named `a2actl` with these
commands:

```text
a2actl discover --url URL
a2actl send --url URL --token-env ENV_NAME --message TEXT [--context ID]
a2actl wait --url URL --token-env ENV_NAME --task ID [--timeout DURATION]
```

Requirements:

- Target A2A 1.0 and use the official Agent Card discovery path.
- Resolve bearer values only from the named environment variable. Never accept
  a token flag and never print a token.
- Use context-aware HTTP calls, explicit client and overall deadlines, bounded
  bodies, and JSON content-type validation.
- Keep wire structs narrow but forward-compatible. Preserve unknown response
  fields where needed for stable JSON output.
- Return distinguishable exit codes for usage/configuration, authentication,
  protocol, remote terminal task failure/rejection, and transport/timeout errors.
- `send` must support both an immediate Message and a Task response.
- `wait` polls with bounded backoff and stops on completed, failed, rejected,
  or canceled task state.
- Unit tests must use `httptest`; do not contact the live cluster.
- Add a Makefile with `fmt`, `vet`, `test`, and `check` targets.
- Add concise usage and threat-model documentation.
- Run formatting, vet, tests, and any race tests that are practical.
- Commit the finished slice with a focused commit message.

Before editing, explain the intended package boundaries briefly in the session.
Do not implement Slice 2 or mutate `/home/tiny/projects/agent-runtime-lab`.

