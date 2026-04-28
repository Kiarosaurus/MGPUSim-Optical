package optical

import (
	"fmt"

	"github.com/sarchlab/akita/v4/sim"
)

type CtrlState int

const (
	StateRunning  CtrlState = iota
	StateDraining           // Esperando a los paquetes viajando.
	StateReconfiguring
)

// --- ESTRUCTURA COMPONENT CONTROLADOR ---
type NetworkController struct {
	*sim.TickingComponent

	Switch    *Switch
	Predictor Predictor
	Links     []*Link // Necesitamos control sobre los Links.

	// Tiempos.
	EpochDuration  sim.VTimeInSec // Cada CUÁNTO tiempo reconfiguramos.
	DrainTime      sim.VTimeInSec // Tiempo de seguridad para vaciar cables (ej. 20ns).
	SwitchingDelay sim.VTimeInSec // Tiempo físico que tardan los espejos en moverse (ej. 5us).

	// Control de Estado
	lastEpoch    sim.VTimeInSec
	phaseDoneAt  sim.VTimeInSec // Timestamp para finalizar (Draining o Reconfig).
	currentState CtrlState
	nextTopology TopologyConfig
}

func NewNetworkController(name string, engine sim.Engine, sw *Switch, predictor Predictor, links []*Link, epochFreq sim.Freq) *NetworkController {
	c := &NetworkController{
		TickingComponent: sim.NewTickingComponent(name, engine, epochFreq, nil),
		Switch:           sw,
		Predictor:        predictor,
		Links:            links,

		// TODO. Probar con varios tiempos para encontrar el óptimo.
		EpochDuration:  sim.VTimeInSec(1.0 / float64(epochFreq)),
		DrainTime:      1e-8, // Latencia del link?
		SwitchingDelay: 0,    // Diré que está incluído según Flexfly.

		currentState: StateRunning,
	}
	return c
}

// Sirve para atrapar nuestros eventos de latencia exacta.
func (c *NetworkController) Handle(e sim.Event) error {
	// Si no es la reconfiguración, es el Tick de la época.
	switch evt := e.(type) {
	case *ReconfigEvent:
		return c.processReconfigEvent(evt)
	default:
		return c.TickingComponent.Handle(e)
	}
}

// --- ESTRUCTURA INTERRUPCIÓN HW ---
type ReconfigEvent struct {
	*sim.EventBase
	NextState CtrlState
}

func NewReconfigEvent(time sim.VTimeInSec, handler sim.Handler, next CtrlState) *ReconfigEvent {
	return &ReconfigEvent{
		EventBase: sim.NewEventBase(time, handler),
		NextState: next,
	}
}

// --- LÓGICA DE RECONFIGURACIÓN ---

// Maneja SOLO el funcionamiento normal (inicio de la época).
func (c *NetworkController) Tick(now sim.VTimeInSec) bool {
	// Estamos en reconfiguración. Ignora el reloj.
	if c.currentState != StateRunning {
		return false
	}

	// 1. Recolectar tráfico.
	currentTraffic := c.Switch.GetAndResetTrafficMatrix()
	if len(currentTraffic) == 0 {
		return true // No hay tráfico. Return.
	}

	// 2. Ejecutar Predictor.
	fmt.Printf("[CONTROLLER] Running Predictor %s at %.9f\n", c.Predictor.Name(), now)
	c.nextTopology = c.Predictor.Predict(currentTraffic)

	// 3. Pausamos Links.
	for _, l := range c.Links {
		l.Pause()
	}

	// 4. Drenado.
	c.currentState = StateDraining
	fmt.Printf("[CONTROLLER] Network PAUSED at %.9f. Draining...\n", now)

	// Programamos el evento reconfiguración en 'now + DrainTime'.
	c.Engine.Schedule(NewReconfigEvent(now+c.DrainTime, c, StateReconfiguring))

	return true
}

func (c *NetworkController) processReconfigEvent(evt *ReconfigEvent) error {
	now := evt.Time()

	// Reconfiguración.
	if evt.NextState == StateReconfiguring {
		c.currentState = StateReconfiguring
		fmt.Printf("[CONTROLLER] Draining Done at %.9f. Reconfiguring phase...\n", now)

		// Programamos resumir la comunicación.
		c.Engine.Schedule(NewReconfigEvent(now+c.SwitchingDelay, c, StateRunning))

	} else if evt.NextState == StateRunning { // Resumir post-reconfig.
		c.Switch.TopologyUpdate(c.nextTopology)
		fmt.Printf("[CONTROLLER] Reconfig Done at %.9f. Resuming...\n", now)

		for _, l := range c.Links {
			l.Resume()
		}
		c.currentState = StateRunning
	}
	return nil
}
