// optical_high_load_stress — saturación profunda de buffers en una topología fija de 3 GPUs.
// ----------------------------------------------------------------------------
//
//	Fase 1   GPU1 -> GPU2: N1 = 500 paquetes secuenciales.
//	         Primer uso del srcPort de GPU1 -> reconfig directo (820 ns), sin
//	         drain. Los 499 restantes hacen stall en el buffer de salida hasta
//	         que el enlace abre.
//
//	Fase 2   Disparada cuando GPU2 ha recibido los 500: GPU2 responde con
//	         N2 = 500 paquetes a GPU1. Primer uso del srcPort de GPU2 ->
//	         reconfig directo (820 ns), sin drain (srcPort distinto).
//	         Genera backpressure cruzado: GPU1 sigue drenando outbound mientras
//	         recibe inbound denso.
//
//	Fase 3   Disparada cuando GPU1 ha recibido los 500 de Fase 2: GPU1 conmuta
//	         a GPU3 con N3 = 200 paquetes. El srcPort de GPU1 YA tenía circuito
//	         hacia GPU2 -> trigger DRAIN (21 ns) + RECONFIG (820 ns) hacia GPU3.
//
// Modelo matemático
// ----------------------------------------------------------------------------
//
//	T_reconfig = 820 ns      (conmutación MZI, FlexFly [2023])
//	T_drain    =  21 ns      (guard window pre-reconfig)
//	T_fiber    =  10 ns      (2 m fibra intra-rack, c/1.5)
//
//	Phase 1 (srcPort GPU1 nuevo, SIN drain):
//	  1st RECV at t = 1 + T_reconfig + T_fiber = 831 ns
//	  500th RECV at t = 831 + 499 = 1330 ns
//
//	Phase 2 (srcPort GPU2 nuevo, SIN drain, trigger t=1330):
//	  1st RECV at t = 1330 + 1 + T_reconfig + T_fiber = 2161 ns
//	  500th RECV at t = 2161 + 499 = 2660 ns
//
//	Phase 3 (srcPort GPU1 cambia de dst GPU2->GPU3, CON drain, trigger t=2660):
//	  DRAIN  [2661, 2682] ns
//	  RECONFIG [2682, 3502] ns
//	  1st RECV at t = 3502 + T_fiber = 3512 ns
//	  200th RECV at t = 3512 + 199 = 3711 ns
//
// Salida esperada en Daisen Gantt global:
//   - Carril OpticalSwitch: 3 optical_reconfig + 1 optical_drain (entre Fase 2 y 3).
//   - Carriles GPU1/GPU2/GPU3: req_in densos al final de cada ventana.
//   - Trace file: optical_stress.sqlite3
package main

import (
	"fmt"
	"log"

	"github.com/sarchlab/akita/v4/datarecording"
	"github.com/sarchlab/akita/v4/sim"
	"github.com/sarchlab/akita/v4/tracing"
)

const (
	phase1Packets = 500
	phase2Packets = 500
	phase3Packets = 200

	packetSizeBytes = 64

	switchingDelay sim.VTimeInSec = 820e-9
	// drainDelay: guard window obligatorio antes de reconfigurar el MZI cuando un
	// srcPort cambia de dst. Modela fotones en vuelo + transceiver settle time.
	// TODO. Protocolo de drenado más realista.
	drainDelay   sim.VTimeInSec = 21e-9
	fiberLatency sim.VTimeInSec = 10e-9
)

func main() {
	engine := sim.NewSerialEngine()

	gpu1 := newSimGPU("GPU1", engine)
	gpu2 := newSimGPU("GPU2", engine)
	gpu3 := newSimGPU("GPU3", engine)

	sw := newStressSwitch("OpticalSwitch", engine, switchingDelay, drainDelay, fiberLatency)
	sw.PlugIn(gpu1.Port)
	sw.PlugIn(gpu2.Port)
	sw.PlugIn(gpu3.Port)

	recorder := datarecording.NewDataRecorder("optical_stress")
	defer recorder.Close()

	dbTracer := tracing.NewDBTracer(engine, recorder)
	defer dbTracer.Terminate()

	tracing.CollectTrace(gpu1, dbTracer)
	tracing.CollectTrace(gpu2, dbTracer)
	tracing.CollectTrace(gpu3, dbTracer)
	tracing.CollectTrace(sw, dbTracer)

	// Trigger Fase 3: GPU1 a GPU3 con 200 paquetes tras recibir 500 de Fase 2.
	gpu1.triggerAfter = phase2Packets
	gpu1.triggerFn = func(t sim.VTimeInSec) {
		fmt.Printf("[STRESS] t=%.9fs  Phase3 trigger: GPU1->GPU3 x %d\n",
			float64(t), phase3Packets)
		for i := 1; i <= phase3Packets; i++ {
			evt := SendEvent{
				DstPort: gpu3.Port.AsRemote(),
				SeqNum:  i,
				Phase:   3,
			}
			fireAt := t + sim.VTimeInSec(i)*1e-9
			evt.EventBase = *sim.NewEventBase(fireAt, gpu1)
			engine.Schedule(evt)
		}
	}

	// Trigger Fase 2: GPU2 a GPU1 con 500 paquetes tras recibir 500 de Fase 1.
	gpu2.triggerAfter = phase1Packets
	gpu2.triggerFn = func(t sim.VTimeInSec) {
		fmt.Printf("[STRESS] t=%.9fs  Phase2 trigger: GPU2->GPU1 x %d\n",
			float64(t), phase2Packets)
		for i := 1; i <= phase2Packets; i++ {
			evt := SendEvent{
				DstPort: gpu1.Port.AsRemote(),
				SeqNum:  i,
				Phase:   2,
			}
			fireAt := t + sim.VTimeInSec(i)*1e-9
			evt.EventBase = *sim.NewEventBase(fireAt, gpu2)
			engine.Schedule(evt)
		}
	}

	// Fase 1: GPU1 a GPU2, 500 paquetes en t = 1ns..500ns.
	for i := 1; i <= phase1Packets; i++ {
		evt := SendEvent{
			DstPort: gpu2.Port.AsRemote(),
			SeqNum:  i,
			Phase:   1,
		}
		evt.EventBase = *sim.NewEventBase(sim.VTimeInSec(i)*1e-9, gpu1)
		engine.Schedule(evt)
	}

	fmt.Println("### optical_high_load_stress — starting ###")
	fmt.Printf("  Phase1: GPU1->GPU2 x %d  | Phase2: GPU2->GPU1 x %d  | Phase3: GPU1->GPU3 x %d\n",
		phase1Packets, phase2Packets, phase3Packets)
	fmt.Printf("  packetSize=%dB  switchingDelay=%.0fns  drainDelay=%.0fns  fiberLatency=%.0fns\n",
		packetSizeBytes,
		float64(switchingDelay)*1e9,
		float64(drainDelay)*1e9,
		float64(fiberLatency)*1e9)

	if err := engine.Run(); err != nil {
		log.Fatal(err)
	}

	fmt.Println("\n### Done ###")
	fmt.Println("    Trace: optical_stress.sqlite3")
	fmt.Println("    Serve: cd akita/daisen && go run . -sqlite <ruta>/optical_stress.sqlite3")
	fmt.Println("    Gantt: http://localhost:3001/global-gantt")
}
