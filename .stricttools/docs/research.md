+++
description = "Research survey of Git alternatives for AI-safe concurrent version control: Jujutsu, GitButler, Sapling, and go-git compared for multi-agent safety."
+++

# Git Alternatives for AI-Safe Concurrency

The core problem: Git's staging area (`.git/index`) is a single global file per worktree. When multiple AI agent sessions run `git add` / `git commit` concurrently, they race on this shared resource -- one session can accidentally commit another's staged files, or overwrite the index mid-operation. Worktrees are the standard mitigation but add complexity and have their own edge cases (stale lock files, orphan branches).

This document surveys four tools that either solve this problem architecturally or could serve as a foundation for a safer git workflow.


## Summary Table

| Project | Language | Type | Started | Maintainers | Activity | Solves AI Staging Races | Stars |
|---|---|---|---|---|---|---|---|
| Jujutsu (jj) | Rust | CLI tool | 2019 (Google) | 7 maintainers, 332 contributors, 1/3 cap per company | Monthly releases, v0.40.0, own conference | Yes -- no index, lock-free op log, conflicts as data | 28.1k |
| GitButler (`but`) | Rust + Svelte | CLI + GUI + MCP | ~2023 (Scott Chacon) | Commercial team | Very active, CLI shipped Feb 2026 | Yes -- virtual branches isolate each agent's work | 20.5k |
| Sapling (`sl`) | Rust + Python | CLI tool | 2022 open-sourced (Meta) | Meta source control team | Active, varies | No | 6.8k |
| go-git | Go | Library only | 2015 (source{d}, now community) | Individual contributors, gitsight | v5.18.0, v6 alpha | No | 7.4k |


## Jujutsu (jj)

- Website: https://jj-vcs.dev
- Repo: https://github.com/jj-vcs/jj
- License: Apache 2.0

### Origin and Motivation

Martin von Zweigbergk started Jujutsu as a hobby project in late 2019, with the first commit on GitHub dated December 18, 2020. Martin is a Senior Software Engineer at Google with a deeply relevant background: he contributed to Git itself from 2011-2014, then spent roughly a decade working on source control at Google (Piper, CitC, Fig -- Google's Mercurial integration). This gives him an unusually complete view of version control: Git internals, Mercurial internals, and Google-scale proprietary VCS systems.

The motivation came from Google's monorepo evolution:

- Perforce -- repo became too large
- Piper -- working copy became too large for local disk
- CitC (Clients in the Cloud) -- users wanted DVCS workflows with stacked commits
- Fig (Mercurial at Google) -- but Mercurial was aging and its design had accumulated cruft

Martin wanted a system that took the best ideas from Mercurial (revsets, changeset evolution/obsolescence), Darcs/Pijul (first-class conflicts), and Git (ubiquity, storage format), then synthesized them into something new with a clean, modern design.

What started as a hobby evolved into Martin's full-time job at Google. Google is actively planning to use jj internally -- as of late 2025, Jujutsu at Google was in open beta with plans for a Linux-only general availability release in early 2026.

### Maintainers and Governance

The project has a formal `GOVERNANCE.md` with defined roles and voting procedures. A structural safeguard: no more than 1/3 of maintainers may be employed by the same company, preventing corporate capture.

Current maintainers (7):

| Maintainer | Commits | Affiliation |
|---|---|---|
| Yuya Nishihara (yuja) | 3,677 | Independent (former Mercurial committer) |
| Martin von Zweigbergk (martinvonz) | 3,082 | Google |
| Ilya Grigoriev (ilyagr) | 639 | -- |
| Austin Seipp (thoughtpolice) | 216 | -- |
| Scott Taylor (scott2000) | 164 | -- |
| Benjamin Tan (bnjmnt4n) | 157 | -- |
| Waleed Khan (arxanas) | 111 | -- |

Total: 332 contributors, 11,192 commits. The top committer (Yuya Nishihara) is not a Googler. The funding model is hybrid: Martin works on it full-time at Google, but the governance docs state that "most people contributing to Jujutsu do so in their spare time."

### Activity

- Release cadence: one release per month, intervals of 27-35 days
- Current version: v0.40.0 (April 2, 2026), 47 releases total
- Repository moved from `martinvonz/jj` to `jj-vcs` GitHub organization in December 2024
- JJ Con 2025: a dedicated conference held September 28, 2025 at Google's Sunnyvale campus
- GitHub metrics: 28,100 stars, 1,009 forks, 743 open issues, 308 open PRs
- Still explicitly pre-1.0: "There will be changes to workflows and backward-incompatible changes before version 1.0.0"

### Design Philosophy

Jujutsu's design philosophy is built on a set of core principles that are each deliberate and deeply interconnected. They stem from Martin von Zweigbergk's experience with Git, Mercurial, and Google's internal version control systems, synthesized into a coherent model that eliminates entire categories of user confusion and operational risk:

- **"The working copy is a commit"**: Your working directory is always represented as a real commit. Edits are automatically snapshotted. There is no index/staging area, no stash, no "dirty working tree" -- just commits. This eliminates `add`/`reset`/`stash`/`checkout` confusion, the soft/hard/mixed reset distinction, and staging races entirely.
- **"Commits are the only user-visible object"**: By making the working copy a commit, the data model becomes radically simple. There is one kind of thing: commits. Branches/bookmarks and tags are just labels pointing at a commit.
- **"Operations, not commands, are the unit of undo"**: Every mutating operation is recorded atomically in an append-only operation log. `jj undo` reverses the last operation. Fundamentally different from Git's reflog, which is per-ref, requires forensic skill, and can't undo multi-ref operations atomically.
- **"Conflicts are data, not errors"**: Conflicts are stored as first-class objects in the commit graph. A rebase that produces conflicts succeeds -- you get a commit with conflict state recorded. Resolve it later, or even rebase on top of it. Inspired by Darcs and Pijul.
- **"Automatic rebase"**: When you modify a commit, all its descendants are automatically rebased on top of the modified version. History editing is fluid, not anxiety-inducing.
- **"Safe by default"**: The operation log means nothing is ever truly lost. Concurrent operations are handled safely. Destructive mistakes become impossible rather than merely recoverable.

### Divergence from Git

| Concept | Git | Jujutsu | Why jj chose differently |
|---|---|---|---|
| Staging area/index | Three states: working dir, index, committed | Working copy IS a commit; no index | The index is a constant source of confusion and adds no fundamental capability. Partial commits use `jj split` instead. |
| Conflict handling | Blocks operation; must resolve before continuing | First-class data stored in commits; resolution deferred | Enables auto-rebase, prevents workflow interruption. |
| Operation log | Per-ref reflog; no atomic multi-ref undo | Append-only log of atomic operations; `jj undo` | Git's reflog requires expert knowledge. jj makes undo trivial. |
| Branches | Named refs tightly coupled to workflow; "detached HEAD" is scary | Lightweight mutable labels ("bookmarks"); anonymous branches are default | Reduces friction for experimentation; name only when sharing. |
| Change IDs | SHA hashes change on any rewrite | Stable change IDs survive rebase/amend | Refer to a change across rewrites without tracking hash changes. Uses letters k-z to be visually distinct from git hashes 0-9/a-f. |
| Rebase | Manual, multi-step, requires `--continue` | Automatic; descendants rebase when ancestors change | History editing becomes routine. |
| Stash | Separate `git stash` mechanism | Not needed; just create a new commit | The working-copy-as-commit model makes stash redundant. |
| Undo | `git reset`, `git revert`, `git reflog` -- each different | Single `jj undo` command | One command, one mental model. |

Unique commands with no git equivalent:

- `jj absorb` -- automatically distributes working copy changes into the correct ancestor commits in a stack
- `jj parallelize` -- rearranges sequential commits into parallel branches
- `jj split` -- interactively split a single commit into multiple commits
- `jj describe` -- edit a commit message without the amend dance

### AI Agent Concurrency Model

Jujutsu's concurrency story is the most relevant aspect for safe multi-agent use. By eliminating the shared mutable index and replacing lock files with an append-only operation log, Jujutsu makes it structurally impossible for one agent session to corrupt another's staged changes. The key mechanisms are:

- **No staging area / no index**: There is no `git add`. Changes are auto-snapshotted into the working-copy commit. The entire class of "two agents fighting over `.git/index`" bugs is eliminated by design.
- **Lock-free concurrency**: Instead of lock files, jj uses an append-only operation log. Each command reads the repo state, does its work, writes a new operation atomically. If two agents operate concurrently and create divergent operation heads, jj auto-merges them on the next command.
- **First-class conflicts**: Two agents can create conflicting changes and jj records them for later resolution rather than aborting.
- **Operation log / undo**: Every operation is recorded. `jj undo` reverses any agent's mistake trivially.
- **Caveat**: With the Git backend specifically, concurrent writes to Git refs can theoretically cause repository corruption, though recovery is straightforward (`jj debug reindex`).

### Git Interoperability

How it works: `jj git init --colocate` creates a hybrid workspace with both `.jj/` and `.git/` directories. jj reads and writes standard git objects, refs, and commits. On every `jj` command, it auto-imports changes from git refs and auto-exports back.

What teammates see: Normal git commits. They have no idea you're using jj. Push/pull to GitHub/GitLab works normally.

What works:

- All commits are standard git commits (same SHA, same format)
- Push/pull to any git remote
- PRs, CI/CD, code review tools all see normal git
- Can switch between `jj` and `git` commands on the same repo

What is missing:

- `.gitattributes` -- completely ignored (no line-ending normalization, no custom diff/merge drivers)
- Git hooks -- not executed by jj
- Submodules -- not supported
- Git LFS -- not supported
- Sparse checkout -- not supported
- Shallow clone deepening -- not supported

Known rough edges:

- IDEs running `git fetch` in the background can cause "divergent change IDs" (no data loss, just confusing)
- jj's conflict format is stored as binary in the git tree -- git tools can't read it
- `git add` / `git status` become meaningless (jj ignores the index)
- Large numbers of branches can slow jj's import/export cycle

### Adopters

- Google: internal beta, planning GA in early 2026
- Mozilla Firefox: official documentation for using jj with Firefox development
- AI coding agents: growing adoption for use with Claude Code and similar tools

### Key Resources

- Docs: https://docs.jj-vcs.dev
- Chris Krycho's "jj init" essay: https://v5.chriskrycho.com/essays/jj-init/
- LWN coverage: https://lwn.net/Articles/958468/
- "jj for AI Coding Agents" blog post: https://www.panozzaj.com/blog/2025/11/22/avoid-losing-work-with-jujutsu-jj-for-ai-coding-agents/
- Developer Voices podcast: https://www.youtube.com/watch?v=ulJ_Pw8qqsE
- Git Merge 2024 talk: https://www.youtube.com/watch?v=LV0JzI8IcCY


## GitButler (`but`)

- Website: https://gitbutler.com
- Repo: https://github.com/gitbutlerapp/gitbutler
- License: Fair Source (becomes MIT after 2 years)

### Origin and Motivation

GitButler was created around 2023 by Scott Chacon, co-author of the Pro Git book and former GitHub employee. The core insight: developers often work on multiple things at once (a feature, a bug fix, a refactor), but git forces you to think in terms of one branch at a time. Switching branches means stashing, committing WIP, or losing context.

GitButler introduced "virtual branches" -- multiple branches applied to the working directory simultaneously, with per-branch staging. You work on multiple features at once and GitButler assigns hunks to the right branch.

### Maintainers and Funding

GitButler is a commercially backed company. The team includes Scott Chacon and a full engineering team. The project is funded as a product, not a volunteer effort. Sebastian Thiel (gitoxide creator) contracts for GitButler to integrate gitoxide into their backend.

### Activity

- 20,500 stars on GitHub
- Very active development
- CLI (`but`) shipped as a technical preview in February 2026, now a first-class product
- MCP server and Claude Code hooks integration actively developed
- TUI mode (`but tui`) built with ratatui

### Design Philosophy

- **Virtual branches**: Multiple branches applied to the working directory simultaneously. Each branch gets its own isolated staging area. Changes are automatically or manually assigned to branches. This eliminates cross-branch contamination.
- **AI-first**: MCP server (`but mcp`), Claude Code hooks, JSON output mode (`--format json`), forge integration (`but push`, `but forge`). Explicitly designed for multi-agent workflows.
- **Git-native storage**: Does not replace git's data model. Virtual branches are metadata stored in `.git/gitbutler/`. When you commit through `but`, it creates normal git commits on normal git branches. The innovation is the layer on top.
- **Undo**: Snapshot-based undo system (not reflog).

### CLI Commands

| Category | Commands |
|---|---|
| Inspection | `status`, `diff`, `show` |
| Branching/Committing | `branch` (new/list/integrate/destroy), `commit`, `stage`, `unstage`, `merge` |
| Commit Editing | `reword`, `amend`, `absorb`, `squash`, `uncommit`, `move`, `pick` |
| Unified Operation | `rub` (polymorphic: rub file onto commit = amend, rub commit onto commit = squash, etc.) |
| Stack/Branch Control | `apply`, `unapply`, `mark`, `unmark` |
| Remote/Forge | `push`, `pull`, `fetch`, `forge` (PR operations) |
| Undo | `undo` |
| Conflict Resolution | `resolve` |
| AI/Agent Integration | `mcp` (starts an MCP server) |
| TUI | `tui` (full terminal UI) |
| Setup | `setup`, `teardown`, `onboarding` |

Output formats: `--format human` (default), `--format shell` (scripting), `--format json` / `--json` (agent consumption).

### AI Agent Concurrency Model

GitButler directly targets multi-agent use and is one of the few tools that explicitly designs for AI coding workflows. Its virtual branch model naturally isolates each agent's changes, and the team has invested in first-class integrations with AI agent frameworks including MCP and Claude Code hooks:

- Multiple AI agents get their changes auto-isolated into separate virtual branches
- Each agent's work is tracked independently -- no staging races
- MCP server (`but mcp`) exposes GitButler operations to AI agents
- Claude Code hooks integration documented for automatic virtual branch isolation
- Trigger.dev published "We ditched worktrees for Claude Code" using GitButler

### Git Interoperability

How it works: operates directly on a standard `.git` repository. Virtual branches are metadata stored in `.git/gitbutler/` -- they don't alter git's data model. When you commit through `but`, it creates normal git commits on normal git branches.

What teammates see: normal git branches and commits. The virtual branch abstraction is local only.

What works:

- All commits are standard git commits
- All branches are standard git branches (virtual branches materialize as real branches on push)
- Push/pull to any git remote
- PRs, CI/CD, code review -- all standard
- Can use `git` commands alongside `but` commands
- Forge integration (`but push`, `but forge`) creates real PRs

What breaks or is missing:

- The virtual branch state (`.git/gitbutler/`) is local -- if you `git` directly, GitButler may need to reconcile
- `but setup` / `but teardown` manage the workspace state -- you need to be in a GitButler-managed workspace for virtual branches to work
- If you bypass `but` and use raw `git commit`, the virtual branch assignment of hunks may get confused

### Architecture

GitButler's core is cleanly layered and fully separable from the GUI, making it viable as a headless engine for AI agent workflows. The Rust crate structure enforces clear boundaries between the version control logic, the API surface, and the various frontends (CLI, desktop, and Node.js bindings):

- Layer 1: Core crates (`but-core`, `but-workspace`, `but-graph`, `but-rebase`, `but-hunk-assignment`, `but-hunk-dependency`) -- no UI dependency
- Layer 2: Unified API (`but-api`) with `#[but_api]` macro generating bindings for direct Rust calls, Tauri IPC, and N-API/Node.js
- Layer 3a: CLI (`but` binary, Clap-based)
- Layer 3b: Desktop GUI (Tauri + Svelte)
- Layer 3c: Node.js SDK (`but-napi`)

The crates are not published to crates.io (all `publish = false`). To use the engine as a library, you'd depend on the Git repository source directly.

### Key Resources

- CLI docs: https://docs.gitbutler.com/cli-overview
- Installation: https://docs.gitbutler.com/cli-guides/installation
- MCP server: https://docs.gitbutler.com/features/ai-integration/mcp-server
- Claude Code hooks: https://docs.gitbutler.com/features/ai-integration/claude-code-hooks
- Virtual branches blog post: https://blog.gitbutler.com/building-virtual-branches
- Trigger.dev adoption: https://trigger.dev/blog/parallel-agents-gitbutler
- Independent review: https://matduggan.com/gitbutler-cli-is-really-good/


## Sapling (`sl`)

- Website: https://sapling-scm.com
- Repo: https://github.com/facebook/sapling
- License: GPL-2.0

### Origin and Motivation

Sapling is Meta's internal source control system, open-sourced in November 2022. Its lineage traces through Meta's monorepo evolution: they started with Mercurial, then heavily modified it over years, eventually producing a system that shares Mercurial's heritage but diverges significantly. Sapling was designed for Meta's monorepo scale (millions of commits, millions of files) with a focus on stacked diffs workflows (integrated with Phabricator/Differential).

### Maintainers and Funding

Developed by Meta's source control team. The project is funded by Meta's internal needs. The main risk for external users: community contributions may be deprioritized relative to Meta's internal roadmap, and the project's long-term maintenance outside Meta is uncertain.

### Activity

- 6,800 stars on GitHub
- Active development, pace varies
- The CLI is fully open source (GPL-2.0)
- The ISL (Interactive Smartlog) web UI is MIT-licensed

### Open Source Status

The `sl` CLI is 100% open source. There are no closed-source components in the distributed builds. The codebase uses `#[cfg(fbcode_build)]` conditional compilation to separate Meta-internal code paths from open-source paths -- when built outside Meta's build system, inert OSS stubs are used. No telemetry is sent to Meta; the sampling system writes to a local file only.

Components that are source-available but unsupported externally:

- **Mononoke** (server-side backend): source at `eden/mononoke/`, but the README states "not yet supported for external usage." Some functions are omitted from the GitHub version. Not needed when using Sapling with Git remotes.
- **EdenFS** (virtual filesystem): source at `eden/fs/`, but similarly "not yet supported for external usage." Not needed for normal use -- without EdenFS, Sapling does a normal full checkout like Git.

Neither Mononoke nor EdenFS is required for local use with Git repositories.

### Design Philosophy

- **Stacked diffs**: First-class support for stacking commits and submitting them as dependent code reviews. This is how Meta does code review (via Phabricator/Differential).
- **Smartlog**: A visual history view (`sl smartlog`) that shows only the commits you care about, not the entire repo history. Replaces `git log` with something more useful.
- **Interactive Smartlog (ISL)**: A web-based UI for interacting with the commit graph.
- **Simplified commit model**: No staging area confusion. The commit workflow is streamlined compared to Git's add/commit dance.

### Divergence from Git

Sapling uses its own command syntax (`sl` not `git`) and introduces several workflow concepts inherited from Mercurial and refined at Meta's scale. While it can operate on standard Git repositories, its mental model differs substantially from Git in areas like branching, history querying, and code review integration. Key differences:

- Stacked commits as a first-class concept
- Smartlog replaces `git log`
- No staging area confusion (simplified commit model)
- Bookmarks instead of branches (Mercurial heritage)
- Revsets for querying commit history (more powerful than Git's revision syntax)

### Git Interoperability

How it works: in `.git` mode, `sl clone` creates a repo with both `.sl/` and `.git/`. Sapling uses git under the hood for network operations (clone/push/pull) but maintains its own internal state.

What teammates see: normal git commits and branches on the remote.

What works:

- Clone from and push to any git remote
- Commits are standard git commits
- PRs and CI/CD work normally

What breaks or is missing:

- Mixing `sl` and `git` commands is explicitly fragile: detached HEAD, incomplete add operations, interrupted rebase incompatibility
- `git status` / `git diff` may show stale or confusing state after `sl` operations
- The `.sl/` directory contains Sapling's own state that git doesn't know about
- Some git config settings are ignored
- Commit Cloud (cross-machine sync) requires Mononoke -- not available externally

You are expected to pick one tool (`sl` or `git`) and stick with it on a given repo. Coexistence is possible but has documented rough edges.

### AI Agent Concurrency

Sapling does not specifically address multi-agent staging races or provide any mechanism for concurrent agent isolation. Its simplified commit model removes the staging area confusion that plagues Git, which reduces the surface area for human errors, but it still relies on a shared working copy and does not offer lock-free concurrency, per-agent isolation, or conflict-as-data semantics that would make it safe for multiple AI agents operating simultaneously on the same repository.

### Key Resources

- Docs: https://sapling-scm.com/docs/introduction/
- Git support modes: https://sapling-scm.com/docs/git/git_support_modes/
- Meta announcement: https://engineering.fb.com/2022/11/15/open-source/sapling-source-control-scalable/
- LWN coverage: https://lwn.net/Articles/915187/


## go-git

- Repo: https://github.com/go-git/go-git
- License: Apache 2.0

### Origin and Motivation

go-git was created in 2015 by engineers at source{d}, a Madrid-based startup building ML-on-source-code tools. source{d} needed a pure Go Git implementation because their infrastructure was written in Go and they wanted to avoid CGo bindings or shelling out to git.

source{d} eventually ran into financial/legal difficulties. The go-git repository went dormant for about four months, and the community created a hard fork. The project moved to the `go-git` GitHub organization, where several original authors resumed maintenance. The `src-d/go-git` repository now contains only a redirect notice.

### Maintainers and Funding

Maintained by individual contributors, including several original source{d} authors. Backed by gitsight, where go-git is described as "a critical component used at scale." No major corporate sponsor on the scale of GitHub/Microsoft or Google. Relies on community contributions.

### Activity

- 7,400 stars on GitHub
- Current stable: v5.18.0 (April 2025)
- v6.0.0-alpha.2 in progress (April 2025) with cherry-pick, reflog, SHA-256, redesigned transport layer

### Design Philosophy

- **Pure Go, zero CGo**: No native dependencies, no manual memory management, no cross-compilation headaches. Compiles to a single static binary.
- **Pluggable storage**: Abstracts storage behind a Storer interface. Default is in-memory filesystem (fast for testing/CI). Custom implementations can back repos with databases, cloud storage, etc.
- **Idiomatic Go API**: Both plumbing and porcelain exposed through Go-native interfaces.
- **Target ecosystem**: Go is the language of cloud infrastructure (Kubernetes, Docker, Terraform). Many tools need Git interaction without a system `git` binary.

### Divergence from Git

go-git's divergence from standard Git is primarily about missing features rather than intentional behavioral differences. Where it implements a feature, it aims for full behavioral compatibility with the C Git reference implementation. The gaps are significant for production use, however, and several are fundamental operations that most workflows depend on.

Major gaps (as of v5.x stable):

- **No three-way merge** -- fast-forward only (dealbreaker for most real workflows)
- No rebase
- No stash
- No cherry-pick (coming in v6)
- No `gc`, `prune`, `repack`, `fsck`
- No `apply`, `patch`, `format-patch`
- No pack protocol v2 (only v1)
- No multiple worktree support
- Index format v2 only (not v1 or v3)

### Git Interoperability

go-git is a pure Go library that reads and writes standard `.git` repositories programmatically. It is not a CLI tool -- your Go application imports it as a dependency and calls its API directly. This makes it well-suited for embedding Git operations into Go services, CI tooling, and infrastructure automation without requiring a system Git binary.

What works:

- Reads and writes standard `.git` repos
- Clone, fetch, push over HTTPS/SSH/git protocol
- Commits, trees, blobs are all standard git objects
- Pluggable storage (database-backed repos, in-memory repos)

What is missing:

- No three-way merge (fast-forward only)
- No rebase, stash, cherry-pick (cherry-pick coming in v6)
- No repository maintenance (gc, prune, repack, fsck)
- No pack protocol v2
- No worktree support
- It's a library -- you'd have to build your own CLI on top

### AI Agent Concurrency

go-git does not address multi-agent staging races and faithfully reproduces Git's shared index model, including its concurrency limitations. Multiple goroutines or processes writing to the same repository will encounter the same race conditions as standard Git. Furthermore, go-git lacks worktree support entirely, which means you cannot even use the standard Git worktree isolation strategy as a mitigation. It is unsuitable as a foundation for concurrent AI agent workflows without substantial custom locking or isolation built on top.

### Adopters

- Gitea: experimental `gogit` build tag for deployment without system Git
- Pulumi: infrastructure-as-code Git operations
- Keybase: used extensively (before Zoom acquisition)
- Flux CD / GitOps controllers: Go-based tools using go-git for repo operations
- gitsight: code analysis at scale

### Key Resources

- Compatibility matrix: https://github.com/go-git/go-git/blob/master/COMPATIBILITY.md
- Git SCM book coverage: https://git-scm.com/book/en/v2/Appendix-B:-Embedding-Git-in-your-Applications-go-git


## Rejected Candidates

### Forking C Git

The most direct approach -- fork git's C codebase and strip/modify what we don't like -- was rejected. Git is ~400k lines of C with decades of tightly intertwined internals (index, refs, pack files, hooks all cross-reference each other). Stripping features is surprisingly hard. Maintaining a fork means inheriting the entire security patch and protocol update burden, perpetually merging from upstream. No one has ever successfully forked C git and gained traction. Every successful effort has been a ground-up reimplementation.

### libgit2 (C)

- Repo: https://github.com/libgit2/libgit2
- Started: 2008 by Shawn O. Pearce (also created JGit and Gerrit). Gained momentum when GitHub sponsored it ~2010.
- Maintainer: Edward Thomson (primary for 10+ years, now at Vanta). Historically funded through GitHub/Microsoft employment.
- Status: Mature, v1.9.x current, v2.0 planned. 10.4k stars.
- What it is: In-process C library for Git operations. No CLI. Pluggable storage backends. Used by GitHub (merges PRs with it), Azure DevOps, GitLab (though actively deprecating it), Visual Studio, Cargo (via git2-rs).
- **Why rejected**: Library only, no CLI. Written in C (memory safety concerns). Faithfully reproduces git's index/locking model -- does not address staging races or any concurrency problem. GitLab is actively moving away from it. Being gradually replaced by gitoxide in the Rust ecosystem.

### JGit (Java)

- Repo: https://github.com/eclipse-jgit/jgit
- Started: 2006 by Shawn O. Pearce. Created for Eclipse IDE and Gerrit Code Review.
- Maintainer: Matthias Sohn (SAP, project lead). Contributors from Google, GerritForge. Eclipse Foundation governance.
- Status: Mature, v7.6.0 (March 2026). 678 commits/year, 23 contributors.
- What it is: Pure Java library. The most feature-complete reimplementation of git (nearly full parity). Powers Gerrit (all Android development flows through it), Eclipse IDE, Jenkins, Bitbucket Server.
- **Why rejected**: Library only, no CLI. Java runtime dependency is heavy for a CLI tool. Faithfully reproduces git's index model -- no concurrency solution. Missing SSH key commit signing, some config options, SVN bridge, bundles.

### gitoxide / gix (Rust)

- Repo: https://github.com/GitoxideLabs/gitoxide
- Started: June 2018 by Sebastian Thiel (former 3D movie pipeline dev, ThoughtWorks contractor).
- Maintainer: Essentially one person -- Thiel has 12,889 of ~14,845 commits (87%). Works on it at night; day job is contracting for GitButler. Funded by GitHub Sponsors, a $20k Meta grant, Rust Foundation grant, and GitHub Secure Open Source Fund.
- Status: Active, 211k LOC across 65 crates, ~24.5 commits/week, 11.2k stars. Monthly retrospectives.
- What it is: Pure Rust reimplementation of git (no C dependencies -- even zlib is pure Rust and beats C zlib-ng by 1%). Library-first; the CLI is explicitly "may forever be unstable." Can clone and fetch but cannot commit, push, pull, merge, or rebase.
- Used by: Cargo (fetches use gitoxide), GitButler (backend), Jujutsu (git backend uses gix), Starship.
- **Why rejected**: CLI cannot commit/push/pull -- not usable as a daily driver. Faithfully reproduces git's index/locking model -- no concurrency solution. Critical bus factor: if Thiel stops, the project is in serious trouble. Excellent engineering but solves a different problem (safe, fast, embeddable git library for Rust programs).

### Game of Trees (C)

- Website: https://gameoftrees.org
- Started: OpenBSD project. Uses git repo format but its own codebase and command names (`got` instead of `git`).
- Status: Active but niche (targets OpenBSD developers specifically). Latest release v0.121 (Jan 2026). Has a server daemon (`gotd`). ISC-licensed.
- **Why rejected**: Written in C. Extremely niche (OpenBSD only). Different command syntax. No concurrency solution.

### Pijul (Rust)

- Website: https://pijul.org
- What it is: A mathematically principled VCS based on commutative patch theory (patches can be applied in any order and produce the same result). Completely different model from git -- not snapshot-based but patch-based.
- Status: Alpha. Small community. Has its own hosting ("The Nest").
- **Why rejected**: Not git-compatible at all. Uses its own repository format. Can import from git but does not use `.git` repos. Adopting Pijul means abandoning the git ecosystem entirely.

### Radicle (Rust)

- Website: https://radicle.xyz
- What it is: Peer-to-peer code collaboration stack built on Git. Adds decentralized identity, gossip protocol, and CRDTs for issues/reviews. v1.6.0 (Jan 2026).
- **Why rejected**: Not a git replacement -- it is a collaboration/hosting layer on top of git. Does not change how staging, committing, or concurrency works. Solves a different problem (decentralized code hosting).

### git-branchless (Rust)

- Repo: https://github.com/arxanas/git-branchless
- What it is: A git extension suite that adds smartlog, undo, revsets, and fast rebase to standard git. 4k stars. Alpha status.
- **Why rejected**: Adds useful UX features to git but does not address staging races or concurrent agent safety. `git undo` could help recover from agent mistakes but that's reactive, not preventive. The author (Waleed Khan) is now a Jujutsu maintainer, which says something about where the ideas are converging.


## Conclusion

For the specific problem of making Git safe for concurrent AI agent sessions, only two of the four surveyed tools offer architectural solutions. The others are valuable in their own domains but do not address the core staging race condition that arises when multiple agents share a single repository:

- **Jujutsu** solves it architecturally by eliminating the index entirely, using a lock-free operation log, and treating conflicts as data. It is the most principled solution but requires agents to learn `jj` commands instead of `git` commands.
- **GitButler** solves it practically by isolating each agent's work into virtual branches, with explicit AI agent support (MCP server, Claude Code hooks, JSON output). It has the least disruptive adoption path since everything underneath is standard git.
- **Sapling** and **go-git** do not address the concurrency problem. Sapling is interesting for its stacked diffs workflow and Meta heritage but has uncertain community health. go-git is a Go library with critical feature gaps (no merge) that make it unsuitable as a standalone solution.
