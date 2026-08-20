# Reversibility gaps: commands whose inverse is missing

## Context

strictcli is gaining declared reversibility support: a mutating command will
declare which command undoes it (verified at registration in both
directions), a command with no recovery will declare irreversible with a
mandatory reason, a warn-severity check will flag destructive commands
declaring neither, and after a real run the framework will print a paste-able
recovery command and emit a machine-readable recovery member in the JSON
result document. When this repo adopts that support, every gap below needs
either a built inverse or an honest irreversible declaration.

## Problems

1. **`hook install` has no `hook remove`.** The group is install/list/run;
   installing copies a script into the git-dir hook area and nothing removes
   it.
2. **`reset` and `rebase` are reflog-recoverable but no declared command
   exposes the recovery.** The undo machinery covers only the
   commit/amend/reword op family; a reset or rebase that should not have
   happened is recovered by hand today.

## Note for the adoption pass

The scrub family's existing refusal — "cannot undo: history rewrite
invalidated prior oplog entries" — is already the irreversible-with-reason
shape the framework will formalize. Those commands are not gaps; they become
declared-irreversible with that exact reason.

## Effort

Hook remove: small. Reset/rebase recovery: medium (decide whether the oplog
grows those op kinds or a recovery command reads the reflog directly).
