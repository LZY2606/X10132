//go:build race

package mempool

// raceEnabled reports whether the race detector is enabled. It is used to
// scale down allocation sizes in tests so that `go test -race` stays fast:
// gigabyte-scale buffers make the race runtime map gigabytes of shadow
// memory, which turns a sub-second test into a multi-minute one.
const raceEnabled = true
