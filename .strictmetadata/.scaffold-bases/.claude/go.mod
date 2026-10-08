// Written by rlsbl scaffold. The Go module proxy leaves a directory holding its
// own go.mod out of the module zip, so this file keeps this private directory
// out of every published version of the module, and `go build ./...` and
// `go test ./...` out of it too.
//
// The path is under a reserved TLD and is deliberately unreachable -- nothing
// publishes, fetches or imports this module. It carries no `go` directive, so
// there is no toolchain version here to go stale.
module private.invalid/rlsbl-private
