package optical

import (
	"sync"

	"github.com/sarchlab/akita/v4/sim"
)

// TrafficPredictor es un esqueleto aislado para análisis predictivo de tráfico inter-GPU.
//
// PROPÓSITO: registra historial de transacciones (src->dst) para inferir el próximo destino
// probable y, eventualmente, alimentar al NetworkController para prefetch de reconfiguración.
//
// AISLAMIENTO ACTUAL: NACE DESCONECTADO. No es invocado por el Switch de producción
// ni por el Connector. No altera la reactividad on-demand actual del OpticalSwitch.
type TrafficPredictor struct {
	mu      sync.RWMutex
	history []transaction
	counts  map[sim.RemotePort]map[sim.RemotePort]int // src -> dst -> freq
}

type transaction struct {
	Src sim.RemotePort
	Dst sim.RemotePort
}

// NewTrafficPredictor construye un predictor vacío.
func NewTrafficPredictor() *TrafficPredictor {
	return &TrafficPredictor{
		history: make([]transaction, 0, 1024),
		counts:  make(map[sim.RemotePort]map[sim.RemotePort]int),
	}
}

// RecordTransaction registra una transacción observada src->dst.
// TODO. Invocable desde hooks de tracing en una integración futura.
func (p *TrafficPredictor) RecordTransaction(src, dst sim.RemotePort) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.history = append(p.history, transaction{Src: src, Dst: dst})
	if _, ok := p.counts[src]; !ok {
		p.counts[src] = make(map[sim.RemotePort]int)
	}
	p.counts[src][dst]++
}

// PredictNextDestination devuelve el destino más frecuente observado desde src.
// Retorna ("", false) si no hay historial para src.
// TODO. política de predicción trivial (reemplazable por modelos más complejos).
func (p *TrafficPredictor) PredictNextDestination(src sim.RemotePort) (sim.RemotePort, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	dsts, ok := p.counts[src]
	if !ok || len(dsts) == 0 {
		return "", false
	}

	var bestDst sim.RemotePort
	bestCount := -1
	for dst, c := range dsts {
		if c > bestCount {
			bestCount = c
			bestDst = dst
		}
	}
	return bestDst, true
}

// HistoryLen devuelve el número de transacciones registradas (introspección/tests).
func (p *TrafficPredictor) HistoryLen() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.history)
}
