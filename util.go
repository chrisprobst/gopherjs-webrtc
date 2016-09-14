package main

import (
	"context"
	"fmt"

	"github.com/gopherjs/gopherjs/js"
)

////////////////////////////////////////////////////////////////////////
//////////////////////////////// Util //////////////////////////////////
////////////////////////////////////////////////////////////////////////

type PromiseError struct {
	*js.Object
}

func (e *PromiseError) Error() string {
	return fmt.Sprintf("PromiseError: %s", e.Object.String())
}

func WaitForPromise(ctx context.Context, promise *js.Object) (*js.Object, error) {
	errChan := make(chan *js.Object, 1)
	resChan := make(chan *js.Object, 1)
	promise.Call("then", func(res *js.Object) { resChan <- res })
	promise.Call("catch", func(err *js.Object) { errChan <- err })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-resChan:
		return res, nil
	case err := <-errChan:
		return nil, &PromiseError{err}
	}
}
