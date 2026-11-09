package main

// lookupChunk caps the ids in one IN list, well under PostgreSQL's 65535
// bind parameters.
const lookupChunk = 5000

// eachChunk calls fn on consecutive slices of items, at most size long, in
// order, and stops at the first error. Every loop that cuts a list to fit a
// query's bind-parameter limit, or to bound one transaction, goes through it.
func eachChunk[T any](items []T, size int, fn func([]T) error) error {
	for len(items) > 0 {
		n := min(len(items), size)
		if err := fn(items[:n]); err != nil {
			return err
		}
		items = items[n:]
	}
	return nil
}

func eachIDChunk(ids []uint, fn func([]uint) error) error {
	return eachChunk(ids, lookupChunk, fn)
}
