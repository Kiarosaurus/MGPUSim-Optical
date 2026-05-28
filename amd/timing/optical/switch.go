package optical

import (
	"fmt"
	"math"

	"github.com/sarchlab/akita/v4/sim"
	"github.com/sarchlab/akita/v4/tracing"
)

// OpticalPacket re-firma un mensaje reenviado para que el puerto de salida del Switch
// sea el remitente declarado. Akita valida que Src coincida con el puerto emisor,
// por lo que el Switch debe envolver el mensaje original en lugar de mutar su Src.
type OpticalPacket struct {
	sim.MsgMeta
	InnerMsg sim.Msg
}

func (p *OpticalPacket) Meta() *sim.MsgMeta { return &p.MsgMeta }

func (p *OpticalPacket) Clone() sim.Msg {
	n := *p
	n.ID = sim.GetIDGenerator().Generate()
	if p.InnerMsg != nil {
		n.InnerMsg = p.InnerMsg.Clone()
	}
	return &n
}

// ReconfigCompleteEvent es programado por el Switch cuando inicia la
// reconfiguración óptica para un destino. Se dispara después de SwitchingDelay.
type ReconfigCompleteEvent struct {
	sim.EventBase
	Dst    sim.RemotePort
	TaskID string
}

type linkState int

const (
	linkConnected     linkState = iota // photonic path activo; mensajes fluyen
	linkReconfiguring                  // switching en progreso; mensajes en stall
)

type linkEntry struct {
	state   linkState
	outPort sim.Port
}

// InputBufferCapacity está dimensionado para absorber el máximo de datos en vuelo durante una
// ventana de reconfiguración de 820 ns a 32 GB/s: ~26 KB / 64 B por paquete =~ 410.
const InputBufferCapacity = 512
const OutputBufferCapacity = 16

// Switch es un switch óptico dinámicamente reconfigurable. Toda la lógica de ruteo y
// el estado de reconfiguración son autocontenidos; no se requiere un Controller o Predictor
// externo. La reconfiguración se activa on-demand: el primer mensaje para un destino
// desconocido o IDLE inicia un ReconfigCompleteEvent
// programado en currentTime + SwitchingDelay.
type Switch struct {
	*sim.TickingComponent
	SwitchingDelay sim.VTimeInSec

	connectedPorts []sim.Port
	PhysicalMap    map[sim.RemotePort]sim.Port   // dst -> puerto de salida físico (estático)
	linkStates     map[sim.RemotePort]*linkEntry // dst -> estado actual del enlace (dinámico)
}

// NewSwitch construye un switch óptico con reconfiguración on-demand.
// switchingDelay es el tiempo de switching fotónico (p. ej., 820 ns para conmutación MZI).
func NewSwitch(name string, engine sim.Engine, switchingDelay sim.VTimeInSec) *Switch {
	s := &Switch{
		SwitchingDelay: switchingDelay,
		PhysicalMap:    make(map[sim.RemotePort]sim.Port),
		linkStates:     make(map[sim.RemotePort]*linkEntry),
	}
	s.TickingComponent = sim.NewSecondaryTickingComponent(name, engine, 1*sim.GHz, s)
	return s
}

// CreatePort agrega un puerto de entrada/salida al switch.
func (s *Switch) CreatePort(name string) sim.Port {
	port := sim.NewPort(s, InputBufferCapacity, OutputBufferCapacity, name)
	s.connectedPorts = append(s.connectedPorts, port)
	return port
}

// RegisterDestination registra el puerto de salida físico para dst y marca el
// enlace como CONNECTED inmediatamente (ruta preconfigurada, sin reconfig al primer uso).
// Usar cuando el photonic path ya está establecido antes de la simulación.
func (s *Switch) RegisterDestination(dst sim.RemotePort, outPort sim.Port) {
	s.PhysicalMap[dst] = outPort
	s.linkStates[dst] = &linkEntry{state: linkConnected, outPort: outPort}
}

// RegisterRoute registra únicamente el puerto de salida físico para dst sin
// preconectar el enlace. El primer mensaje a este destino disparará la
// reconfiguración on-demand (y emitirá el trace task optical_reconfig).
// Usar para cualquier ruta que deba modelar la circuit-setup latency inicial del OCS.
func (s *Switch) RegisterRoute(dst sim.RemotePort, outPort sim.Port) {
	s.PhysicalMap[dst] = outPort
}

// EvictLink elimina la entrada lógica para dst de modo que el próximo mensaje a ese
// destino dispare la reconfiguración on-demand. Usar en tests o benchmarks
// para simular un enlace que fue reclamado para un photonic path diferente.
func (s *Switch) EvictLink(dst sim.RemotePort) {
	delete(s.linkStates, dst)
}

// Tick drena todos los puertos de entrada en orden round-robin.
func (s *Switch) Tick() bool {
	madeProgress := false
	for _, port := range s.connectedPorts {
		if s.processPort(port) {
			madeProgress = true
		}
	}
	return madeProgress
}

// processPort reenvía el mensaje cabeza de inPort si el enlace está CONNECTED.
// Retorna ante el primer stall (dst desconocido, RECONFIGURING, o salida llena).
func (s *Switch) processPort(inPort sim.Port) bool {
	madeProgress := false
	for {
		msg := inPort.PeekIncoming()
		if msg == nil {
			return madeProgress
		}

		dst := msg.Meta().Dst
		entry, exists := s.linkStates[dst]

		if !exists {
			physPort, ok := s.PhysicalMap[dst]
			if !ok {
				fmt.Printf("[SWITCH] Unknown dst %s — dropping msg\n", dst)
				inPort.RetrieveIncoming()
				continue
			}
			s.startReconfig(dst, physPort)
			return madeProgress
		}

		if entry.state == linkReconfiguring {
			return madeProgress
		}

		outPort := entry.outPort
		if !outPort.CanSend() {
			return madeProgress
		}

		inPort.RetrieveIncoming()

		packet := &OpticalPacket{
			MsgMeta: sim.MsgMeta{
				ID:           sim.GetIDGenerator().Generate(),
				Src:          outPort.AsRemote(),
				Dst:          dst,
				TrafficBytes: 16,
				TrafficClass: "OpticalPacket",
			},
			InnerMsg: msg,
		}
		outPort.Send(packet)
		madeProgress = true
	}
}

func (s *Switch) startReconfig(dst sim.RemotePort, physPort sim.Port) {
	now := s.Engine.CurrentTime()
	taskID := sim.GetIDGenerator().Generate()

	s.linkStates[dst] = &linkEntry{state: linkReconfiguring, outPort: physPort}

	tracing.StartTask(taskID, "", s, "reconfig", "optical_reconfig", nil)

	evt := ReconfigCompleteEvent{}
	delayCycles := int(math.Round(float64(s.SwitchingDelay) * float64(s.Freq)))
	fireAt := s.Freq.NCyclesLater(delayCycles, now)
	evt.EventBase = *sim.NewEventBase(fireAt, s)
	evt.Dst = dst
	evt.TaskID = taskID
	s.Engine.Schedule(evt)

	fmt.Printf("[SWITCH] t=%.9fs  RECONFIG_START  dst=%s  delay=%.9fs\n",
		float64(now), dst, float64(s.SwitchingDelay))
}

func (s *Switch) handleReconfigComplete(e ReconfigCompleteEvent) error {
	entry := s.linkStates[e.Dst]
	entry.state = linkConnected
	tracing.EndTask(e.TaskID, s)

	now := s.Engine.CurrentTime()
	fmt.Printf("[SWITCH] t=%.9fs  RECONFIG_END  dst=%s\n", float64(now), e.Dst)

	s.TickNow()
	return nil
}

// Handle intercepta ReconfigCompleteEvents despachados a s.
// Los TickEvents son manejados por el TickingComponent embebido (handler = tc) y
// nunca llegan a este método; invocan s.Tick() a través del ticker pointer almacenado.
func (s *Switch) Handle(e sim.Event) error {
	if evt, ok := e.(ReconfigCompleteEvent); ok {
		return s.handleReconfigComplete(evt)
	}
	if s.Tick() {
		s.TickLater()
	}
	return nil
}

func (s *Switch) NotifyRecv(_ sim.Port) {
	s.TickNow()
}

func (s *Switch) NotifyPortFree(_ sim.Port) {
	s.TickNow()
}
