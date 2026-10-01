package steps

import (
	"errors"
	"time"
)

// pollInterval is how often an assertion re-reads the page while waiting for
// it to become true. Assertions poll rather than read once because the thing
// asserted is usually the result of a fetch the previous step started.
const pollInterval = 250 * time.Millisecond

var errTimedOut = errors.New("timed out")

// pollUntil calls check until it returns true or the step's budget is spent.
// A check that errors ends the poll at once — the error is a problem reading
// the page, not a value that might yet change.
func pollUntil(c *Context, check func() (bool, error)) error {
	deadline := time.Now().Add(c.Timeout)
	for {
		ok, err := check()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return errTimedOut
		}
		select {
		case <-c.Ctx.Done():
			return c.Ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}
