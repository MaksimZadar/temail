package sync

// chunk is one header fetch: the UID window [First, Last] holds Count UIDs
// that are on the server and not yet cached. The window contains no cached UID,
// so a fetch of it never re-downloads committed headers.
type chunk struct {
	First uint32
	Last  uint32
	Count int
}

// planChunks splits the server's UIDs into header fetch windows, newest first.
//
// serverUIDs must be ascending. A cached UID ends the current run of pending
// UIDs, so no window spans a committed message. Each run is cut into windows of
// at most size UIDs, starting from its highest UID.
func planChunks(serverUIDs []uint32, cached map[uint32]bool, size int) (chunks []chunk, pending int) {
	if size < 1 {
		size = 1
	}
	var runs [][]uint32
	var run []uint32
	for _, uid := range serverUIDs {
		if cached[uid] {
			if len(run) > 0 {
				runs = append(runs, run)
				run = nil
			}
			continue
		}
		run = append(run, uid)
	}
	if len(run) > 0 {
		runs = append(runs, run)
	}

	for r := len(runs) - 1; r >= 0; r-- {
		u := runs[r]
		pending += len(u)
		for end := len(u); end > 0; end -= size {
			start := max(0, end-size)
			chunks = append(chunks, chunk{First: u[start], Last: u[end-1], Count: end - start})
		}
	}
	return chunks, pending
}
