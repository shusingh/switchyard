package sim

import (
	"context"
	"time"
)

// Device is an accelerator shared by several engines. Engines on one Device
// execute their steps one at a time, as separate processes time-slicing a
// single GPU do, so a request routed to an idle replica on a busy device
// still waits for the device.
//
// A nil *Device is valid and means the engine has dedicated compute.
type Device struct {
	slot chan struct{}
}

// NewDevice returns a Device with no engines attached.
func NewDevice() *Device {
	return &Device{slot: make(chan struct{}, 1)}
}

// execute holds the device for d and reports whether it did so without ctx
// ending first.
func (dev *Device) execute(ctx context.Context, d time.Duration) bool {
	if dev == nil {
		return sleep(ctx, d)
	}
	select {
	case dev.slot <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	defer func() { <-dev.slot }()
	return sleep(ctx, d)
}
