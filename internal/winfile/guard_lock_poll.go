//go:build !windows

package winfile

import (
	"context"
	"os"
	"time"
)

const guardRetryInterval = 5 * time.Millisecond

func lockGuard(ctx context.Context, handle *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		locked, err := tryGuardLock(handle)
		if err != nil {
			return err
		}
		if locked {
			return nil
		}
		timer := time.NewTimer(guardRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
