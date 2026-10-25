---
name: vroom
description: "Trigger: vroom, local services, project daemon, service logs, orchestration stack, .vroom.toml, or vroom status. Operates and configures vroom."
---

## Activation Contract

Load for vroom services, stacks, lifecycle, logs, status, or `.vroom.toml`.

## Hard Rules

- CLI commands emit JSON. Preserve JSON errors and do not parse TUI prose.
- Resolve ambiguous projects with `--path`.
- Inspect state before starting or stopping a service.
- `start` is daemonized; `build` and `install` are synchronous jobs.

## Execution Steps

1. Run `vroom list` or `vroom status`.
2. Run `start`, `stop`, `build`, `install`, `logs`, or `launch` with explicit target.
3. Use `--path` for duplicate worktrees.
4. Report command, project path, JSON result, route, and warnings.

## Stack Rule

Run `vroom launch --list` before an unfamiliar stack. Stages run in order and services inside a stage run concurrently. Use `--dry` to inspect a stack before starting it.

