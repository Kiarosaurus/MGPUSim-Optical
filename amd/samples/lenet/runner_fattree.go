//go:build fattree

package main

import runner "github.com/sarchlab/mgpusim/v4/amd/samples/runner_fattree_pcie"

// newRunner crea un platform runner para fat-tree + PCIe.
// go build -tags fattree
func newRunner() topoRunner {
	return new(runner.Runner).Init()
}
