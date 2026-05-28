package optical

import (
	"sync"

	"github.com/sarchlab/akita/v4/sim"
)

// NetworkController es el encargado de orquestar la reconfiguración óptica.
//
// PROPÓSITO: futuro punto único donde se consolide la política global de switching
// (ej: recibir hints del [[TrafficPredictor]] y emitir EvictLink/preconfig hacia el Switch).
//
// AISLAMIENTO ACTUAL: El Switch sigue siendo autocontenido y reactivo
// on-demand. Cuando esta capa se cablee, se hará a través de hooks
// (probablemente EvictLink / RegisterRoute) SIN TOCAR la máquina de estados de linkEntry.
type NetworkController struct {
	mu        sync.RWMutex
	switches  []*Switch
	predictor *TrafficPredictor
}

// NewNetworkController construye un controller vacío.
// El predictor es opcional; si es nil, el controller solo expone el registry de switches.
func NewNetworkController(predictor *TrafficPredictor) *NetworkController {
	return &NetworkController{
		switches:  make([]*Switch, 0, 4),
		predictor: predictor,
	}
}

// AttachSwitch añade un Switch bajo la órbita del controller.
// No instala hooks ni interfiere con el Switch en este estado del refactor.
func (c *NetworkController) AttachSwitch(s *Switch) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.switches = append(c.switches, s)
}

// Predictor expone el TrafficPredictor asociado (puede ser nil).
func (c *NetworkController) Predictor() *TrafficPredictor {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.predictor
}

// SwitchCount devuelve el número de switches registrados.
func (c *NetworkController) SwitchCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.switches)
}

// SuggestPreconfig consulta al predictor por el siguiente destino probable desde src.
// NO emite reconfig real. Devuelve la decisión que el wiring futuro consumiría.
func (c *NetworkController) SuggestPreconfig(src sim.RemotePort) (sim.RemotePort, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.predictor == nil {
		return "", false
	}
	return c.predictor.PredictNextDestination(src)
}
