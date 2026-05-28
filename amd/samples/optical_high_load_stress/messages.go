package main

import "github.com/sarchlab/akita/v4/sim"

// DataMsg payload de tamaño fijo enrutado a través del optical switch.
type DataMsg struct {
	sim.MsgMeta
	SeqNum int
	Phase  int
}

func (m *DataMsg) Meta() *sim.MsgMeta { return &m.MsgMeta }
func (m *DataMsg) Clone() sim.Msg {
	c := *m
	c.ID = sim.GetIDGenerator().Generate()
	return &c
}

// SendEvent dispara la inyección de un paquete desde una GPU hacia DstPort.
type SendEvent struct {
	sim.EventBase
	DstPort sim.RemotePort
	SeqNum  int
	Phase   int
}

// ReconfigCompleteEvent programado por el switch cuando transcurre el SwitchingDelay.
type ReconfigCompleteEvent struct {
	sim.EventBase
	Key    LinkKey
	TaskID string
}

// DrainCompleteEvent programado por el switch cuando termina la ventana de
// drenado del circuito óptico viejo. Modela el guard window físico necesario
// antes de que el MZI empiece a reconfigurarse: los fotones ya inyectados a la fibra
// hacia el destino anterior necesitan terminar de salir. Si arrancáramos el
// reconfig de inmediato esos paquetes se perderían.
// Cuando dispara este evento, el switch transita el link de Draining
// a Reconfiguring y programa el ReconfigCompleteEvent.
type DrainCompleteEvent struct {
	sim.EventBase
	SrcPort sim.Port
	OldKey  LinkKey // circuito que estaba activo, lo desmontamos
	NewKey  LinkKey // circuito que vamos a establecer después del reconfig
	TaskID  string  // tarea tracing del drenado, se cierra aquí
}

// FiberDeliverEvent representa un paquete en tránsito por la fibra óptica.
// El switch lo programa en t = now + T_fiber al momento de "transmitir" el
// paquete. Cuando dispara, el paquete llega al puerto destino (Deliver). Esto
// modela la latencia de propagación de la luz por la fibra (T_fiber =~ 10 ns
// para 2 m de fibra intra-rack, asumiendo c/1.5 por índice de refracción).
//
// La transmisión es pipelined: el switch puede emitir 1 paquete por ciclo
// (1 ns @ 1 GHz) mientras hasta T_fiber/T_tick paquetes están on-flight.
type FiberDeliverEvent struct {
	sim.EventBase
	Msg     sim.Msg
	DstPort sim.Port
	TaskID  string // ID de la tarea tracing del tránsito en fibra
}
