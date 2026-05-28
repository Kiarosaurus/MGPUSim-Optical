package main

import (
	"fmt"

	"github.com/sarchlab/akita/v4/sim"
	"github.com/sarchlab/akita/v4/tracing"
)

type simGPU struct {
	*sim.ComponentBase
	engine    sim.Engine
	Port      sim.Port
	recvCount int

	// Configurar antes de engine.Run(). Se dispara una vez cuando recvCount == triggerAfter.
	triggerAfter int
	triggerFn    func(t sim.VTimeInSec)
}

func newSimGPU(name string, engine sim.Engine) *simGPU {
	g := &simGPU{
		ComponentBase: sim.NewComponentBase(name),
		engine:        engine,
	}
	// Buffers grandes para absorber 500 paquetes en vuelo.
	g.Port = sim.NewPort(g, 1024, 1024, name+".Port")
	g.AddPort("Port", g.Port)
	return g
}

func (g *simGPU) Handle(e sim.Event) error {
	if evt, ok := e.(SendEvent); ok {
		return g.sendMsg(evt)
	}
	return nil
}

func (g *simGPU) sendMsg(e SendEvent) error {
	msg := &DataMsg{
		MsgMeta: sim.MsgMeta{
			ID:           sim.GetIDGenerator().Generate(),
			Src:          g.Port.AsRemote(),
			Dst:          e.DstPort,
			TrafficBytes: packetSizeBytes,
		},
		SeqNum: e.SeqNum,
		Phase:  e.Phase,
	}
	tracing.TraceReqInitiate(msg, g, "")
	if err := g.Port.Send(msg); err != nil {
		fmt.Printf("[WARN] %s: outgoing full at t=%.9fs — phase=%d seq=%d dropped\n",
			g.Name(), float64(g.engine.CurrentTime()), e.Phase, e.SeqNum)
	}
	return nil
}

func (g *simGPU) NotifyRecv(port sim.Port) {
	for {
		msg := port.RetrieveIncoming()
		if msg == nil {
			return
		}
		tracing.TraceReqReceive(msg, g)
		tracing.TraceReqComplete(msg, g)
		// Modelo one-way: no hay response que dispare TraceReqFinalize en el sender.
		// Sin este cierre, req_out queda huérfano hasta que DBTracer.Terminate()
		// lo estampa al t final de la simulación (1670 ns).
		tracing.TraceReqFinalize(msg, g)
		g.recvCount++
		if g.triggerAfter > 0 && g.recvCount == g.triggerAfter && g.triggerFn != nil {
			fn := g.triggerFn
			g.triggerFn = nil
			fn(g.engine.CurrentTime())
		}
	}
}

func (g *simGPU) NotifyPortFree(_ sim.Port) {}
