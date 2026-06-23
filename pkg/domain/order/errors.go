package order

import "errors"

var (
	// ErrShortRegulated indicates a symbol has sell-short restrictions (Code 100302).
	ErrShortRegulated = errors.New("short entry regulated (100302)")

	// ErrOrderSkipped indicates the order was locally suppressed or skipped.
	ErrOrderSkipped = errors.New("order skipped locally")

	// ErrDispatchQueueBypass indicates the order was overwritten or canceled in the dispatch queue.
	ErrDispatchQueueBypass = errors.New("order bypassed in dispatch queue")
)
