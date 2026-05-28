// Package runner defines how default benchmark samples are executed.
package runner

import (
	"fmt"
	"log"
	"strings"

	// Enable profiling
	_ "net/http/pprof"
	"sync"

	"github.com/sarchlab/akita/v4/sim"
	"github.com/sarchlab/akita/v4/simulation"
	"github.com/sarchlab/akita/v4/tracing"
	"github.com/sarchlab/mgpusim/v4/amd/benchmarks"
	"github.com/sarchlab/mgpusim/v4/amd/driver"
	"github.com/sarchlab/mgpusim/v4/amd/samples/runner/emusystem"
	"github.com/sarchlab/mgpusim/v4/amd/samples/runner_optical/timingconfig"
	"github.com/sarchlab/mgpusim/v4/amd/sampling"
)

type verificationPreEnablingBenchmark interface {
	benchmarks.Benchmark

	EnableVerification()
}

// Runner is a class that helps running the benchmarks in the official samples.
type Runner struct {
	simulation *simulation.Simulation // motor de simulación
	platform   *sim.Domain            // plataforma de HW
	reporter   *reporter              // recolector de métricas

	// Flags
	Timing           bool
	Verify           bool
	Parallel         bool
	UseUnifiedMemory bool

	GPUIDs     []int
	benchmarks []benchmarks.Benchmark
}

// Init initializes the platform simulate
func (r *Runner) Init() *Runner {
	r.parseFlag() // Leer línea de comandos

	log.SetFlags(log.Llongfile | log.Ldate | log.Ltime)

	r.initSimulation()

	if r.Timing { // timing simulation or...
		r.buildTimingPlatform()
	} else { // ... functional simulation?
		r.buildEmuPlatform()
	}

	r.createUnifiedGPUs()

	return r
}

func (r *Runner) initSimulation() {
	builder := simulation.MakeBuilder()

	if *parallelFlag {
		builder = builder.WithParallelEngine()
	}

	if *visTracerDBFileName != "" {
		builder = builder.WithOutputFileName(*visTracerDBFileName)
	}

	r.simulation = builder.Build()
}

func (r *Runner) buildEmuPlatform() { // FUNCTIONAL simulation.
	b := emusystem.MakeBuilder().
		WithSimulation(r.simulation).
		WithNumGPUs(r.GPUIDs[len(r.GPUIDs)-1])

	if *isaDebug {
		b = b.WithDebugISA()
	}

	r.platform = b.Build()
}

func (r *Runner) buildTimingPlatform() { // TIMING simulation.
	sampling.InitSampledEngine()

	b := timingconfig.MakeBuilder().
		WithSimulation(r.simulation).
		WithNumGPUs(r.GPUIDs[len(r.GPUIDs)-1])

	if *magicMemoryCopy {
		b = b.WithMagicMemoryCopy()
	}

	r.platform = b.Build()
	r.reporter = newReporter(r.simulation)
	r.configureVisTracing()
}

func (r *Runner) configureVisTracing() {
	if !*visTracing {
		return
	}

	visTracer := r.simulation.GetVisTracer()
	for _, comp := range r.simulation.Components() {
		tracing.CollectTrace(comp.(tracing.NamedHookable), visTracer)
	}
}

func (r *Runner) createUnifiedGPUs() {
	if *unifiedGPUFlag == "" {
		return
	}

	driver := r.simulation.GetComponentByName("Driver").(*driver.Driver)
	unifiedGPUID := driver.CreateUnifiedGPU(nil, r.GPUIDs)
	r.GPUIDs = []int{unifiedGPUID}
}

// AddBenchmark adds an benchmark that the driver runs
func (r *Runner) AddBenchmark(b benchmarks.Benchmark) {
	b.SelectGPU(r.GPUIDs)
	if r.UseUnifiedMemory {
		b.SetUnifiedMemory()
	}

	r.benchmarks = append(r.benchmarks, b)
}

// AddBenchmarkWithoutSettingGPUsToUse allows for user specified GPUs for
// the benchmark to run.
func (r *Runner) AddBenchmarkWithoutSettingGPUsToUse(b benchmarks.Benchmark) {
	if r.UseUnifiedMemory {
		b.SetUnifiedMemory()
	}

	r.benchmarks = append(r.benchmarks, b)
}

// Run runs the benchmark. PROGRAMA PRINCIPAL DE EMULACIÓN.
func (r *Runner) Run() {
	fmt.Println("\n=== DIAGNÓSTICO PRE-VUELO DE COMPONENTES ===")

	// 1. Usamos r.simulation.Components() en lugar del mapa
	allComponents := r.simulation.Components()

	foundRDMA := false
	for _, comp := range allComponents {
		// Filtramos solo los componentes RDMA para no saturar el log
		if strings.Contains(comp.Name(), "RDMA") {
			foundRDMA = true
			fmt.Printf("Componente: %s\n", comp.Name())
			ports := comp.Ports()
			for _, p := range ports {
				// Filtramos puertos relevantes (Data/Outside)
				if strings.Contains(p.Name(), "Outside") || strings.Contains(p.Name(), "Data") {
					// 2. Imprimimos el puntero del puerto (%p) para cotejar con link.go
					fmt.Printf("   -> Puerto: %s | Puntero Memoria: %p\n", p.Name(), p)
				}
			}
		}
	}

	if !foundRDMA {
		fmt.Println("¡ALERTA! No se encontraron componentes con nombre 'RDMA' registrados en la simulación.")
	}
	fmt.Println("==========================================================\n")

	r.Driver().Run() // Inicia el Driver.

	var wg sync.WaitGroup // <- lanza los benchmarks concurrentemente.
	for _, b := range r.benchmarks {
		wg.Add(1)
		go func(b benchmarks.Benchmark, wg *sync.WaitGroup) {
			if r.Verify {
				if b, ok := b.(verificationPreEnablingBenchmark); ok {
					b.EnableVerification()
				}
			}

			b.Run()

			if r.Verify {
				b.Verify()
			}
			wg.Done()
		}(b, &wg)
	}
	wg.Wait()

	if r.reporter != nil {
		r.reporter.report() // write resultados de rendimiento.
	}

	r.Driver().Terminate()
	r.simulation.Terminate()
}

// Driver returns the GPU driver used by the current runner.
func (r *Runner) Driver() *driver.Driver {
	return r.simulation.GetComponentByName("Driver").(*driver.Driver)
}

// Engine returns the event-driven simulation engine used by the current runner.
func (r *Runner) Engine() sim.Engine {
	return r.simulation.GetEngine()
}
