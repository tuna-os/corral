package web

import (
	"sync"
	"testing"
	"time"
)

// TestBridgeCopies_JoinsBothDirections proves the bridge helper observes
// BOTH copy completions: with the first direction finished and the second
// blocked, the helper must still be waiting — only the second's retirement
// returns it. (Joining one copy and returning would abandon the other
// goroutine to an unobserved fate.)
func TestBridgeCopies_JoinsBothDirections(t *testing.T) {
	release := make(chan struct{})
	secondStarted := make(chan struct{})
	secondDone := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	go func() {
		bridgeCopies(
			func() {},
			func() {
				once.Do(func() { close(secondStarted) })
				<-release
				close(secondDone)
			},
			func() {},
		)
		close(done)
	}()
	// Both copies are running and the first has long finished: the helper
	// must still be waiting on the second.
	select {
	case <-secondStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("second copy never started")
	}
	time.Sleep(100 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("bridge helper returned while the second copy was still blocked")
	default:
	}
	// Releasing the second retires the helper.
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("bridge helper did not return after both copies retired")
	}
	select {
	case <-secondDone:
	default:
		t.Fatal("bridge helper returned without observing the second copy")
	}
}
