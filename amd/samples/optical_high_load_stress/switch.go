package main

import (
	"fmt"
	"math"

	"github.com/sarchlab/akita/v4/sim"
	"github.com/sarchlab/akita/v4/tracing"
)

// LinkKey identifica un directed optical path (src->dst).
type LinkKey struct {
	Src sim.RemotePort
	Dst sim.RemotePort
}

func (k LinkKey) String() string { return fmt.Sprintf("%s->%s", k.Src, k.Dst) }

type linkState int

const (
	linkConnected linkState = iota
	// linkDraining: el srcPort cambió de dst. El MZI todavía no se reconfigura.
	// Estamos esperando que los paquetes ya inyectados a la fibra hacia el
	// dst viejo terminen de salir. Durante esta ventana no se admiten nuevos
	// paquetes en el enlace viejo (ver processOutgoing).
	linkDraining
	linkReconfiguring
)

// stressSwitch refleja el optical.Switch de producción standalone.
// Modela tres fenómenos físicos:
//   - Reconfig (820 ns): cambio de fase del MZI hacia el nuevo dst.
//   - Drain    ( 21 ns): guard window pre-reconfig cuando el srcPort cambia
//     de dst y todavía hay un circuito vivo.
//   - Fibra    ( 10 ns): propagación de la luz por la fibra; paquetes en
//     pipelined transit gestionados con FiberDeliverEvent.
//
// Parche de precisión por ciclos enteros (Freq.NCyclesLater) intacto.
type stressSwitch struct {
	*sim.TickingComponent
	engine         sim.Engine
	reconfigCycles int // conteo entero de ciclos para el reconfig MZI
	drainCycles    int // conteo entero de ciclos para el drain pre-reconfig
	fiberCycles    int // conteo entero de ciclos para la propagación en fibra
	portsByRemote  map[sim.RemotePort]sim.Port
	linkStates     map[LinkKey]linkState
	// currentDstBySrc registra, para cada srcPort que ya tuvo un circuito
	// establecido, hacia qué dst está actualmente conectado. Necesario para
	// detectar la condición "srcPort quiere transmitir a un dst distinto al
	// que ya tiene activo" -> trigger del drain. Si el srcPort nunca tuvo
	// circuito (primer paquete), no aparece aquí y se llama a startReconfig
	// directamente, sin drain.
	currentDstBySrc map[sim.RemotePort]sim.RemotePort
}

func newStressSwitch(
	name string,
	engine sim.Engine,
	reconfigDelay sim.VTimeInSec,
	drainDelay sim.VTimeInSec,
	fiberDelay sim.VTimeInSec,
) *stressSwitch {
	const switchFreq = 1 * sim.GHz
	s := &stressSwitch{
		engine:          engine,
		reconfigCycles:  int(math.Round(float64(reconfigDelay) * float64(switchFreq))),
		drainCycles:     int(math.Round(float64(drainDelay) * float64(switchFreq))),
		fiberCycles:     int(math.Round(float64(fiberDelay) * float64(switchFreq))),
		portsByRemote:   make(map[sim.RemotePort]sim.Port),
		linkStates:      make(map[LinkKey]linkState),
		currentDstBySrc: make(map[sim.RemotePort]sim.RemotePort),
	}
	s.TickingComponent = sim.NewSecondaryTickingComponent(name, engine, switchFreq, s)
	return s
}

// -- interfaz sim.Connection --------------------------------------------------

func (s *stressSwitch) PlugIn(port sim.Port) {
	s.portsByRemote[port.AsRemote()] = port
	port.SetConnection(s)
}

func (s *stressSwitch) Unplug(port sim.Port) {
	delete(s.portsByRemote, port.AsRemote())
}

func (s *stressSwitch) NotifyAvailable(_ sim.Port) { s.TickNow() }
func (s *stressSwitch) NotifySend()                { s.TickNow() }

// -- override de sim.Handler --------------------------------------------------

func (s *stressSwitch) Handle(e sim.Event) error {
	if evt, ok := e.(ReconfigCompleteEvent); ok {
		return s.handleReconfigComplete(evt)
	}
	if evt, ok := e.(DrainCompleteEvent); ok {
		return s.handleDrainComplete(evt)
	}
	if evt, ok := e.(FiberDeliverEvent); ok {
		return s.handleFiberDeliver(evt)
	}
	if s.Tick() {
		s.TickLater()
	}
	return nil
}

// -- sim.Ticker ----------------------------------------------------------------

func (s *stressSwitch) Tick() bool {
	madeProgress := false
	for _, port := range s.portsByRemote {
		if s.processOutgoing(port) {
			madeProgress = true
		}
	}
	return madeProgress
}

// processOutgoing emite COMO MUCHO UN paquete por Tick a la fibra óptica.
//
// Modelo en dos fases por paquete:
//
//  1. Transmisión (bandwidth)  — 1 paquete por ciclo del switch.
//     El switch corre a f_switch = 1 GHz, así cada ciclo (1 ns) puede
//     "poner en la fibra" exactamente 1 paquete. Si quedan paquetes
//     pendientes retornamos madeProgress=true y TickingComponent programa
//     otro Tick 1 ciclo después.
//
//  2. Propagación (fiber)      — T_fiber ns por paquete (en vuelo).
//     En vez de Deliver síncrono programamos un FiberDeliverEvent que
//     dispara a t = now + fiberCycles. Cuando dispara, el paquete llega
//     al puerto destino. Múltiples paquetes pueden estar en vuelo a la
//     vez (pipelined): hasta fiberCycles / 1ciclo = fiberCycles paquetes.
func (s *stressSwitch) processOutgoing(srcPort sim.Port) bool {
	head := srcPort.PeekOutgoing()
	if head == nil {
		return false
	}

	srcRemote := srcPort.AsRemote()
	key := LinkKey{Src: srcRemote, Dst: head.Meta().Dst}

	state, exists := s.linkStates[key]
	if !exists {
		// El circuito (src,dst) pedido no existe aún. Dos sub-casos:
		//   A) Este srcPort NUNCA estuvo conectado a nada: arrancamos reconfig
		//      directo, sin drain. No hay fotones en vuelo que proteger.
		//   B) Este srcPort YA tenía un circuito hacia otro dst: tenemos que
		//      drenar primero (guard window) antes de reconfigurar el MZI, si no
		//      cortamos a la mitad cualquier paquete que ya esté en la fibra.
		oldDst, hadCircuit := s.currentDstBySrc[srcRemote]
		if !hadCircuit {
			s.startReconfig(srcPort, key)
			return false
		}
		oldKey := LinkKey{Src: srcRemote, Dst: oldDst}
		s.startDrain(srcPort, oldKey, key)
		return false
	}
	if state == linkDraining || state == linkReconfiguring {
		return false
	}

	dstPort, ok := s.portsByRemote[key.Dst]
	if !ok {
		fmt.Printf("[Switch] unknown dst %s — dropping\n", key.Dst)
		srcPort.RetrieveOutgoing()
		return true // drop también consume el ciclo
	}

	// El paquete sale del buffer del switch y entra a la fibra. La llegada
	// al puerto destino se programa como un evento futuro a now + T_fiber.
	// RetrieveOutgoing libera el slot del switch para que el siguiente Tick
	// pueda transmitir el siguiente paquete.
	srcPort.RetrieveOutgoing()

	now := s.engine.CurrentTime()
	fireAt := s.Freq.NCyclesLater(s.fiberCycles, now)

	taskID := sim.GetIDGenerator().Generate()
	tracing.StartTaskWithSpecificLocation(
		taskID, "", s, "fiber_transit", "optical_fiber", key.String(), nil,
	)

	evt := FiberDeliverEvent{
		Msg:     head,
		DstPort: dstPort,
		TaskID:  taskID,
	}
	evt.EventBase = *sim.NewEventBase(fireAt, s)
	s.engine.Schedule(evt)

	return true
}

// handleFiberDeliver entrega un paquete que terminó su tránsito por la fibra.
// Si el puerto destino está lleno re-programamos el evento 1 ciclo después —
// el paquete sigue on flight en la cola de entrada, no se pierde.
func (s *stressSwitch) handleFiberDeliver(e FiberDeliverEvent) error {
	if err := e.DstPort.Deliver(e.Msg); err != nil {
		now := s.engine.CurrentTime()
		fireAt := s.Freq.NCyclesLater(1, now)
		e.EventBase = *sim.NewEventBase(fireAt, s)
		s.engine.Schedule(e)
		return nil
	}
	tracing.EndTask(e.TaskID, s)
	return nil
}

// startDrain abre la ventana de drenado del circuito viejo antes de reconfigurar
// el MZI al nuevo dst. Marca el linkState del oldKey como linkDraining y programa
// un DrainCompleteEvent que llevará al reconfig real.
func (s *stressSwitch) startDrain(
	srcPort sim.Port, oldKey LinkKey, newKey LinkKey,
) {
	s.linkStates[oldKey] = linkDraining
	now := s.engine.CurrentTime()

	taskID := sim.GetIDGenerator().Generate()
	tracing.StartTask(taskID, "", s, "drain", "optical_drain", nil)

	fireAt := s.Freq.NCyclesLater(s.drainCycles, now)

	evt := DrainCompleteEvent{
		SrcPort: srcPort,
		OldKey:  oldKey,
		NewKey:  newKey,
		TaskID:  taskID,
	}
	evt.EventBase = *sim.NewEventBase(fireAt, s)
	s.engine.Schedule(evt)

	fmt.Printf("[Switch] t=%.9fs  DRAIN_START     %s  delay=%dns\n",
		float64(now), oldKey, s.drainCycles)
}

// handleDrainComplete cierra el drenado: el circuito viejo se da por terminado
// (delete de linkStates) y se arranca el reconfig hacia el nuevo dst. El
// mensaje que disparó la cadena sigue esperando en el outport del srcPort.
// El TickNow al final de handleReconfigComplete lo procesará después.
func (s *stressSwitch) handleDrainComplete(e DrainCompleteEvent) error {
	delete(s.linkStates, e.OldKey)
	tracing.EndTask(e.TaskID, s)

	now := s.engine.CurrentTime()
	fmt.Printf("[Switch] t=%.9fs  DRAIN_END       %s\n", float64(now), e.OldKey)

	s.startReconfig(e.SrcPort, e.NewKey)
	return nil
}

func (s *stressSwitch) startReconfig(srcPort sim.Port, key LinkKey) {
	s.linkStates[key] = linkReconfiguring
	now := s.engine.CurrentTime()

	taskID := sim.GetIDGenerator().Generate()
	tracing.StartTask(taskID, "", s, "reconfig", "optical_reconfig", nil)

	// Freq.NCyclesLater: parche de precisión por ciclos enteros — NO modificar.
	fireAt := s.Freq.NCyclesLater(s.reconfigCycles, now)

	evt := ReconfigCompleteEvent{}
	evt.EventBase = *sim.NewEventBase(fireAt, s)
	evt.Key = key
	evt.TaskID = taskID
	s.engine.Schedule(evt)

	fmt.Printf("[Switch] t=%.9fs  RECONFIG_START  %s  delay=%dns\n",
		float64(now), key, s.reconfigCycles)
}

func (s *stressSwitch) handleReconfigComplete(e ReconfigCompleteEvent) error {
	s.linkStates[e.Key] = linkConnected
	// Registrar el nuevo circuito activo para este srcPort. Necesario para que
	// el próximo cambio de dst dispare drain y no reconfig directo.
	s.currentDstBySrc[e.Key.Src] = e.Key.Dst
	tracing.EndTask(e.TaskID, s)

	now := s.engine.CurrentTime()
	fmt.Printf("[Switch] t=%.9fs  RECONFIG_END    %s\n", float64(now), e.Key)

	s.TickNow()
	return nil
}
