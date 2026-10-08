// Written by rlsbl scaffold. The go command never descends into a directory
// that declares its own module, so this file is what keeps `go test ./...`
// and `go build ./...` out of this scratch directory: a half-finished probe
// left here cannot break the project's suite.
//
// The path is under a reserved TLD and is deliberately unreachable -- nothing
// publishes, fetches or imports this module. It carries no `go` directive, so
// there is no toolchain version here to go stale.
module scratch.invalid/rlsbl-scratch
