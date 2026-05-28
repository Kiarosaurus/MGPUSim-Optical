package main

import (
	"fmt"

	"github.com/sarchlab/akita/v4/sim"
	"github.com/sarchlab/akita/v4/tracing"
)

type simGPU struct {
	*sim.ComponentBase
	engine sim.Engine
	Port   sim.Port
	Index  int

	onRecv func(t sim.VTimeInSec, hopSrc int)
}

func newSimGPU(name string, index int, engine sim.Engine) *simGPU {
	g := &simGPU{
		ComponentBase: sim.NewComponentBase(name),
		engine:        engine,
		Index:         index,
	}
	g.Port = sim.NewPort(g, 64, 64, name+".Port")
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
		HopSrc: e.HopSrc,
		HopDst: e.HopDst,
	}
	tracing.TraceReqInitiate(msg, g, "")
	if err := g.Port.Send(msg); err != nil {
		fmt.Printf("[WARN] %s: outgoing full at t=%.9fs — hop=%d->%d dropped\n",
			g.Name(), float64(g.engine.CurrentTime()), e.HopSrc, e.HopDst)
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
		dm := msg.(*DataMsg)
		fmt.Printf("[%s] t=%.9fs  RECV hop=%d->%d\n",
			g.Name(), float64(g.engine.CurrentTime()), dm.HopSrc, dm.HopDst)
		if g.onRecv != nil {
			g.onRecv(g.engine.CurrentTime(), dm.HopSrc)
		}
	}
}

func (g *simGPU) NotifyPortFree(_ sim.Port) {}
