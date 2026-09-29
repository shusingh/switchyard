package sim

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestDeviceSerializesSteps(t *testing.T) {
	t.Parallel()
	const step = 40 * time.Millisecond
	cases := []struct {
		name   string
		device *Device
		min    time.Duration
		max    time.Duration
	}{
		{name: "shared", device: NewDevice(), min: 3 * step, max: 6 * step},
		{name: "dedicated", device: nil, min: step, max: 2*step + step/2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			start := time.Now()
			var wg sync.WaitGroup
			for range 3 {
				wg.Go(func() {
					if !tc.device.execute(context.Background(), step) {
						t.Error("execute returned false without cancellation")
					}
				})
			}
			wg.Wait()
			if elapsed := time.Since(start); elapsed < tc.min || elapsed > tc.max {
				t.Errorf("three concurrent steps took %v, want between %v and %v", elapsed, tc.min, tc.max)
			}
		})
	}
}

func TestDeviceWaitHonorsCancellation(t *testing.T) {
	t.Parallel()
	dev := NewDevice()
	held := make(chan struct{})
	release := make(chan struct{})
	go func() {
		dev.slot <- struct{}{}
		close(held)
		<-release
		<-dev.slot
	}()
	<-held
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if dev.execute(ctx, time.Hour) {
		t.Fatal("execute on a busy device returned true after its context ended")
	}
}
