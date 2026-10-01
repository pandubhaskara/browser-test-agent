// Package browser launches Playwright browsers the way a test's options ask
// for, and watches their network traffic.
package browser

import (
	"fmt"

	"github.com/mxschmitt/playwright-go"

	"browser-test-agent/pkg/spec"
)

// contextOptionsForDevice fills the viewport part of a context from a device
// preset. Mobile presets also switch on touch and the mobile meta-viewport,
// which is what makes a responsive site render its phone layout instead of a
// shrunken desktop one.
func contextOptionsForDevice(device string, opts *playwright.BrowserNewContextOptions) error {
	vp, ok := spec.DeviceViewports[device]
	if !ok {
		return fmt.Errorf("unknown device %q", device)
	}
	opts.Viewport = &playwright.Size{Width: vp.Width, Height: vp.Height}
	if vp.Mobile {
		opts.IsMobile = playwright.Bool(true)
		opts.HasTouch = playwright.Bool(true)
	}
	return nil
}
