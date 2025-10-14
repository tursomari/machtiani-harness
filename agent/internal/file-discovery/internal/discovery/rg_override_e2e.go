//go:build e2e_slow_rg

package discovery

import (
	"context"
	"time"
)

// In e2e tests built with -tags e2e_slow_rg, override the RG runner to simulate a slow command
// that exceeds the provided context deadline.
func init() {
	runRGFilesFn = func(ctx context.Context) ([]string, rgStats, error) {
		// Wait until context expires
		start := time.Now()
		<-ctx.Done()
		// Report a duration at least equal to the timeout
		return nil, rgStats{duration: time.Since(start), totalLines: 0}, context.DeadlineExceeded
	}
}
