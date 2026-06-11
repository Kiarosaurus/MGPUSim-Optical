// ----------------------------------------------------------------------------
//
//	Fase 1   GPU1 -> GPU2: inyectar N=10 paquetes de S=64 bytes cada uno.
//	         El primer paquete activa una ventana de reconfiguración de 820 ns.
//	         Los paquetes 2–10 hacen stall en el buffer de salida hasta que termina el reconfig.
//	         Los 10 se entregan a la vez cuando el enlace se abre.
//
//	Fase 2   Inmediatamente después de que GPU2 recibe el 10° paquete, GPU1 envía 1
//	         paquete a GPU3. Es el primer mensaje en el camino GPU1->GPU3,
//	         por lo que se activa una segunda ventana de reconfiguración independiente de 820 ns.
//
// Línea de tiempo de simulación (serial engine, switch clock = 1 GHz = 1 ns/tick)
// ----------------------------------------------------------------------------
//
//	Phase 1 (primer uso del srcPort de GPU1; sin drain previo)
//	t =   1 ns        GPU1 envía seq=1 a GPU2; switch inicia reconfig (820 ns)
//	t =   2..10 ns    GPU1 envía seq=2..10; en stall (linkReconfiguring)
//	t = 821 ns        Reconfig terminado; seq=1 entra a la fibra
//	t = 822..830 ns   seq=2..10 entran a la fibra (1 paquete/ciclo, pipelined)
//	t = 831 ns        GPU2 recibe seq=1     (821 + T_fiber)
//	t = 832..839 ns   GPU2 recibe seq=2..9
//	t = 840 ns        GPU2 recibe seq=10  -> programa Phase2 SendEvent
//
//	Phase 2 (cambio de dst en el srcPort de GPU1; drain ANTES de reconfig)
//	t = 840 ns        GPU1 envía seq=11 a GPU3; switch detecta dst nuevo en
//	                  un srcPort ya conectado -> inicia DRAIN (21 ns)
//	t = 861 ns        Drain terminado; switch inicia reconfig (820 ns)
//	t =1681 ns        Reconfig terminado; seq=11 entra a la fibra
//	t =1691 ns        GPU3 recibe seq=11   (1681 + T_fiber)

// Modelo matemático
// ----------------------------------------------------------------------------
//
//	T_reconfig = 820 ns      (conmutación MZI, FlexFly [2023])
//	T_drain    =  21 ns      (guard window pre-reconfig: fotones en vuelo +
//	                          transceiver settle time; placeholder fijo)
//	T_fiber    =  10 ns      (2 m de intra-rack fiber, c/1.5)
//	BW         =  32 GB/s = 256 Gbit/s   (bandwidth de referencia del enlace)
//
//	T_drain_bw = (N x S x 8) / BW
//	           = (10 x 64 x 8) / 256e9
//	           = 5120 / 256e9
//	           =~  20 ns    (teórico a 32 GB/s)
//
//	Cuándo aplica T_drain:
//	  - Sólo cuando un srcPort cambia de dst (ya tenía circuito vivo).
//	  - NO aplica al primer uso del srcPort (no hay nada que drenar).
//
//	Entrega Fase 1 (primer paquete, srcPort nuevo, SIN drain):
//	  T_total_1 = T_reconfig + T_fiber = 820 + 10 = 830 ns
//	Drenado Fase 1 (los N paquetes salen pipelined del switch):
//	  T_drain_sim = N / f_switch = 10 / 1 GHz = 10 ns   (modelo de simulación)
//	  T_drain_ref = N x S x 8 / BW =~ 20 ns              (fórmula de bandwidth)
//	Fase 2 (GPU1 -> GPU3, 1 paquete, CON drain del circuito viejo):
//	  T_total_2 = T_drain + T_reconfig + T_fiber = 21 + 820 + 10 = 851 ns
//
//	Nota: esta simulación no modela el bandwidth del enlace de forma explícita.
//	      Cada tick del switch (1 ns a 1 GHz) reenvía un paquete independientemente del
//	      tamaño del paquete. T_drain_ref se muestra como referencia de hardware real.

// Inspección con Daisen
// ----------------------------------------------------------------------------
//  1. Ejecutar: cd /path/to/optical_deterministic_audit && go run .
//  2. Archivo:  optical_audit.sqlite3  (escrito en el directorio de trabajo)
//  3. Servir:   cd akita/daisen && go run . -sqlite <ruta>/optical_audit.sqlite3
//  4. Consulta: GET http://localhost:3001/api/trace?where=OpticalSwitch
//  5. Ver:      http://localhost:3001/task?id=<id>
//
// Salida esperada de Daisen:
//   - Carril OpticalSwitch:
//   - "optical_reconfig" [1 ns -> 821 ns]    (820 ns, Phase 1, sin drain previo)
//   - "optical_drain"    [840 ns -> 861 ns]  ( 21 ns, Phase 2 guard window)
//   - "optical_reconfig" [861 ns -> 1681 ns] (820 ns, Phase 2 MZI switch)
//   - 11 "optical_fiber" cortos (10 ns c/u) en pipeline 821-830 ns y solo en 1681 ns.
//   - Carril GPU2: 10 req_in events agrupados en 831..840 ns.
//   - Carril GPU3:  1 req_in event a t = 1691 ns.
package main

import (
	"fmt"
	"log"

	"github.com/sarchlab/akita/v4/datarecording"
	"github.com/sarchlab/akita/v4/sim"
	"github.com/sarchlab/akita/v4/tracing"
)

const (
	numPackets      = 10
	packetSizeBytes = 64

	switchingDelay sim.VTimeInSec = 820e-9
	// drainDelay: guard window obligatorio antes de reconfigurar el MZI cuando un
	// srcPort cambia de dst. Modela fotones en vuelo + transceiver settle time.
	// TODO. Protocolo de drenado más realista.
	drainDelay   sim.VTimeInSec = 1e-9
	fiberLatency sim.VTimeInSec = 0

	linkBandwidthGBs = 32.0 // GB/s reference (Flexfly)
)

func main() {
	engine := sim.NewSerialEngine()

	gpu1 := newSimGPU("GPU1", engine)
	gpu2 := newSimGPU("GPU2", engine)
	gpu3 := newSimGPU("GPU3", engine)

	sw := newAuditSwitch("OpticalSwitch", engine, switchingDelay, drainDelay, fiberLatency)
	sw.PlugIn(gpu1.Port)
	sw.PlugIn(gpu2.Port)
	sw.PlugIn(gpu3.Port)

	recorder := datarecording.NewDataRecorder("optical_audit")
	defer recorder.Close()

	dbTracer := tracing.NewDBTracer(engine, recorder)
	defer dbTracer.Terminate()

	tracing.CollectTrace(gpu1, dbTracer)
	tracing.CollectTrace(gpu2, dbTracer)
	tracing.CollectTrace(gpu3, dbTracer)
	tracing.CollectTrace(sw, dbTracer)

	// Trigger de Fase 2: cuando GPU2 recibe el 10° paquete, programar
	// un SendEvent en GPU1 al mismo tiempo simulado.
	gpu2.triggerAfter = numPackets
	gpu2.triggerFn = func(t sim.VTimeInSec) {
		evt := SendEvent{
			DstPort: gpu3.Port.AsRemote(),
			SeqNum:  numPackets + 1,
		}
		evt.EventBase = *sim.NewEventBase(t, gpu1)
		engine.Schedule(evt)
		fmt.Printf("[AUDIT] t=%.9fs  Phase2 scheduled: GPU1->GPU3\n", float64(t))
	}

	// Fase 1: inyectar N paquetes en t = 1ns, 2ns, ..., Nns.
	for i := 1; i <= numPackets; i++ {
		evt := SendEvent{DstPort: gpu2.Port.AsRemote(), SeqNum: i}
		evt.EventBase = *sim.NewEventBase(sim.VTimeInSec(i)*1e-9, gpu1)
		engine.Schedule(evt)
	}

	printMathModel()

	fmt.Println("### optical_deterministic_audit — starting ###")
	if err := engine.Run(); err != nil {
		log.Fatal(err)
	}
	fmt.Println("\n### Done ###")
	fmt.Println("    Trace: optical_audit.sqlite3")
	fmt.Println("    Serve: cd akita/daisen && go run . -sqlite <ruta>/optical_audit.sqlite3")
	fmt.Println("    Gantt: http://localhost:3001/global-gantt  (o /api/trace?where=OpticalSwitch)")
}

func printMathModel() {
	const bwBitsPerSec = linkBandwidthGBs * 8e9
	const totalBits = numPackets * packetSizeBytes * 8
	tDrainRef := float64(totalBits) / bwBitsPerSec * 1e9
	tDrainSim := float64(numPackets) * 1.0 // 1 ns por paquete a 1 GHz
	tReconfigNs := float64(switchingDelay) * 1e9
	tDrainNs := float64(drainDelay) * 1e9
	tFiberNs := float64(fiberLatency) * 1e9

	// Tiempos absolutos esperados. Phase 1 arranca con SendEvent en t=1ns.
	// Phase 1: primer uso del circuito GPU1->GPU2, NO hay drain (no había
	// nada antes en el srcPort de GPU1). Sólo reconfig + fiber.
	tFirstRecvP1 := 1 + tReconfigNs + tFiberNs  // 831 ns
	tLastRecvP1 := tFirstRecvP1 + tDrainSim - 1 // 840 ns
	// Phase 2: cambio de dst GPU1->GPU2 a GPU1->GPU3. El srcPort de GPU1
	// YA tenía circuito activo -> trigger del drain antes del reconfig.
	tDrainStartP2 := tLastRecvP1                // 840 ns (Phase2 SendEvent)
	tDrainEndP2 := tDrainStartP2 + tDrainNs     // 861 ns
	tReconfigEndP2 := tDrainEndP2 + tReconfigNs // 1681 ns
	tFirstRecvP2 := tReconfigEndP2 + tFiberNs   // 1691 ns

	fmt.Printf("\n-- Analytical Model ------------------------------------\n")
	fmt.Printf("  N            = %d packets\n", numPackets)
	fmt.Printf("  S            = %d bytes = %d bits\n", packetSizeBytes, packetSizeBytes*8)
	fmt.Printf("  BW (ref)     = %.0f GB/s = %.0f Gbit/s\n", linkBandwidthGBs, linkBandwidthGBs*8)
	fmt.Printf("  T_reconfig   = %.0f ns\n", tReconfigNs)
	fmt.Printf("  T_drain      = %.0f ns  (guard window pre-reconfig, sólo si hay circuito vivo)\n", tDrainNs)
	fmt.Printf("  T_fiber      = %.0f ns  (modelada por FiberDeliverEvent)\n", tFiberNs)
	fmt.Printf("  T_drain_ref  = NxSx8 / BW = %d / %.0fGbit/s = %.2f ns\n",
		totalBits, linkBandwidthGBs*8, tDrainRef)
	fmt.Printf("  T_drain_sim  = N / f_switch = %d / 1GHz = %.0f ns\n",
		numPackets, tDrainSim)
	fmt.Printf("\n  Phase 1 (GPU1->GPU2, N=%d packets, sin drain previo):\n", numPackets)
	fmt.Printf("    1st packet  RECV at t = 1 + T_reconfig + T_fiber = 1 + %.0f + %.0f = %.0f ns\n",
		tReconfigNs, tFiberNs, tFirstRecvP1)
	fmt.Printf("    Nth packet  RECV at t = 1st + (N-1) = %.0f + %d = %.0f ns\n",
		tFirstRecvP1, numPackets-1, tLastRecvP1)
	fmt.Printf("\n  Phase 2 (GPU1->GPU3, 1 packet, triggered at t = %.0f ns):\n", tLastRecvP1)
	fmt.Printf("    DRAIN window:    [%.0f, %.0f] ns  (T_drain = %.0f ns)\n",
		tDrainStartP2, tDrainEndP2, tDrainNs)
	fmt.Printf("    RECONFIG window: [%.0f, %.0f] ns  (T_reconfig = %.0f ns)\n",
		tDrainEndP2, tReconfigEndP2, tReconfigNs)
	fmt.Printf("    T_2 = T_drain + T_reconfig + T_fiber = %.0f + %.0f + %.0f = %.0f ns\n",
		tDrainNs, tReconfigNs, tFiberNs, tDrainNs+tReconfigNs+tFiberNs)
	fmt.Printf("    GPU3 receives at t = %.0f + T_2 = %.0f ns\n",
		tLastRecvP1, tFirstRecvP2)
	fmt.Printf("--------------------------------------------------------\n\n")
}
