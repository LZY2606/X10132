//go:build race

package mempool

// raceEnabled reports whether the test binary is built with the race
// detector. Allocation-heavy stress loops are scaled down in that mode:
// the race detector instruments every memory access and makes
// gigabyte-scale allocation loops take orders of magnitude longer.
const raceEnabled = true
