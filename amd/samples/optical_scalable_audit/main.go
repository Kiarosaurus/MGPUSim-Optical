// optical_scalable_audit — anillo cíclico de N GPUs con N variable por CLI.
// ----------------------------------------------------------------------------
//
//	 Topología:
//	   GPU0 -> GPU1 -> GPU2 -> ... -> GPU(N-1) -> GPU0   (cierre del anillo)
//
//	 Comportamiento:
//	   - Cada GPU i envía un único paquete a GPU (i+1) mod N.
//	   - El primer salto (GPU0 -> GPU1) parte en t = 1 ns.
//	   - El siguiente salto se dispara cuando la GPU receptora recibe el paquete
//	     (cadena de dependencias secuencial, no paralela).
//	   - Cada srcPort se usa exactamente UNA vez, así nunca se dispara el drain
//	     (no hay cambio de dst en un srcPort ya conectado). Cada salto sólo
//	     sufre T_reconfig + T_fiber.
//
//	 Modelo matemático
//	 ----------------------------------------------------------------------------
//		T_reconfig = 820 ns
//		T_drain    =  21 ns   (no se dispara en este benchmark)
//		T_fiber    =  10 ns
//
//		Latencia por salto i: T_reconfig + T_fiber = 830 ns
//		1er RECV  (GPU0->GPU1) at t = 1 + 830 = 831 ns
//		siguiente trigger a t = 832 ns  (1 ns después del recv)
//		2do RECV  (GPU1->GPU2) at t = 832 + 830 = 1662 ns
//		...
//		k-ésimo RECV at t = 1 + k*830 + (k-1)*1 = 1 + k*831 - 1 = k*831 ns
//
//	 Uso:
//	   go run . -gpus 4
//	   go run . -gpus 16
//
//	 Salida esperada en Daisen Gantt global:
//	   - Carril OpticalSwitch: N bloques optical_reconfig consecutivos + N optical_fiber cortos.
//	   - Carriles GPU0..GPU(N-1): cada uno con un req_in al final de su ventana.
//	   - Trace file: optical_scalable.sqlite3
package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/sarchlab/akita/v4/datarecording"
	"github.com/sarchlab/akita/v4/sim"
	"github.com/sarchlab/akita/v4/tracing"
)

const (
	packetSizeBytes = 64

	switchingDelay sim.VTimeInSec = 820e-9
	// drainDelay: guard window pre-reconfig. Sin tráfico de cambio de dst en
	// este benchmark, el evento de drain nunca se programa.
	drainDelay   sim.VTimeInSec = 21e-9
	fiberLatency sim.VTimeInSec = 10e-9
)

var numGPUs = flag.Int("gpus", 4, "number of GPUs in the ring (>= 2)")

func main() {
	flag.Parse()
	N := *numGPUs
	if N < 2 {
		log.Fatalf("-gpus must be >= 2, got %d", N)
	}

	engine := sim.NewSerialEngine()

	gpus := make([]*simGPU, N)
	for i := 0; i < N; i++ {
		gpus[i] = newSimGPU(fmt.Sprintf("GPU%d", i), i, engine)
	}

	sw := newScalableSwitch("OpticalSwitch", engine, switchingDelay, drainDelay, fiberLatency)
	for _, g := range gpus {
		sw.PlugIn(g.Port)
	}

	recorder := datarecording.NewDataRecorder("optical_scalable")
	defer recorder.Close()

	dbTracer := tracing.NewDBTracer(engine, recorder)
	defer dbTracer.Terminate()

	for _, g := range gpus {
		tracing.CollectTrace(g, dbTracer)
	}
	tracing.CollectTrace(sw, dbTracer)

	// triggerFreq: la misma frecuencia que el switch (1 GHz). Se usa exclusivamente
	// para calcular el "siguiente tick" del SendEvent que dispara cada hop del anillo
	// con precisión de ciclo entero (Freq.NCyclesLater).
	const triggerFreq = 1 * sim.GHz

	// Cadena cíclica: al recibir el paquete del predecesor, la GPU emite al sucesor.
	// El último salto (GPU N-1 -> GPU 0) cierra el anillo y no dispara más envíos.
	for i := 0; i < N; i++ {
		sender := gpus[i]
		nextIdx := (i + 1) % N
		nextPort := gpus[nextIdx].Port.AsRemote()
		hopSrc, hopDst := i, nextIdx

		// La GPU receptora gpus[nextIdx] dispara su propio envío al sucesor.
		recv := gpus[nextIdx]
		recv.onRecv = func(t sim.VTimeInSec, _ int) {
			// El último eslabón (GPU N-1 -> GPU 0) cierra el anillo sin re-disparar.
			if nextIdx == 0 {
				return
			}
			evt := SendEvent{
				DstPort: gpus[(nextIdx+1)%N].Port.AsRemote(),
				SeqNum:  nextIdx + 1,
				HopSrc:  nextIdx,
				HopDst:  (nextIdx + 1) % N,
			}
			// NCyclesLater(1, t) = Freq.ThisTick(t + 1/freq) — snap a tick limpio.
			// Sin esto, `t + 1e-9` acumula ULP drift hop a hop y a N=12 ya rompe.
			fireAt := triggerFreq.NCyclesLater(1, t)
			evt.EventBase = *sim.NewEventBase(fireAt, gpus[nextIdx])
			engine.Schedule(evt)
			fmt.Printf("[RING] t=%.9fs  hop scheduled: GPU%d->GPU%d\n",
				float64(fireAt), nextIdx, (nextIdx+1)%N)
		}
		_ = sender
		_ = nextPort
		_ = hopSrc
		_ = hopDst
	}

	// Semilla inicial: GPU0 -> GPU1 en t = 1 ns.
	first := SendEvent{
		DstPort: gpus[1].Port.AsRemote(),
		SeqNum:  1,
		HopSrc:  0,
		HopDst:  1,
	}
	first.EventBase = *sim.NewEventBase(1e-9, gpus[0])
	engine.Schedule(first)

	fmt.Println("### optical_scalable_audit — starting ###")
	fmt.Printf("  N = %d GPUs (ring: 0 -> 1 -> ... -> %d -> 0)\n", N, N-1)
	fmt.Printf("  switchingDelay = %.0f ns  drainDelay = %.0f ns (idle)  fiberLatency = %.0f ns\n",
		float64(switchingDelay)*1e9,
		float64(drainDelay)*1e9,
		float64(fiberLatency)*1e9)
	fmt.Printf("  expected total reconfigs = %d  fibers = %d  drains = 0\n", N, N)

	if err := engine.Run(); err != nil {
		log.Fatal(err)
	}

	fmt.Println("\n### Done ###")
	fmt.Println("    Trace: optical_scalable.sqlite3")
	fmt.Println("    Serve: cd akita/daisen && go run . -sqlite <ruta>/optical_scalable.sqlite3")
	fmt.Println("    Gantt: http://localhost:3001/global-gantt")
}
