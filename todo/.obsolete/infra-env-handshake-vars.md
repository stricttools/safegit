# Declare handshake env vars via strictcli InfraEnv

Declare RLSBL_SCRUB_ORCHESTRATED (scrub_guard.go:29, cross-tool permission handshake) and CLAUDE_CODE_SESSION_ID (trailer.go:28, undo.go:60, oplog.go:44, session audit trail) via strictcli's WithHandshakeEnv primitive.

## Current state

4 raw os.Getenv reads across:
- scrub_guard.go:29 (RLSBL_SCRUB_ORCHESTRATED)
- trailer.go:28 (CLAUDE_CODE_SESSION_ID)
- undo.go:60 (CLAUDE_CODE_SESSION_ID)
- oplog.go:44 (CLAUDE_CODE_SESSION_ID)

## Target state

Both vars declared via WithHandshakeEnv. Raw os.Getenv reads become ctx.InfraValue accessors. Both appear in schema/help with hermetic-immune annotation. Zero behavior change.

## Prerequisites

Requires go-strictcli v0.22.0 (already in go.mod).

## Effort

Small-medium.
