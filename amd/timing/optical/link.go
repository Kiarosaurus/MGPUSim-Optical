package optical

import (
	"fmt"
	"log"
	"sync"

	"github.com/sarchlab/akita/v4/sim"
)

// -- EVENTO : *Entrega de luz llegando* --
type LinkDeliveryEvent struct {
	*sim.EventBase // Cumpliendo con interfaz Event.
	Msg            sim.Msg
	DstPort        sim.Port
}

func NewLinkDeliveryEvent(time sim.VTimeInSec, handler sim.Handler, msg sim.Msg, dst sim.Port) *LinkDeliveryEvent {
	return &LinkDeliveryEvent{
		EventBase: sim.NewEventBase(time, handler),
		Msg:       msg,
		DstPort:   dst,
	}
}

// Metiendo un mensaje en el INPUT BUFFER del PORT DESTINO.
func (e *LinkDeliveryEvent) Execute(engine sim.Engine) error {
	err := e.DstPort.Deliver(e.Msg)

	if err != nil {
		retryEvent := NewLinkDeliveryEvent(e.Time()+1e-9, e.Handler(), e.Msg, e.DstPort)

		engine.Schedule(retryEvent)
		return nil
	}

	return nil
}

// --- DEFINICIÓN DEL COMPONENTE FIBRA ÓPTICA ---
type Link struct {
	*sim.TickingComponent // Reloj. Da la base.

	SideA   sim.Port
	SideB   sim.Port
	Latency sim.VTimeInSec

	// Estados para la reconfiguración.
	paused     bool
	pausedLock sync.Mutex
}

// TODO. Justo ahora la latencia es estática y el bandwidth infinito.
func NewLink(name string, engine sim.Engine, latency sim.VTimeInSec) *Link {
	l := &Link{
		TickingComponent: sim.NewTickingComponent(name, engine, 1*sim.GHz, nil),
		Latency:          latency,
		paused:           false,
	}
	return l
}

// Cuando el motor despierta al componente...
func (l *Link) Handle(e sim.Event) error {
	switch evt := e.(type) {
	case *LinkDeliveryEvent: // Es nuestro evento de entrega.
		return evt.Execute(l.Engine)
	default: // Otra cosa = tick.
		return l.TickingComponent.Handle(e)
	}
}

// --- LÓGICA DE PAUSA POR RECONFIGURACIÓN ---

func (l *Link) Pause() {
	l.pausedLock.Lock()
	defer l.pausedLock.Unlock()

	l.paused = true
	fmt.Printf("[LINK] %s Paused.\n", l.Name())
}

func (l *Link) Resume() {
	l.pausedLock.Lock()
	defer l.pausedLock.Unlock()

	l.paused = false
	fmt.Printf("[LINK] %s Resumed.\n", l.Name())

	l.NotifySend() // Revisamos lo pendiente.
}

// --- REQUISITOS INTERFACE CONNECTION ---
func (l *Link) PlugIn(port sim.Port) {

	fmt.Printf("[DEBUG_PLUGIN] Link '%s' connected to Port '%s' (Pointer: %p).\n", l.Name(), port.Name(), port)

	if l.SideA == nil {
		l.SideA = port
	} else if l.SideB == nil {
		l.SideB = port
	} else {
		log.Panicf("OpticalLink %s supports only 2 ports.", l.Name())
	}

	port.SetConnection(l) // Guardando un puntero a este cable.
}

func (l *Link) Unplug(port sim.Port) {
	if l.SideA == port {
		l.SideA = nil
	} else if l.SideB == port {
		l.SideB = nil
	}
}

func (l *Link) NotifyAvailable(port sim.Port) {
	l.NotifySend()
}

// Llamado automáticamente cuando alguien quiere enviar algo.
func (l *Link) NotifySend() {
	// CASO 1: HAY Reconfiguración. Pausamos.
	// No hay call a CheckAndForward, tampoco a RetrieveOutgoing.
	// El package queda en el componente origen.
	l.pausedLock.Lock()
	if l.paused {
		l.pausedLock.Unlock()
		return
	}
	l.pausedLock.Unlock()

	// CASO 2: NO HAY Reconfiguración.
	// Se tiene que revisar quién tiene algo porque no hay args.
	now := l.Engine.CurrentTime()

	if l.SideA != nil {
		l.CheckAndForward(now, l.SideA, l.SideB)
	}
	if l.SideB != nil {
		l.CheckAndForward(now, l.SideB, l.SideA)
	}
}

// --- LÓGICA DE DESEMPAQUETADO ---
func (l *Link) CheckAndForward(now sim.VTimeInSec, src, dst sim.Port) {
	if dst == nil {
		fmt.Printf("[LINK_ERROR] %s: Destination not connected.\n", l.Name())
		return
	}

	// Procesar todos los mensajes pendientes.
	for {
		msg := src.RetrieveOutgoing() // Extrayendo el mensaje del port origen.
		if msg == nil {
			return
		}

		msgToDeliver := msg

		// Si el mensaje viene del Switch (sobre), lo abrimos.
		if pkt, ok := msg.(*OpticalPacket); ok {
			msgToDeliver = pkt.InnerMsg
			//} else { // Viene de la GPU (mensaje normal), lo pasamos tal cual.
			//	fmt.Printf("[LINK_TRAFFIC] %s: %s -> %s (Type: %T)\n", l.Name(), src.Name(), dst.Name(), msg)
		}

		// Diciéndole al motor (Engine) que ejecute luego.
		evt := NewLinkDeliveryEvent(now+l.Latency, l, msgToDeliver, dst)
		l.Engine.Schedule(evt)
	}

}
