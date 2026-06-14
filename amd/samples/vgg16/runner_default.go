//go:build !fattree && !mesh

package main

import "github.com/sarchlab/mgpusim/v4/amd/samples/runner"

// newRunner builds the default platform runner (no topology tag).
func newRunner() topoRunner {
	return new(runner.Runner).Init()
}
