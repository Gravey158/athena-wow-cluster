# CLAUDE.md — athena-wow-cluster

## Briefing

The master goal is `.claude/goals/athena-wow-cluster.md` (machine-local, not committed).
Read it at session start. Phases, success criteria and non-goals are defined there.

This file + the ADRs in `docs/adr/` are the next sources of truth.

## Working directory

- `/Users/dennis/athena-wow-cluster` — remote `Gravey158/athena-wow-cluster` (public).
- Default branch: `master` (inherited from upstream ToCloud9).
- Upstream: `walkline/ToCloud9` — push disabled (`DISABLED_no_pushes_to_upstream`). Hardfork, no upstream PRs by default.
- Co-author trailer on commits: `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>`.

## Hard constraints (do not violate without explicit ask)

- **Do not touch the `athena-cluster` repo** beyond a minimal App-of-Apps pointer. The exact mechanism is the subject of ADR 002 (open).
- **Do not touch the live `gameserver-wow` namespace** on Athena. We run parallel in `wow-cluster` ns. Existing realm + wow-panel keep running.
- **MetalLB**: `10.10.30.64/.65` are taken by `gameserver-wow`. New services pull from `homelab-pool` (range `10.10.30.60-80`); planned: `.66` (auth) and `.67` (game-lb). Verify free at deploy time.
- **Self-hosted runner** (Phase 0.1, ns `wow-ci`): workflows from PRs must not run on self-hosted. Only `main`/tag triggers may use `runs-on: self-hosted`.
- **Sealed Secrets** for DB/NATS creds — reuse `~/.config/sops/age/keys.txt`, no new key id.
- **No CataclysmMoP / 4.x / 5.x scope creep**. 3.3.5a only.

## Repos & build

- ToCloud9-microservice container images: `ghcr.io/gravey158/athena-wow-cluster/*` (per inherited workflows).
- AC-fork image: `ghcr.io/gravey158/athena-wow-core` (built in Phase 0.2 on self-hosted runner).
- All `ghcr.io` packages are **public** (decision recorded by user, Phase-0 bootstrap).

## Conventions

- Branch naming: `feat/<phase>-<topic>`, `fix/<topic>`, `chore/<topic>`.
- Commits: small, with a clear deliverable per commit. Merges only after green CI + ADR (if architectural) + doc update.
- ADRs: `docs/adr/NNN-title.md`. 000-099 reserved for Phase 0. Use the Nygard template (see ADR 000).
- Tickets: GitHub issues with labels `phase-0` … `phase-5`.
- Releases: SemVer on the repo, tagged; bump `chart/Chart.yaml` `version` in lockstep.

## Reading order for a fresh session

1. `.claude/goals/athena-wow-cluster.md` — phasing + non-goals.
2. This file.
3. `docs/adr/` in order — accumulated decisions.
4. `docs/baseline.md` if it exists — current setup state.
5. `git log -10 --oneline` for recent context.
