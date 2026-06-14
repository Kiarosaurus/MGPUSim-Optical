//go:build mesh

package main

import runner "github.com/sarchlab/mgpusim/v4/amd/samples/runner_mesh_nvlink"

// newRunner crea un platform runner para mesh + NVLink.
// go build -tags mesh
func newRunner() topoRunner {
	return new(runner.Runner).Init()
}
