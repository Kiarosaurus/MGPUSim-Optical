//go:build fattree

package main

import runner "github.com/sarchlab/mgpusim/v4/amd/samples/runner_fattree_pcie"

// newRunner builds the fat-tree + PCIe platform runner.
// Selected with: go build -tags fattree
func newRunner() topoRunner {
	return new(runner.Runner).Init()
}
