# Single-app maintenance

This implementation is under review. The disposable two-site workflow trial,
failure drills and maintainer sign-off must pass before a maintenance-capable
release is enabled on customer benches. This document describes the current
command behavior; it does not certify that trial.

Phase 2 supports one READY line-1 app, `kb_compta` or `kb_stock`. Multi-app
sessions, `--all`, framework maintenance and squash-merge reconciliation are
not supported. Use the bench owner account. Bench 5.31.0 and native Git access
to the private product repository are required. A trusted archive receipt is
required before an approval code is consumed; use `kb adopt` beforehand if the
bench predates receipts.

## Request and enter

```sh
kb maintenance request --apps kb_compta --reason "Investigate the reported issue"
```

Approve the displayed request in the license server's `/admin` interface. Run
the following command and type the entry code at its password-style prompt:

```sh
kb maintenance enter
```

Codes are not accepted on the command line or in a URL. The default branch is
`support/<session_id>`; `--branch` selects another valid Git branch name before
redemption. Entry backs up the database and public/private files of every site
and records each site's applied Patch Log. It prepares self-contained history
and an index at the receipt's exact base, then publishes `.git` without replacing
an existing destination. It does not check out files, reset or clean the working
tree, reinstall packages, build, migrate, restart services or enable developer
mode. Existing edits, ignored files, symlinks and executable files remain intact.
An archive with export transformations is refused until those transformations
can be reconciled safely.

Git uses the installation's normal HTTPS credential helper or SSH agent.
The CLI does not read its configured GitHub token for native Git access and does
not borrow the production license server's PAT. Keep Git credentials out of
remote URLs.

## Work, report and finish

Use normal Git and bench commands for the fix. Take appropriate site backups
and stop traffic/workers before changing live code or schema. Test data-changing
work on a disposable or staging site. Push the exact support commit and report
it before deploying live source:

```sh
kb maintenance status
kb maintenance report
kb maintenance watch
```

`status` is offline, takes no lock and writes nothing. It identifies dirty,
untracked and unrecorded live source, protected commits, approval expiry and
pending recovery work. `watch` runs in the foreground and reports every five
minutes. Failed reports keep the exact sequence and payload for retry; after
thirty minutes without an acknowledgement, the displayed state is stale and
unknown. Interrupting watch does not close the session or release protection.

```sh
kb maintenance finish --migration-attestation none
```

Finish requires clean source and proof that every protected commit is reachable
from a current branch in the same remote product repository. `none` attests that
no migration or other schema change was performed since entry. Use `changed`
if schema changed. Omitting the attestation, or a Patch Log difference, makes
`schema_changed` sticky. Finish records `pending_release`; it does not remove
Git history or permit ordinary archive upgrades. Repeating an unchanged finish
does not create another report sequence or rewrite the receipt.

To resume supported editing after finish or approval expiry, request and redeem
a fresh approval for the same frozen app scope:

```sh
kb maintenance request --renew --reason "Continue the same support work"
kb maintenance enter
```

Approval expiry keeps source protected. A passing license contract and exact
installation/key standing are still required for recovery and exit.

## Publish and exit

Merge the support commits into the live product line without squashing away
their ancestry. Publish a stable release containing every protected commit and
every local branch/ref tip. Stop traffic and workers for affected sites using
the host's service procedure, then name the exact release:

```sh
kb maintenance exit --release kb_compta=v1.1.2 \
  --migration-attestation none --services-stopped
```

The tag above is an example, not a release recommendation. Exit refreshes online
license standing, obtains a strict licensed archive and verifies the tag's
peeled commit against the server's immutable commit header. Dirty source,
untracked files, stashes, linked worktrees, in-progress Git operations,
unexpected refs and missing ancestry refuse the return. There is no squash
bypass flag.

Before a source swap, exit verifies a complete recovery copy, including `.git`,
reflogs, unreachable objects, ignored files, modes and symlinks. Available space
must be at least twice the app size. Fresh verified backups are taken for each
site with the app installed. The shared app source is replaced once, and each
affected site receives one explicit `bench --site <site> migrate`. Sites without
the selected app are not migrated.

Before migration, a verified restoration is allowed only when no previous schema
change is known. Failed package restoration retains recovery state even if the
source bytes were restored. Once migration starts, no automatic source rollback
occurs. Outcomes are recorded as `unknown` before execution and then `ok` or
`failed`; an interrupted migration never appears successful. Both source sets
and the recovery copy remain available for deliberate recovery.
Migration outcomes remain in the session after the operation journal is cleared.
Closing also retains a private completed `session.json` under the session's
recovery directory, so a later request cannot erase that evidence.

After successful source application and migrations, exit finalizes the archive
receipt and Bench metadata, reports the return, and closes the server session.
The operator credential is deleted only after a confirmed HTTP 200 close. If
close fails, retry `exit`; it retries close without reinstalling or migrating.
Resume services using the host procedure after checking the sites and app import.
Recovery copies are never deleted automatically.

On the native acme trial, the successful migration queued Module Profile updates
and search indexing while workers were stopped. An immediate ordinary upgrade
then failed with `DocumentLockedError` during fixture import. Drain that queued
work with the host's worker procedure after the released source and migration
are verified, while keeping site traffic in maintenance mode. A native burst
worker cleared the locks on this disposable bench; workers must be available
before the next migration. Preserve a failed ordinary journal and recover
forward deliberately rather than deleting it or rolling source back alone.

## State and recovery

`.kb/bench-id` identifies the bench. `.kb/maintenance/session.json` contains
versioned source state, frozen mapping, backups, Patch Log snapshots, protection
refs and pending source/report/close records. The session credential lives only
in the operator config directory's `maintenance/<session_id>.json`, outside the
bench, in a 0700 directory with a 0600 file. Copying only the bench does not
provide the credential needed to resume it; a different installation fingerprint
is refused even when stored IDs match.

Protected or malformed maintenance state blocks ordinary source/configuration
mutations across the whole bench before journal cleanup or replay. Offline
status, reports/watch, finish, exit, read-only license status and update checks
retain their paths. Activation must use the session's original fingerprint and
license-key hash. Do not manually clear a pending journal to unlock source.

An interrupted attach can be resumed with `enter` only when the attached Git
repository carries the matching versioned ownership marker and the expected
base/branch. The existing `.git` inode is preserved. An interrupted exit or
incomplete restoration requires deliberate operator recovery: inspect status,
the recorded paths and migration outcomes before deciding which source and
database state to retain. A source recovery copy is not a database rollback.

### An approval that was never entered

A lost redeem response cannot be retried to recover its one-time credential.
Do not attach Git yourself. Have the server operator revoke that unentered
session and confirm that `/admin` shows it closed. Denied and expired unentered
requests also need their terminal server state confirmed before starting again.
Repeating `request` otherwise intentionally reuses its original idempotency key.

There is no automatic local reset. Only when **all** of the following are true
may the operator preserve the old request record elsewhere under
`.kb/recovery/requests/`, move `session.json` there, and run a new `request`:

- The server operator has confirmed that the previous session is closed.
- The local source state is `archive`, and `pending`, `pending_report` and
  `pending_close` are absent.
- Every selected app has a valid archive receipt with no session ownership,
  and its own `.git` entry is absent.
- The ordinary journal is absent or finalized, and no other receipt protects
  maintenance source in this bench.

Preserve the previous record with mode 0600 in a 0700 recovery directory; do not
delete it. This procedure applies only to unentered archive requests. If any
condition is uncertain, retain the state and investigate. Never use it to clear
an attach, active, pending-release, returning or recovery journal.
