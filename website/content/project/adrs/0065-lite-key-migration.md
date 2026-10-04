---
title: "ADR 0065: Migrating an existing Lite install off the published encryption key"
linkTitle: "0065 · Lite key migration off the published key"
weight: 650
description: "ADR 0065: An explicit, operator-triggered `dexaflow lite migrate-key` that records every key before touching a row, re-encrypts in one verified transaction, and drops the predecessor only after a clean pass."
---

**Status:** Proposed
**Date:** 2026-10-04
**Relates:** ADR 0019 (secret encryption at rest; this ADR narrows its "re-encrypt at startup" rule for Lite), ADR 0009 (Postgres advisory locks), ADR 0026, ADR 0029 and ADR 0030 (the Lite datastore, managed or Docker), ADR 0011 (strict TDD, which the test matrix below has to satisfy).
**Issues:** #1263 (this decision), split from #486. #507 (Variables encrypted at rest) changes what the sweep has to cover. PR #1264 landed the per-install key for new installs and carried the three rejected attempts recorded below.

> **Numbering.** 0062 (#1307), 0063 (#1365) and 0064 (release branching, in
> draft) are open PRs. If any of them lands under a different number, or another
> ADR merges first, this file is renumbered before merge. Nothing else in the
> repository refers to it by number yet.

## Context

Until v0.5.0, Dexaflow Lite encrypted every connection `password` and `extra`
with `devSecretKey` (`internal/cli/dev.go`), a constant compiled into this
repository and identical on every install. Anyone holding a Lite datastore (a
backup, a synced home directory, a support bundle, a resold laptop) reads every
credential in it with no work.

v0.5.0 (#1264) fixed this for **new** installs: `dexaflow setup` generates a
per-install `secret_key` and writes it to `~/.dexaflow/config.yaml`, and the
published constant is never accepted by an install that has its own key. An
install created **before** that still has no `secret_key`. `liteSecretKeyList`
maps the empty key to the constant, and `warnIfSharedSecretKey` prints on every
start that the datastore should be treated as holding readable credentials and
that no migration exists yet. That warning is honest. This ADR is what lets it
name a fix.

Moving such an install means re-encrypting every stored credential. Doing it
wrong is worse than the status quo: a credential under the published key is
exposed, but a credential under a key that nobody recorded is gone. Three
attempts were written and each was sunk by security review. They are the
requirements for this design, so they are recorded precisely.

### Attempt 1: a read fallback, no rewrite (commit 2b7d587)

A decrypt-only `LEOFLOW_SECRET_KEY_FALLBACK` pointed at the constant, reads
reported "stale", and nothing ever rewrote a row. Six places, including the
operator-facing boot log, said values were "re-encrypted on first read". They
were not. An operator who believed the log and dropped the old key lost every
connection.

**Lesson:** a log line, help text or comment may only state what the code
verified in the same run. Never a prediction, never a plan.

### Attempt 2: implicit machinery (commit d47cb1d)

A backfill on every `lite` boot, key preservation in `uninstall`, key adoption
in `setup`. Two reviews found eleven defects, nearly all in that machinery:

- A regex YAML reader returned empty for `secret_key: abc`, `secret_key: 'abc'`
  and `secret_key: "abc" # note`. A rewrite persisted the emptiness and wedged
  the install on the published key permanently, reachable with no hand edit by
  running `reset-password` before the first boot.
- `os.WriteFile` applies its mode only on create, so a world-readable config
  stayed world-readable while gaining an encryption key. The test seeded the
  file at `0600` and never exercised its own property.
- Non-atomic writes could take the encryption key, the JWT secret and the admin
  hash together.
- Preservation covered only managed `pgdata`, so Docker-backed installs lost
  everything on uninstall and reinstall.
- The preserved key was written inside `pgdata`, which is the artifact the
  threat model is about.

**Lesson:** one explicit path the operator asked for, no migration as a side
effect of boot, setup or uninstall, and every Lite datastore flavor in scope.

### Attempt 3: an explicit `rotate-key` (commit fbcb671)

Closer, still unsafe:

- The sweep migrated readable rows and **then** returned an error if any row
  was unreadable. The command treated any error as "nothing happened",
  discarded the freshly generated key, and printed *"nothing was changed in
  your config, the previous key still works"*. The migrated rows were under a
  key that existed nowhere.
- An interrupt between the sweep and the config write did the same,
  deterministically for an install with no `config.yaml` (which `lite`
  permits).
- It ran against a live server, whose writes during the pass landed under the
  **old** key and were orphaned when the config dropped it.

**Lesson:** record the new key durably before any row can be written under it;
treat a partial pass as incomplete, never as "nothing happened"; and exclude
every other writer for the whole operation.

### What already exists and was reviewed clean

- `internal/secrets/keylist.go`, `fallback.go`: the key list (first encrypts,
  the rest only decrypt) and the try-in-order decrypt. Safe only because
  AES-GCM is authenticated: a wrong key fails to open rather than returning
  plausible garbage.
- `internal/storage/reencrypt.go` and the optimistic
  `UpdateConnectionCiphertext`: the per-row sweep, integration-tested against a
  real Postgres, with independent handling of the two encrypted columns.
- `internal/cli/config_file_secrets.go`: YAML parsing of the config with no
  environment overlay.
- `internal/cli/write_atomic.go`: temp file, explicit `0600`, owner preserved,
  rename.
- `secret_key_previous` in the Lite config: today a hand-set escape hatch, and
  exactly the field this design needs.

### Gaps found on current `main` while writing this

These are not failures of the attempts above; they are things the
implementation has to fix or account for.

1. **`ReencryptSecrets` is not a transaction, and swallows `skipped`.** It
   commits row by row, and a row skipped by the optimistic guard is only
   logged; the returned count and error do not reflect it.
2. **`finishKeyRotation` runs on every server boot, Lite included.** Any Lite
   config holding `secret_key_previous` is therefore migrated implicitly at
   boot, and its log advises removing the previous key "from
   `LEOFLOW_SECRET_KEY`", which is not where Lite keeps it.
3. **`writeFileAtomic` does not `fsync` the directory after the rename.** On
   power loss the rename itself may not be durable.
4. **`restore` writes `config.yaml` with `os.WriteFile`**, the exact
   mode-on-create and non-atomic defect fixed elsewhere.
5. **`setup` on an existing datastore with no `config.yaml`** writes a fresh
   per-install key with no predecessor. Rows under the published key then stop
   opening. They are not destroyed (an unreadable row is never rewritten), but
   nothing tells the operator how to get them back.
6. **Stale claims in comments.** The GoDoc of `config.Config.SecretKey` says the
   constant is handed to the server "so existing rows are re-encrypted rather
   than orphaned", and `SecretKeyPrevious` says "Nothing writes it". Both stop
   being true or were never true. They are the attempt 1 defect class, and
   they are corrected in the same PR as the command.

## Decision

### 1. One explicit command: `dexaflow lite migrate-key`

The migration happens only when the operator runs:

```text
dexaflow lite migrate-key [--dry-run] [--yes]
```

- **Name.** `migrate-key`, not `rotate-key`: the name attempt 3 used is tied to
  semantics that were proven unsafe, and the operation is a one-way move off a
  published key. A general rotation for Lite can reuse the same machinery later
  under its own name and ADR.
- **`--dry-run`** performs the preflight (step 0 below), prints what it found
  and what it would do, and writes nothing, anywhere.
- **`--yes`** skips the confirmation prompt. Nothing else changes.
- **Preconditions it enforces, not documents:** the Lite server is stopped (see
  section 3), `config.yaml` exists and parses, and the datastore is reachable.
  The command brings up the datastore the same way `dexaflow lite` does (managed
  Postgres or the Docker container, per ADR 0030) without starting the server,
  and leaves it in the state it found it.
- **Exit status:** `0` when the install ends fully migrated (including "already
  migrated, nothing to do"); non-zero in every other case. A non-zero exit
  always prints which recoverable state the install is in, from the table in
  section 5, and that re-running the same command is the way forward.

Nothing else migrates. `dexaflow lite`, `setup`, `uninstall`, `reset-password`,
`backup` and `restore` never re-encrypt a row and never add or remove a key
from the config on Lite's behalf. For Lite, the server's boot-time sweep is
switched off by a server setting that Lite sets in the environment it builds
(gap 2); Pro keeps the ADR 0019 behavior unchanged.

Example session:

```text
$ dexaflow lite migrate-key
  Found 14 connection secrets (9 connections) under the key published in this
  repository, 0 under another key, 0 that no known key opens.
  This re-encrypts them onto a new key that only this install has.
  The Lite server must stay stopped until this finishes.
  Continue? [y/N] y
  ✓ saved your current config to ~/.dexaflow/config.yaml.pre-migrate-key
  ✓ recorded the new key AND the published key in ~/.dexaflow/config.yaml (verified on disk)
  ✓ re-encrypted 14 secrets in one transaction; verified all 14 open under the new key alone
  ✓ re-checked after commit: 0 secrets need any other key
  ✓ removed the published key from ~/.dexaflow/config.yaml (verified on disk)
  Done. Start Lite with `dexaflow lite`.
```

### 2. Order of operations: record every key before touching any row

The invariant the whole design serves:

> **At every instant, every non-empty ciphertext in the datastore opens under at
> least one key that the on-disk `config.yaml` records**, where an absent
> `secret_key` records the published constant.

Steps, in this order, under the locks of section 3:

0. **Preflight (read only).** Parse `config.yaml` as YAML with no environment
   overlay (`configFileSecrets`). A file that exists but does not parse is a
   refusal, never "empty". Build the candidate key set: `secret_key`, every
   entry of `secret_key_previous` (a comma-separated list, parsed with
   `ParseKeys`), and the published constant **always**, because rows can be
   under it even when the config does not say so (gap 5). Read every encrypted
   column and classify it: opens under `secret_key`, opens under a predecessor
   (record which), or opens under no candidate. If any column opens under no
   candidate, **refuse and change nothing**: list the `conn_id`s and columns,
   and say plainly that those values are already under a key nobody recorded;
   the operator can re-enter or delete them, or add the key they were written
   with to `secret_key_previous`, then re-run. If `secret_key` is set, no predecessor is
   recorded and every column opens under `secret_key` alone, print "already
   migrated" and exit `0` without writing anything. A Legacy install with zero
   connections still proceeds: it has nothing to move, but it still needs a key
   of its own before it writes its first secret.
1. **Pre-image.** Copy `config.yaml` to `config.yaml.pre-migrate-key` (atomic,
   `0600`, owner preserved, file and directory fsynced). If a pre-image already
   exists from an interrupted run, keep the older one: it is the true pre-state.
2. **Record both keys.** Choose the encrypting key: on a first run, generate one
   with `generateSecretKey`; on a resumed run (a predecessor is already
   recorded), reuse the recorded `secret_key` and never generate another. Write
   `secret_key: <new>` and `secret_key_previous: <every predecessor that opened
   a row, as literal values>`, including the published constant written out
   literally rather than implied by an empty field. Every other field of the
   file is carried over unchanged. The write is `writeFileAtomic`, with the
   directory fsync added (gap 3).
3. **Verify on disk.** Re-read the file from disk through the same YAML path
   and require the recorded keys to equal, byte for byte, the ones held in
   memory. Mismatch means stop before any row is touched.
4. **One transaction.** `BEGIN`; `LOCK TABLE connections IN SHARE ROW EXCLUSIVE
   MODE` (and every other table in the sweep registry, see section 7); select
   every encrypted column; decrypt with the full recorded key list; re-encrypt
   every value a predecessor opened under the new key; `UPDATE` with the
   optimistic guard. Inside the same transaction, **re-select and decrypt every
   non-empty column with the new key alone**, and require each plaintext to
   equal the one read at the start of the pass. Any of these makes the
   transaction roll back and the command exit non-zero: a column no recorded key
   opens, an optimistic `UPDATE` that matched zero rows (under the table lock it
   cannot happen, so it means an assumption is broken), a verification that does
   not open or does not match. Only a fully clean pass issues `COMMIT`. The sweep
   returns a result with `migrated`, `skipped` and `unreadable` counts, and the
   caller treats anything but `skipped == 0 && unreadable == 0` as incomplete
   (gap 1).
5. **Re-check after commit.** In a fresh read, outside the transaction, open
   every non-empty column with the new key alone. This catches anything that
   reached the datastore outside the transaction's view.
6. **Drop the predecessor.** Only if step 5 found zero columns needing another
   key: rewrite `config.yaml` without `secret_key_previous` (atomic, fsynced),
   then re-read and verify as in step 3.
7. **Clean up.** Remove the pre-image, which by now records only keys that open
   nothing, and print the summary. On any earlier failure the pre-image is kept
   and its path printed.

Why this order closes each failure: the new key reaches the datastore (step 4)
only after it is durable and verified on disk (steps 2 and 3), so attempt 3's
"migrated under a key that exists nowhere" cannot happen. The old key leaves the
config (step 6) only after a committed, verified, re-checked pass, so attempt 1's
"dropped the old key on a false claim" cannot happen. A partial pass cannot
commit, so there is no "some rows moved, report nothing changed".

### 3. Exclusion: no live server, no concurrent config writer

- **Datastore lock.** The Lite server, at startup, takes a shared Postgres
  advisory lock on a dedicated key-migration lock id on its own session, and
  holds it for its lifetime (ADR 0009's session-lock rules apply). `migrate-key`
  takes the same id with `pg_try_advisory_lock` (exclusive) and holds it from
  step 0 to step 7. If it cannot, a server is running against this datastore:
  refuse with "stop `dexaflow lite` first" and change nothing. A server that
  starts while the migration holds the lock refuses to start with "a key
  migration is in progress". This works identically for the managed and the
  Docker datastore, and does not depend on PID files or a port probe (a probe
  of the Lite HTTP port is kept as an earlier, friendlier message, not as the
  guarantee).
- **Config lock.** Every command that rewrites `~/.dexaflow/config.yaml`
  (`setup`, `reset-password`, `restore`, `uninstall`, `migrate-key`) takes an
  exclusive `flock` on `~/.dexaflow/.config.lock` for the duration of its
  read-modify-write. `backup` takes it shared, so an archive never pairs a
  post-commit datastore with a pre-migration config. `restore` also moves to
  `writeFileAtomic` (gap 4).
- **Belt and braces.** Even with both locks, step 6 is gated on the full re-scan
  of step 5 with the new key alone, so a writer that somehow slipped past the
  lock under the old key blocks the drop instead of being orphaned by it.

### 4. Crash safety and idempotency at every step

Postgres rolls back an open transaction when the client dies, and `rename` is
atomic within a directory once the directory is fsynced. With those two facts:

| Interrupted after | Config on disk | Datastore | What a re-run does |
|---|---|---|---|
| step 0 | unchanged | unchanged | starts over |
| step 1 | unchanged (plus pre-image) | unchanged | keeps the older pre-image, continues |
| during step 2 (temp written, not renamed) | old file | unchanged | removes the stale temp file, starts over |
| step 2 or 3 | new + every predecessor | unchanged | resume: reuses the recorded new key |
| during step 4 (any point before `COMMIT` returns) | new + predecessors | rolled back, unchanged | resume: runs the transaction again |
| `COMMIT` sent, outcome unknown to the client | new + predecessors | either all old or all new | resume: preflight classifies, the transaction moves what is left (possibly nothing) |
| step 4 or 5 | new + predecessors | all under new | resume: nothing to move, re-check passes, drops predecessor |
| during step 6 | new + predecessors | all under new | as above |
| step 6 | new only (plus pre-image) | all under new | "already migrated"; removes a leftover pre-image only if it records no key that opens a row |

In every row the invariant of section 2 holds, and the same command, with the
same arguments, is the way forward. There is no separate "resume" or "repair"
command to discover.

### 5. What boot logs and command output may claim

The rule from attempt 1: **a message may state only what the same process
verified in the same run.** No "will be re-encrypted", no "on first read", no
"complete" from anything that did not just scan and see it.

`dexaflow lite` derives the install state from the config and a read-only scan
of the encrypted columns at boot (Lite tables are small; the scan decrypts and
never writes):

| State | Condition | Boot output |
|---|---|---|
| Legacy | no `secret_key` (with or without rows under the published key) | the existing warning, now naming `dexaflow lite migrate-key` and that Lite must be stopped to run it |
| Pending | `secret_key_previous` recorded | "a key migration was started and has not finished; both keys are still needed; run `dexaflow lite migrate-key` to finish it". It must **not** say "re-encrypted", "complete" or suggest removing anything by hand |
| Stranded | `secret_key` set, no predecessor, and some rows open only under the published constant (gap 5) | "N stored secrets are under the published key and this install cannot read them; run `dexaflow lite migrate-key` to recover them" |
| Migrated | every row opens under `secret_key` alone, no predecessor | nothing. In particular no message saying the published key is "removed" or "gone": the constant still exists in the binary until section 8 completes |

The server's own rotation logs (`finishKeyRotation`) are not emitted for Lite,
since the boot sweep is off there. Where they remain (Pro), the "rotation
complete" line is emitted only when `skipped == 0 && unreadable == 0`.

`migrate-key` prints each step's result only after that step's verification
(see the example in section 1). On failure it prints, in this order: what it
verified before failing, the state from the table above, which keys the config
records, and "re-run `dexaflow lite migrate-key`". It never prints "nothing was
changed" unless the config file and the datastore were both left untouched in
this run, which is checkable: steps 1 to 6 never ran.

### 6. Behavior when both keys are present

- **Lite boot (Pending).** The server gets `secret_key,secret_key_previous` as
  today: it reads with any of them and writes only with the first. New or edited
  connections land under the new key, which is already recorded, so they are
  safe. Nothing is swept at boot. The Pending warning is printed.
- **Hand-set predecessor.** A `secret_key_previous` an operator added by hand
  for their own rotation is treated the same way: `migrate-key` moves what it
  opens and drops it on a clean pass. The command is not specific to the
  published constant; it is specific to "finish what the config says is
  unfinished".
- **Degenerate configs.** `secret_key_previous` equal to `secret_key` is
  normalized away on the step 2 write. `secret_key_previous` set with no
  `secret_key` is treated as Legacy with an extra predecessor: the constant and
  the hand-set key are both recorded literally in step 2.
- **Environment.** `DEXAFLOW_SECRET_KEY` or `LEOFLOW_SECRET_KEY` exported in the
  operator's shell is ignored by the command (the file is the source of truth,
  as `configFileSecrets` already establishes), and if set the command says so
  in one line so nobody believes it was used.

### 7. What the sweep covers: connections now, Variables when #507 lands

The sweep iterates a **registry** of encrypted columns, today
`connections.password` and `connections.extra`. If #507 lands first, its
encrypted column joins the registry and is migrated in the **same** transaction,
under the same table lock. A unit test enumerates every repository write path
that calls the cipher and fails if any column it writes is missing from the
registry, so a future encrypted field cannot be silently left under a key the
migration then drops. If #507 lands after this, its PR adds the registry entry
and that test forces it to.

### 8. Rollback, and retiring the constant

- **Before `COMMIT`:** automatic. The transaction rolls back, and the config
  records both keys, which is always a safe state to run in. The supported
  forward path is re-running the command. A backward path is deliberately not
  offered: restoring the pre-image is safe only while no row is under the new
  key, and a Lite boot in the Pending state writes new rows under it. The
  pre-image is kept for diagnosis, and the docs say not to restore it once Lite
  has run in the Pending state.
- **After completion:** there is nothing to roll back to that is desirable (the
  predecessor is a published key). A binary downgrade is supported down to
  v0.5.0, the first release that reads `secret_key`; below that, a downgraded
  binary would ignore the key and read nothing.
- **Old backups.** A `dexaflow lite backup` archive made before migration holds
  a config with no `secret_key` and a dump under the published key. `restore`
  of such an archive writes the published constant literally into
  `secret_key_previous` alongside a freshly generated `secret_key`, and prints
  that `migrate-key` finishes the job. The restored install is then in the
  Pending state, never in Stranded.
- **Retiring the constant, two releases.** In the release after this ships, an
  install still in the Legacy state no longer falls back silently: `dexaflow
  lite` refuses to start and names `migrate-key`. `migrate-key` keeps the
  constant as a named, decrypt-only legacy candidate for one more release, so a
  late upgrader and an old archive still have a path. The release after that
  deletes it from the source, and the restore documentation states how to
  supply it as `secret_key_previous` by hand (the value is public regardless).

### 9. Test matrix

All of it lands test-first per ADR 0011. "Integration" means a real Postgres:
the managed binaries Lite ships, and the Docker datastore in CI.

**Invariant checker.** One helper, used after every case below: read the on-disk
config, build its recorded key set, and assert (I1) every non-empty ciphertext
opens under some recorded key; (I2) every plaintext equals the seeded value; and
on success (I3) every ciphertext opens under `secret_key` alone and no
predecessor is recorded.

**Crash injection.** The command is built on named step boundaries with a
test-only hook. The test runs the command as a subprocess and kills it
(`SIGKILL`, not a returned error) at each point, asserts I1 and I2, re-runs the
command to completion, and asserts I3:

| Id | Kill point |
|---|---|
| C1 | after preflight, before the pre-image |
| C2 | after the pre-image, before the config temp file |
| C3 | after the temp file is written, before rename |
| C4 | after rename, before the directory fsync |
| C5 | after the config write, before on-disk verification |
| C6 | inside the transaction, after the first `UPDATE` |
| C7 | after every `UPDATE`, before in-transaction verification |
| C8 | after verification, before `COMMIT` |
| C9 | immediately after `COMMIT` returns |
| C10 | after the post-commit re-check, before dropping the predecessor |
| C11 | during the drop rewrite, before rename |
| C12 | after the drop, before pre-image cleanup |

Also: the Postgres connection terminated server-side (`pg_terminate_backend`)
mid-transaction, and a Lite boot in each intermediate state (it must start,
read every connection, and print the Pending warning).

**Datastore contents (integration).**

- The **mixed** case the issue requires: readable rows under the published key,
  readable rows under the per-install key, and a row no key opens, in one
  datastore. Expected: preflight refusal, config and datastore byte-identical
  afterwards.
- A column that becomes unreadable between preflight and transaction (injected):
  rollback, non-zero exit, config in Pending, I1 holds.
- `password` readable and `extra` under the predecessor (and the reverse);
  nil and empty columns; zero connections; one connection; a few thousand.
- Rows under three keys (per-install, hand-set predecessor, constant).
- Stranded (gap 5): `secret_key` set, rows only under the constant, no
  predecessor recorded. Expected: recovered and migrated.
- Verification failure forced with a cipher double that seals garbage:
  rollback, nothing committed.
- Variables, once #507 lands: migrated in the same transaction; and the
  registry-completeness unit test of section 7.

**Config file.**

- `secret_key: abc`, `secret_key: 'abc'`, `secret_key: "abc" # note`, and the
  same spellings for `secret_key_previous`, including a comma list.
- Unparseable YAML: refusal, nothing written.
- No `config.yaml`: refusal that names `dexaflow setup` first, and the Stranded
  path then recovers it.
- Starting mode `0644`: ends `0600`. Owner preserved when run under `sudo`
  (root-owned temp, user-owned result). Read-only directory and a full disk at
  the temp write: refusal before any row is touched.
- `DEXAFLOW_SECRET_KEY` and `LEOFLOW_SECRET_KEY` exported with a different value:
  ignored, and the file's keys are what end up recorded.
- Every other field (`jwt_secret`, admin hash, workspace, port, executor,
  parser) survives both rewrites unchanged.

**Exclusion.**

- Server running: refusal before step 1, nothing written.
- Server started while the migration holds the lock: the server refuses to start.
- `reset-password`, `restore`, `uninstall` and `setup` during a migration: they
  wait or refuse on the config lock. `backup` during a migration: the archive
  pairs consistent config and data.

**Idempotency and messages.**

- Running on a migrated install: exit `0`, the config file's inode and mtime
  unchanged, no datastore write (statement log).
- `--dry-run` in every state: no file and no row changes.
- Golden tests for boot output in each state of section 5, asserting the
  forbidden phrases ("re-encrypted", "complete", "nothing was changed") appear
  only where section 5 allows them.
- `restore` of a pre-migration archive lands in Pending, and `migrate-key`
  finishes it.

## Consequences

- **An existing install can leave the published key** with one command, and
  every interruption is recoverable by running the same command again.
- **No path writes a row under a key the config does not record**, and the
  predecessor leaves the config only after a committed, verified, re-checked
  pass. These are tested at twelve kill points, not argued.
- **Lite has to be stopped to migrate.** That is the price of excluding the
  writer that broke attempt 3; Lite is single-user and local, so it is a short
  interruption the operator chooses.
- **Lite diverges from ADR 0019 on one point:** its server does not re-encrypt
  at boot. Pro is unchanged. ADR 0019 gets a dated note pointing here when this
  is accepted.
- **Every Lite boot scans the encrypted columns once** to choose its message.
  Lite tables are small; Pro is not affected.
- **New cross-command locking** (`.config.lock`, the advisory lock id) is
  surface that every future config writer must take. The registry test and the
  exclusion tests are what keep that honest.
- **The constant leaves in two releases**, not one, so late upgraders and old
  archives keep a documented path.

## Alternatives considered

- **Read fallback with lazy re-encryption on read (attempt 1).** Rejected: it
  never finishes, and its only observable output is a claim.
- **Implicit migration at boot, setup or uninstall (attempt 2).** Rejected:
  every implicit path mutates the config under the operator, on a path nobody
  asked for, and that is where nearly all of the eleven defects came from.
- **Sweep first, write the key afterwards (attempt 3).** Rejected: any failure
  between the two leaves rows under an unrecorded key.
- **Per-row commits with a resumable cursor.** Workable, given the key
  ordering, but a single transaction makes "verified before commit" possible
  and makes every intermediate state either all old or all new. Lite datasets
  are small enough for one transaction.
- **Keeping a copy of the key next to the datastore.** Rejected in attempt 2 and
  again here: it puts the key inside the artifact the threat model names.
- **A `--force` that migrates the readable rows and leaves unreadable ones.**
  Not offered in this version. An unreadable row is already lost to every
  recorded key; the operator resolving it explicitly (re-enter, delete, or
  supply its key) is better than a flag whose effect is hard to state honestly.
  It can be added later if field reports show it is needed.
- **Envelope encryption or a key hierarchy.** Out of scope, as in ADR 0019;
  this is a one-time move off a published key.
