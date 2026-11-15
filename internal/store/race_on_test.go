//go:build race

package store

// raceEnabled is true under -race, where the 100k-placement test is skipped
// (it takes about 80 s with the race detector and tests no new code path).
const raceEnabled = true
