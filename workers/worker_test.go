package workers

import (
	"testing"
	"time"
)

func TestRetryDelayForAttempt(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: 30 * time.Second},
		{attempt: 2, want: 2 * time.Minute},
		{attempt: 3, want: 10 * time.Minute},
	}

	for _, tt := range tests {
		if got := retryDelayForAttempt(tt.attempt); got != tt.want {
			t.Fatalf("retryDelayForAttempt(%d) = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}
