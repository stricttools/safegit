# Record file and directory moves in repository history

## Context

Git stores snapshots, not operations. A move is never recorded: the commit
contains a deletion at the old path and an addition at the new path, and every
consumer (git log --follow, diff -M/-C, blame) reconstructs the move at read
time with similarity heuristics. Those heuristics fail when a file is moved and
edited in the same commit, behave differently across consumers and settings,
and get slower and less reliable as history grows. Directory moves are worse:
they are only ever inferred file-by-file.

safegit owns the commit path for its repositories, which is exactly the
position from which a durable, authoritative record of moves can be produced.

## Problem

There is no trustworthy, durable answer to "when did this file live at that
path, and where did it go?" Everything downstream that wants file identity
across renames (blame, per-file history, changelog tooling, scrub targeting a
file across its whole life) is built on a heuristic that is allowed to be
wrong.

## Approaches

Ten options, ordered from pure discipline to restructuring the problem away.
Each is more engineered than the last. They are not all mutually exclusive;
see the recommendation at the end.

### 1. Convention only

Rule: every move happens in its own commit, unmixed with content edits, with a
message convention like `move: old/path -> new/path`. Git's rename detection
then works at 100% similarity and the message is the human-readable record.

- Pros: zero tooling, works today.
- Cons: relies entirely on discipline; one session mixing a move with an edit
  silently breaks it; nothing machine-checkable.

### 2. Commit message trailers

Machine-readable form of the same: a `Moved: old/path -> new/path` trailer per
move, parseable with `git interpret-trailers`. `internal/trailer` already
implements trailer injection and parsing.

- Pros: parseable; near-zero new code.
- Cons: hand-written, so it can be forgotten or wrong; still trust-based.

### 3. A `safegit mv` command

The tool performs the filesystem move, stages the deletion and the addition
atomically through the existing per-invocation index machinery, and writes the
trailer itself. The record is generated, not typed.

- Pros: cannot be forgotten or mistyped on the path that goes through the
  command; fits the existing command surface.
- Cons: moves made outside the command (editor refactor, bare `mv` followed by
  `safegit commit`) go unrecorded.

### 4. Detection at commit time, journaled locally

The commit pipeline compares deletions against additions in the commit it is
about to make -- exact blob-SHA matching first, similarity matching second --
and appends detected moves to an append-only journal at
`.git/safegit/moves.jsonl`, the same pattern as the oplog and the scrub
rewrite journal.

- Pros: captures all moves that pass through safegit regardless of how the
  file was moved; no operator action required.
- Cons: the journal is local to one clone and dies with the machine; similarity
  matches are still heuristic (though recorded once, at the moment the change
  was made, when the evidence is freshest).

### 5. The journal as a tracked file

Same detection, but the record lives in a committed file in the repository,
updated as part of the same commit that contains the move (safegit can inject
it into the tree it builds).

- Pros: survives clones; visible to every consumer with no extra fetch.
- Cons: a shared mutable file that concurrent sessions append to -- merge
  conflicts by construction; history noise in every moving commit.

### 6. Git notes on a tool-owned ref

Attach the move record to the exact commit that performed the move, via
`refs/notes/moves`. Notes live outside the working tree, attach to the precise
commit, are shareable with push/fetch, and have their own merge machinery.
This fits safegit's existing habit of tool-owned namespaces
(`refs/backups/*`) and CAS ref updates; `safegit push` could carry the notes
ref automatically.

- Pros: no working-tree conflicts; exact commit attachment; shareable.
- Cons: notes are second-class in most hosting UIs and easy to forget to push
  without tool support.

### 7. Verified records, not claimed ones

Orthogonal to storage: each record carries evidence and a confidence class.

- `exact`: old blob SHA equals new blob SHA -- cryptographically certain.
- `similar`: similarity score recorded, threshold declared.
- `declared`: the operator asserted the move via `safegit mv` but content
  changed too much to verify.

Consumers trust `exact` unconditionally and treat the rest accordingly. The
record becomes a checkable claim instead of an assertion.

- Pros: honest; downstream tooling can choose its own trust threshold.
- Cons: adds schema and classification logic; only meaningful combined with
  one of the storage options above.

### 8. A parallel move history under a dedicated ref

Each move becomes its own object: a commit on `refs/safegit/moves` whose tree
encodes the mapping (old path, new path, blob SHAs, and the working-history
commit it corresponds to), CAS-updated like every other safegit ref, pushed
and fetched as a unit. The move log becomes a first-class, append-only,
concurrency-safe data structure with git's own integrity guarantees -- a
queryable sibling of the real history rather than an annotation on it.
Follow-style queries read this ref instead of re-running heuristics.

- Pros: fully structured and queryable; concurrency-safe with existing lock
  and CAS machinery; independent of commit messages and notes.
- Cons: a new ref namespace to design, push, fetch, and garbage-collect; the
  correspondence between move commits and working-history commits must be
  maintained across rewrites (scrub already journals commit maps, which
  helps).

### 9. Stable file identity

Stop recording moves; make them derivable. Every file gets a persistent ID at
creation, held in a tracked identity map of ID to current path (compare
Unity's .meta files and Mercurial's copies model). A move is that map entry's
path changing; an edit is the blob changing under a stable ID; per-file
history is a lookup, not a heuristic chase. Rename detection stops being a
reconstruction problem because identity is never lost.

- Pros: moves, splits, and copies all become representable; every downstream
  question gets an exact answer.
- Cons: the map must be maintained atomically with every add, move, and
  delete -- which a commit-owning tool can enforce, but files created outside
  the tool need adoption on first commit; the map is repo-visible state that
  other tooling must tolerate.

### 10. An operation log as the source of truth, git as the projection

The full restructuring: the primary record is an append-only log of typed
operations -- create, edit, move, delete, split, merge -- in the spirit of
patch-theory systems (Pijul, darcs), where a move is a primitive, not an
inference. Git commits are generated from the operation log, so snapshot
history stays perfectly standard for CI, hosting, and teammates, while every
question about moves (or any operation) is answered from the log directly.
Options 3 through 9 are partial shadows of this: option 9's identity map is
the log's projection of current identity state; option 8's move ref is the
log restricted to one operation type.

- Pros: complete; every operation-level question becomes answerable.
- Cons: the log must never be bypassed, so every write path has to go through
  the tool -- less alien for a wrapper whose premise is already "all commits
  go through safegit", but a major project and a real lock-in decision.

## Recommendation shape

Options 3 and 4 combined (declared moves via `safegit mv`, detected moves at
commit time) with storage from option 6 (notes on a tool-owned ref) and
evidence classes from option 7 is a coherent, shippable middle. Options 9 and
10 change what the repository is and are projects in their own right.

## Affected areas

- `internal/commit` -- detection pass between staged deletions and additions;
  hook point for record emission
- `internal/trailer` -- trailer format if options 2/3 storage is used
- `internal/git` -- notes plumbing (if option 6), ref plumbing (if option 8),
  blob SHA comparison
- `internal/oplog` -- journal pattern to copy for option 4
- `main.go` -- new `mv` command registration
- `internal/test` -- integration tests for declared and detected moves,
  move-plus-edit commits, directory moves, concurrent sessions

## Effort estimate

- Options 1-2: hours.
- Options 3-4: a day or two each, including tests.
- Options 5-8: several days each; option 8 the largest of the group.
- Option 7: a day on top of whichever storage exists.
- Options 9-10: separate projects, weeks, requiring their own design rounds.
