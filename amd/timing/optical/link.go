package optical

import (
	"fmt"
	"log"

	"github.com/sarchlab/akita/v4/sim"
)

// LinkDeliveryEvent modela un estallido de fotones llegando al puerto de destino
// después de propagar a través de la fibra durante Latency segundos.
type LinkDeliveryEvent struct {
	*sim.EventBase
	Msg     sim.Msg
	DstPort sim.Port
}

func NewLinkDeliveryEvent(
	time sim.VTimeInSec,
	handler sim.Handler,
	msg sim.Msg,
	dst sim.Port,
) *LinkDeliveryEvent {
	return &LinkDeliveryEvent{
		EventBase: sim.NewEventBase(time, handler),
		Msg:       msg,
		DstPort:   dst,
	}
}

func (e *LinkDeliveryEvent) Execute(engine sim.Engine) error {
	err := e.DstPort.Deliver(e.Msg)
	if err != nil {
		// Destination buffer full: reintentar después de 1 ns virtual.
		retry := NewLinkDeliveryEvent(e.Time()+1e-9, e.Handler(), e.Msg, e.DstPort)
		engine.Schedule(retry)
	}
	return nil
}

// Link modela una fibra óptica bidireccional con una latencia de propagación fija. El
// backpressure por reconfiguración es manejado completamente por el Switch; el Link siempre
// reenvía lo que el puerto fuente ofrece.
type Link struct {
	*sim.TickingComponent

	SideA   sim.Port
	SideB   sim.Port
	Latency sim.VTimeInSec
}

func NewLink(name string, engine sim.Engine, latency sim.VTimeInSec) *Link {
	l := &Link{Latency: latency}
	l.TickingComponent = sim.NewTickingComponent(name, engine, 1*sim.GHz, l)
	return l
}

// Tick es un no-op: el Link es impulsado por eventos (LinkDeliveryEvent + NotifySend),
// no por ticks. Retornar false previene que se programen más TickEvents.
func (l *Link) Tick() bool { return false }

func (l *Link) Handle(e sim.Event) error {
	if evt, ok := e.(*LinkDeliveryEvent); ok {
		return evt.Execute(l.Engine)
	}
	return l.TickingComponent.Handle(e)
}

func (l *Link) PlugIn(port sim.Port) {
	fmt.Printf("[LINK] %s connected to port %s\n", l.Name(), port.Name())
	if l.SideA == nil {
		l.SideA = port
	} else if l.SideB == nil {
		l.SideB = port
	} else {
		log.Panicf("OpticalLink %s supports only 2 ports", l.Name())
	}
	port.SetConnection(l)
}

func (l *Link) Unplug(port sim.Port) {
	if l.SideA == port {
		l.SideA = nil
	} else if l.SideB == port {
		l.SideB = nil
	}
}

func (l *Link) NotifyAvailable(_ sim.Port) {
	l.NotifySend()
}

func (l *Link) NotifySend() {
	now := l.Engine.CurrentTime()
	if l.SideA != nil {
		l.CheckAndForward(now, l.SideA, l.SideB)
	}
	if l.SideB != nil {
		l.CheckAndForward(now, l.SideB, l.SideA)
	}
}

// CheckAndForward checa y reenvía los mensajes del buffer de salida de src,
// desempaquetando cualquier OpticalPacket, y programa un LinkDeliveryEvent
// para cada mensaje después de Latency.
func (l *Link) CheckAndForward(now sim.VTimeInSec, src, dst sim.Port) {
	if dst == nil {
		fmt.Printf("[LINK] %s: destination not connected\n", l.Name())
		return
	}
	for {
		msg := src.RetrieveOutgoing()
		if msg == nil {
			return
		}
		msgToDeliver := msg
		if pkt, ok := msg.(*OpticalPacket); ok {
			msgToDeliver = pkt.InnerMsg
		}
		evt := NewLinkDeliveryEvent(now+l.Latency, l, msgToDeliver, dst)
		l.Engine.Schedule(evt)
	}
}
