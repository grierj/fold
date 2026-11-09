package main

// Batch sizes. Each names one constraint; use the constant, not a number.
//
//   - lookupChunk: ids in one IN list, a read or a write by id. The limit is
//     PostgreSQL's 65,535 bind parameters per statement; 5,000 stays far under
//     it for any statement that binds a few values per id.
//   - rowBatch: rows in one multi-row INSERT, or destinations in one consent
//     read. It bounds one statement's size and the work held in one step, and
//     is not a parameter limit.
//   - messageBatchSize (messages_send.go): recipients one message.deliver_batch
//     job sends, which bounds the work one job holds a worker for.
//   - userlock.Batch (internal/userlock): user rows one FOR KEY SHARE
//     statement locks, which bounds how long one statement holds locks.
const (
	lookupChunk = 5000
	rowBatch    = 500
)

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
