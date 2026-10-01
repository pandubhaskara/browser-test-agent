package main

import "runtime/debug"

// playwrightModuleVersion is read from the build's module list, so `version`
// and `doctor` report the driver version actually compiled in rather than a
// constant someone has to remember to bump.
var playwrightModuleVersion = func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range info.Deps {
		if dep.Path == "github.com/mxschmitt/playwright-go" {
			return dep.Version
		}
	}
	return "unknown"
}()
