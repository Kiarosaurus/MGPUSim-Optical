// Package timingconfig contains the configuration for the timing simulation.
package timingconfig

import (
	"fmt"
	"math"

	"github.com/sarchlab/akita/v4/noc/networking/nvlink"

	"github.com/sarchlab/akita/v4/mem/mem"
	"github.com/sarchlab/akita/v4/mem/vm"
	"github.com/sarchlab/akita/v4/mem/vm/mmu"
	"github.com/sarchlab/akita/v4/sim"
	"github.com/sarchlab/akita/v4/simulation"
	"github.com/sarchlab/mgpusim/v4/amd/driver"
	"github.com/sarchlab/mgpusim/v4/amd/samples/runner/timingconfig/r9nano"
)

// Builder builds a hardware platform for timing simulation.
type Builder struct {
	simulation *simulation.Simulation

	numGPUs            int
	numCUPerSA         int // Num Compute Units por Shader Array.
	numSAPerGPU        int // Num Shader Array por GPU.
	cpuMemSize         uint64
	gpuMemSize         uint64
	log2PageSize       uint64
	useMagicMemoryCopy bool

	platform          *sim.Domain
	globalStorage     *mem.Storage
	rdmaAddressMapper *mem.BankedAddressPortMapper
}

// MakeBuilder creates a new Builder with default parameters.
func MakeBuilder() Builder {
	return Builder{
		numGPUs:            1,
		numCUPerSA:         4,
		numSAPerGPU:        16,
		cpuMemSize:         4 * mem.GB,
		gpuMemSize:         4 * mem.GB,
		log2PageSize:       12,
		useMagicMemoryCopy: false,
	}
}

// WithSimulation sets the simulation to use.
func (b Builder) WithSimulation(sim *simulation.Simulation) Builder {
	b.simulation = sim
	return b
}

// WithNumGPUs sets the number of GPUs to simulate.
func (b Builder) WithNumGPUs(numGPUs int) Builder {
	b.numGPUs = numGPUs
	return b
}

// WithMagicMemoryCopy sets whether to use the magic memory copy middleware.
func (b Builder) WithMagicMemoryCopy() Builder {
	b.useMagicMemoryCopy = true
	return b
}

// Build builds the hardware platform.
func (b Builder) Build() *sim.Domain {
	b.cpuGPUMemSizeMustEqual()

	b.platform = &sim.Domain{}

	b.globalStorage = mem.NewStorage( // Espacio de mem física TOTAL (para CPU y GPUs).
		uint64(b.numGPUs)*b.gpuMemSize + b.cpuMemSize)

	mmuComp, pageTable := b.createMMU()      // Crea la MMU y PT.
	gpuDriver := b.buildGPUDriver(pageTable) // Driver de la GPU.

	gpuBuilder := b.createGPUBuilder(gpuDriver, mmuComp) // Constructor de GPU a partir del paquete 'r9nano'.

	// Crea el conector NVLINK, el Root Complex (punto de origen en la CPU),
	// y la red híbrida a la que se conectarán las GPUs.
	nvConnector, rootComplexID :=
		b.createConnection(gpuDriver, mmuComp)

	mmuComp.MigrationServiceProvider = gpuDriver.GetPortByName("MMU").AsRemote()

	b.createRDMAAddrTable()
	pmcAddressTable := b.createPMCPageTable()

	b.createGPUs( // Se crean e interconectan las GPUs formando nuestra Malla 2D (Mesh)

		rootComplexID, nvConnector,
		gpuBuilder, gpuDriver,
		pmcAddressTable)

	return b.platform
}

func (b *Builder) cpuGPUMemSizeMustEqual() {
	if b.cpuMemSize != b.gpuMemSize {
		panic("currently only support cpuMemSize == gpuMemSize")
	}
}

func (b *Builder) createMMU() (*mmu.Comp, vm.PageTable) {
	pageTable := vm.NewPageTable(b.log2PageSize)
	mmuBuilder := mmu.MakeBuilder().
		WithEngine(b.simulation.GetEngine()).
		WithFreq(1 * sim.GHz).
		WithPageWalkingLatency(100).
		WithLog2PageSize(b.log2PageSize).
		WithPageTable(pageTable)

	mmuComponent := mmuBuilder.Build("MMU")

	b.simulation.RegisterComponent(mmuComponent)

	return mmuComponent, pageTable
}

func (b *Builder) buildGPUDriver(
	pageTable vm.PageTable,
) *driver.Driver {
	gpuDriverBuilder := driver.MakeBuilder()

	if b.useMagicMemoryCopy {
		gpuDriverBuilder = gpuDriverBuilder.WithMagicMemoryCopyMiddleware()
	}

	gpuDriver := gpuDriverBuilder.
		WithEngine(b.simulation.GetEngine()).
		WithPageTable(pageTable).
		WithLog2PageSize(b.log2PageSize).
		WithGlobalStorage(b.globalStorage).
		WithD2HCycles(8500).
		WithH2DCycles(14500).
		Build("Driver")

	b.simulation.RegisterComponent(gpuDriver)

	return gpuDriver
}

func (b *Builder) createGPUBuilder(
	gpuDriver *driver.Driver,
	mmuComponent *mmu.Comp,
) r9nano.Builder {
	gpuBuilder := r9nano.MakeBuilder().
		WithFreq(1 * sim.GHz).
		WithSimulation(b.simulation).
		WithMMU(mmuComponent).
		WithNumCUPerShaderArray(b.numCUPerSA).
		WithNumShaderArray(b.numSAPerGPU).
		WithNumMemoryBank(16).
		WithLog2MemoryBankInterleavingSize(7).
		WithLog2PageSize(b.log2PageSize).
		WithGlobalStorage(b.globalStorage)

	b.createRDMAAddressMapper()

	// gpuBuilder = b.setMemTracer(gpuBuilder)
	// gpuBuilder = b.setISADebugger(gpuBuilder)

	return gpuBuilder
}

func (b *Builder) createGPUs(
	rootComplexID int,
	nvConnector *nvlink.Connector, // Nuevo conector.
	gpuBuilder r9nano.Builder,
	gpuDriver *driver.Driver,
	pmcAddressTable *mem.BankedAddressPortMapper,
) {
	// 1. Dimensiones de la topología Mesh 2D (dinámico).
	numGPUsFloat := float64(b.numGPUs)
	numCols := int(math.Ceil(math.Sqrt(numGPUsFloat)))
	numRows := int(math.Ceil(numGPUsFloat / float64(numCols)))

	// Guardamos los IDs de red devueltos por nvlink.
	gpuMatrix := make([][]int, numRows)
	for r := range gpuMatrix {
		gpuMatrix[r] = make([]int, numCols)
		for c := range gpuMatrix[r] {
			gpuMatrix[r][c] = -1 // Hueco vacío!
		}
	}

	gpuID := 1
	for r := 0; r < numRows; r++ {
		for c := 0; c < numCols; c++ {

			// Salir si ya hemos creado todas las GPUs
			if gpuID > b.numGPUs {
				break
			}

			// Creamos un switch PCIe local y lo conectamos al Root Complex de la CPU
			pcieSwitchID := nvConnector.AddPCIeSwitch()
			nvConnector.ConnectSwitchesWithPCIeLink(rootComplexID, pcieSwitchID)

			// Creamos la GPU físicamente
			gpu := b.createGPU(gpuID, gpuBuilder, gpuDriver, pmcAddressTable)

			// !!! ENCHUFAMOS LA GPU AL ECOSISTEMA NVLINK
			// Asigna automáticamente rutas PCIe y su propio switch NVLink
			networkDeviceID := nvConnector.PlugInDevice(pcieSwitchID, gpu.Ports())

			// Guardamos el ID en la cuadrícula
			gpuMatrix[r][c] = networkDeviceID
			gpuID++
		}
	}

	// 2. Construimos los enlaces del Mesh 2D
	b.buildNVLinkMesh(nvConnector, gpuMatrix, numRows, numCols)

	// 3. Calculamos todas las rutas de la red
	nvConnector.EstablishRoute()
}

// Aprovechando el modelado de topología directo.
// En clústeres de >100 GPUs se usaría NVLink 5 Switch.
// El Switch soporta hasta 576 GPU con conexión directa.
func (b *Builder) buildNVLinkMesh(nvConnector *nvlink.Connector, gpuMatrix [][]int, numRows, numCols int) {
	// Nro de cables NVLink que unen dos GPUs.
	// NVLink 4ta generación soporta 18 enlaces por GPU.
	// Como cada GPU tiene hasta 4 vecinos, aumentaremos a 4.
	// https://www.nvidia.com/es-la/data-center/nvlink/
	numLinks := 4

	// Filas
	for r := 0; r < numRows; r++ {
		for c := 0; c < numCols-1; c++ {
			idA := gpuMatrix[r][c]
			idB := gpuMatrix[r][c+1]

			if idA != -1 && idB != -1 { // Ignorar huecos
				nvConnector.ConnectDevicesWithNVLink(idA, idB, numLinks)
			}
		}
	}

	// Columnas
	for c := 0; c < numCols; c++ {
		for r := 0; r < numRows-1; r++ {
			idA := gpuMatrix[r][c]
			idB := gpuMatrix[r+1][c]

			if idA != -1 && idB != -1 {
				nvConnector.ConnectDevicesWithNVLink(idA, idB, numLinks)
			}
		}
	}
}

func (b *Builder) createPMCPageTable() *mem.BankedAddressPortMapper {
	pmcAddressTable := new(mem.BankedAddressPortMapper)
	pmcAddressTable.BankSize = 4 * mem.GB
	pmcAddressTable.LowModules = append(pmcAddressTable.LowModules, "")
	return pmcAddressTable
}

func (b *Builder) createRDMAAddrTable() *mem.BankedAddressPortMapper {
	rdmaAddressTable := new(mem.BankedAddressPortMapper)
	rdmaAddressTable.BankSize = 4 * mem.GB
	rdmaAddressTable.LowModules = append(rdmaAddressTable.LowModules, "")
	return rdmaAddressTable
}

func (b *Builder) createConnection(
	gpuDriver *driver.Driver,
	mmuComp *mmu.Comp,
) (*nvlink.Connector, int) {
	// 1. Inicializamos el conector (PCIe + NVLink + Ethernet)
	nvConnector := nvlink.NewConnector().
		WithEngine(b.simulation.GetEngine()).
		WithFrequency(1 * sim.GHz)

	// 2. Creamos la red base
	nvConnector.CreateNetwork("SupercomputerFabric")

	// 3. Añadimos la CPU (Root Complex) a la red
	rootComplexID := nvConnector.AddRootComplex(
		[]sim.Port{
			gpuDriver.GetPortByName("GPU"),
			mmuComp.GetPortByName("Migration"),
		})

	return nvConnector, rootComplexID
}

func (b *Builder) createRDMAAddressMapper() {
	b.rdmaAddressMapper = new(mem.BankedAddressPortMapper)
	b.rdmaAddressMapper.BankSize = b.gpuMemSize
	b.rdmaAddressMapper.LowModules = append(b.rdmaAddressMapper.LowModules,
		sim.RemotePort("CPU"))
}

func (b *Builder) createGPU(
	index int,
	gpuBuilder r9nano.Builder, // Utiliza la plantilla de 'r9nano' para construir un GPU con nombre y ID único.
	gpuDriver *driver.Driver,
	pmcAddressTable *mem.BankedAddressPortMapper,
) *sim.Domain {
	name := fmt.Sprintf("GPU[%d]", index)
	memAddrOffset := uint64(index) * 4 * mem.GB // Asigna un desplazamiento de dirección de memoria
	gpu := gpuBuilder.
		WithGPUID(uint64(index)).
		WithMemAddrOffset(memAddrOffset).
		WithRDMAAddressMapper(b.rdmaAddressMapper).
		Build(name)

	gpuDriver.RegisterGPU(
		gpu.GetPortByName("CommandProcessor"),
		driver.DeviceProperties{
			CUCount:  b.numCUPerSA * b.numSAPerGPU,
			DRAMSize: 4 * mem.GB,
		},
	)
	// gpu.CommandProcessor.Driver = gpuDriver.GetPortByName("GPU")

	b.configRDMAEngine(gpu)
	// b.configPMC(gpu, gpuDriver, pmcAddressTable)

	// Necesitamos capturar el deviceID que returnea el conector al enchufar la GPU.
	//pcieConnector.PlugInDevice(pcieSwitchID, gpu.Ports())

	// b.gpus = append(b.gpus, gpu)

	return gpu
}

func (b *Builder) configRDMAEngine(
	gpu *sim.Domain,
) {
	b.rdmaAddressMapper.LowModules = append(
		b.rdmaAddressMapper.LowModules,
		gpu.GetPortByName("RDMAData").AsRemote())
}

// func (b *Builder) configPMC(
// 	gpu *GPU,
// 	gpuDriver *driver.Driver,
// 	addrTable *mem.BankedAddressPortMapper,
// ) {
// 	gpu.PMC.RemotePMCAddressTable = addrTable
// 	addrTable.LowModules = append(
// 		addrTable.LowModules,
// 		gpu.PMC.GetPortByName("Remote").AsRemote())
// 	gpuDriver.RemotePMCPorts = append(
// 		gpuDriver.RemotePMCPorts, gpu.PMC.GetPortByName("Remote"))
// }
