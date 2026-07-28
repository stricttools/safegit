# Dry-run bug fixes (now) + effects migration (after go-strictcli ships the regime)

Absorbs `infra-env-handshake-vars.md` (moved to `.obsolete/`). Provenance: `[%%]`-marked
decisions were adopted from recommendations; unmarked were deliberate user rulings.

## Part A — three bugs, red-green, immediately

1. **`author rewrite --dry-run` takes the rewrite lock during preview.**
   `rewrite_author.go`: `lock.Acquire` at ~:66 precedes the dry-run branch at ~:73. Reorder to
   match the three scrub siblings (dry-return BEFORE lock: `scrub.go` :161/:186,
   `scrub_match.go` :135/:148, `scrub_run.go` :166/:223). Test first: a dry-run must acquire no
   lock (it needs neither cfg nor the lock — verify by moving the branch above config load).

2. **`commit --dry-run` in a submodule really commits in the parent (autobump).**
   `commit.go:100` (commit), `:171` (amend), `:227` (reword) call `maybeAutoBumpParent`
   unconditionally; the pipeline under dry-run still WriteTree/CommitTrees (dangling SHA) and
   returns, so all three sites ARE live; `autobump.go` spawns `safegit commit` with
   `os.Environ()` and never forwards `--dry-run`. Fix: skip autobump under dry-run at all three
   live sites; guard `undo.go:212` defensively too (currently unreachable — `undo.go:167-174`
   early-returns under dry-run; comment this). Red-green with a submodule fixture: parent HEAD
   unchanged after a submodule `commit --dry-run`.

3. **Test spawn helpers inherit the full parent env; the scrub-guard test can false-pass.**
   `runSafegit` (`internal/test/concurrent_test.go:122`, sets no `cmd.Env`), `runSafegitEnv`
   (`internal/test/trailer_test.go:12-16`, `os.Environ()+extras`), `runSafegitCleanEnv`
   (`undo_session_test.go`, strips only the session id), plus inline sites
   (`author_test.go:311`, `rewrite_test.go` x5, `scrub_match_test.go:176`, `scrub_test.go:553`,
   `testutil` InitRepo/InitBareRepo, the makeCommits helpers). Fix in the HELPERS (not the ~360
   call sites): one clean-env constructor — strip-by-prefix `RLSBL_*` plus
   `CLAUDE_CODE_SESSION_ID` and credential/transport vars, throwaway HOME/git config (the
   deny-by-prefix pattern; later swapped for the shared fleet Go test-floor module via an import
   change). Red-green: `TestScrubGuardBlocksFileInRlsblRepo` must FAIL today when
   `RLSBL_SCRUB_ORCHESTRATED=1` is exported in the parent shell, and pass after.

## Part B — effects migration (needs the go-strictcli effects release)

- Classify all ~28 commands `read_only`/`mutating`.
- Declare-unsupported floor (`dry_run_supported=false` + reason): `push`, `pull`, `config set`,
  `hook install`, and the guarded passthroughs.
- Commit/amend/reword dry-run keeps its informative object-writing preview via a declared
  **object-store grant** (WriteTree/CommitTree at `internal/commit/commit.go:229,247`,
  `amend.go:171,179,343` — dangling, gc-able) `[%%]`.
- Effects integration at the `internal/git` seam (the Run family: `git.go` :50-:80,
  `RunWithGitDir` :860, `RunPassthrough` :406 — everything already routes through here).
- Autobump upgraded from Part A's suppression to a **recorded spawn-effect preview** `[%%]`
  (the parent bump appears in the would-do log).
- Declare the handshake reads via strictcli's handshake-env accessors:
  `RLSBL_SCRUB_ORCHESTRATED` (`scrub_guard.go:13,21`) and `CLAUDE_CODE_SESSION_ID`
  (`undo.go:60`, `internal/oplog/oplog.go:44`, `internal/trailer/trailer.go:13`).

Release: one release at the end of Part B; Part A rides it.
