package a13n

import (
	"context"
	"fmt"
	"iter"
)

// Pages makes one page request at a time, retaining each page's status and
// headers. Fetch uses the generated low-level operation and ParseJSON; stopping
// iteration stops further requests. An empty next cursor ends the sequence.
func Pages[T any](ctx context.Context, first string, fetch func(context.Context, *string) (Result[T], error), next func(T) string) iter.Seq2[Result[T], error] {
	return func(yield func(Result[T], error) bool) {
		var cursor *string
		if first != "" {
			cursor = &first
		}
		seen := map[string]bool{}
		if first != "" {
			seen[first] = true
		}
		for {
			if err := ctx.Err(); err != nil {
				yield(Result[T]{}, err)
				return
			}
			page, err := fetch(ctx, cursor)
			if err != nil {
				yield(page, err)
				return
			}
			id := next(page.Value)
			if !yield(page, nil) || id == "" {
				return
			}
			if seen[id] {
				yield(Result[T]{}, fmt.Errorf("%w: repeated pagination cursor", ErrProtocol))
				return
			}
			seen[id] = true
			cursor = &id
		}
	}
}
