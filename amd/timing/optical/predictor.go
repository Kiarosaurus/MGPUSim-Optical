package optical

import (
	"fmt"
	"sort"
)

// --- ESTRUCTURAS ---
// Tráfico: Src -> Dst -> Bytes.
type TrafficPattern map[string]map[string]uint64

// Topología predecida: Src -> Dst.
type TopologyConfig map[string]string

type Predictor interface {
	Predict(currentTraffic TrafficPattern) TopologyConfig
	Name() string
}

// --- IMPLEMENTACIÓN DUMMY ---
// Conectamos cada nodo con el que más tráfico ha tenido.
type MaxTrafficPredictor struct{}

func (p *MaxTrafficPredictor) Name() string { return "MaxTrafficPredictor" }

func (p *MaxTrafficPredictor) Predict(traffic TrafficPattern) TopologyConfig {
	newTopo := make(TopologyConfig)

	fmt.Println("[PREDICTOR] Analyzing traffic patterns...")

	// 1. Ordenar los Src.
	var srcList []string
	for src := range traffic {
		srcList = append(srcList, src)
	}
	sort.Strings(srcList)

	// Para cada Src...
	for _, src := range srcList {
		mapDstBytes := traffic[src]
		var maxBytes uint64
		var bestDst string

		// 2. Ordenar los Dst.
		var dstList []string
		for dst := range mapDstBytes {
			dstList = append(dstList, dst)
		}
		sort.Strings(dstList)

		// ...buscamos el Dst con más Bytes.
		for _, dst := range dstList {
			bytes := mapDstBytes[dst]

			// Agarramos el primero de la lista.
			if bytes > maxBytes {
				maxBytes = bytes
				bestDst = dst
			}
		}

		if bestDst != "" {
			newTopo[src] = bestDst
		}
	}
	return newTopo
}
