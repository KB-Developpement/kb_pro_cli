# KB Pro CLI

Read `README.md` for usage and `Makefile` for build and test commands.

## Work tracking

Locate `../kb_obsidian_vault` first, then `../../kb_obsidian_vault`. Read its
`CLAUDE.md` and `KB Pro CLI/KB Pro CLI Hub.md` before starting work. The hub,
`KB Pro CLI/KB Pro CLI Dev.md`, `Decisions/` and `Log/` hold project state,
decisions and session history. Keep technical reference documentation in this repo.

Use `/log-session` after meaningful work and `/adr` for lasting decisions; update
the board when state changes. Command sources live in the vault's
`Meta/claude-commands/kb-pro-cli/`; reinstall with
`sh Meta/claude-commands/install.sh` from the vault.

## Source of truth: maintenance and provenance work

Code for the maintenance plan (Phase 0: bench lock, transaction journal, Git
guard, receipts, `kb status`, `kb adopt`, `kb upgrade --to`; later phases:
maintenance sessions) builds toward the vault spec
`KB Pro CLI/KB Pro CLI Maintenance Spec.md`. Its wire, file and state contracts
are in `KB Pro CLI/KB Pro CLI Maintenance Contracts.md`, per-app identity in
`KB Pro CLI/KB Pro CLI Maintenance App Inventory.md`, and the accepted decisions
in CLI ADR-007 and ADR-008 and server ADR-008 and ADR-009. When code and those
notes disagree, the notes win until the disagreement is resolved in the vault.
