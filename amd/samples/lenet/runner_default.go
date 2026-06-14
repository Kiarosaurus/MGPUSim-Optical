//go:build !fattree && !mesh

package main

import "github.com/sarchlab/mgpusim/v4/amd/samples/runner"

// newRunner crea default platform runner (sin topology tag).
func newRunner() topoRunner {
	return new(runner.Runner).Init()
}
