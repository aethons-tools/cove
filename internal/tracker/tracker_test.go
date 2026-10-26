package tracker

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestRateLimitErrorUnwrapsAndReads(t *testing.T) {
	err := fmt.Errorf("poll: %w", &RateLimitError{Service: KindGitHub, RetryAfter: 90 * time.Second})
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 90*time.Second {
		t.Fatalf("errors.As = %v, %+v", err, rl)
	}
	if got := rl.Error(); got != "github: rate limited; retry after 1m30s" {
		t.Fatalf("Error() = %q", got)
	}
}
