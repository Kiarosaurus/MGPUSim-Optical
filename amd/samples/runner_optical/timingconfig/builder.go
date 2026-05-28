// Package timingconfig contains the configuration for the timing simulation.
package timingconfig

import (
	"fmt"
	"strings"

	"github.com/sarchlab/akita/v4/mem/mem"
	"github.com/sarchlab/akita/v4/mem/vm"
	"github.com/sarchlab/akita/v4/mem/vm/mmu"
	"github.com/sarchlab/akita/v4/noc/networking/pcie"
	"github.com/sarchlab/akita/v4/sim"
	"github.com/sarchlab/akita/v4/simulation"
	"github.com/sarchlab/mgpusim/v4/amd/driver"
	"github.com/sarchlab/mgpusim/v4/amd/samples/runner/timingconfig/r9nano"

	"github.com/sarchlab/mgpusim/v4/amd/timing/optical"
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

	opticalConnector *optical.Connector
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

	// Crea el conector PCIe, el Root Complex (punto de origen de PCIe en la CPU),
	// y la red PCIe a la que se conectarán las GPUs.
	pcieConnector, rootComplexID :=
		b.createConnection(gpuDriver, mmuComp)

	mmuComp.MigrationServiceProvider = gpuDriver.GetPortByName("MMU").AsRemote()

	b.createRDMAAddrTable()
	pmcAddressTable := b.createPMCPageTable()

	b.createGPUs( // Se crean e interconectan las GPUs según la lógica definida.
		rootComplexID, pcieConnector,
		gpuBuilder, gpuDriver,
		pmcAddressTable)

	pcieConnector.EstablishRoute()

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
	pcieConnector *pcie.Connector,
	gpuBuilder r9nano.Builder,
	gpuDriver *driver.Driver,
	pmcAddressTable *mem.BankedAddressPortMapper,
) {

	// Topología de Red.
	lastSwitchID := rootComplexID
	for i := 1; i < b.numGPUs+1; i++ {
		if i%2 == 1 { // Por cada 2 GPUs se crea un nuevo switch PCIe (conectado al Root Complex).
			lastSwitchID = pcieConnector.AddSwitch(rootComplexID)
		}

		// Se conectan esas 2 GPUs al switch recién creado.
		b.createGPU(i, gpuBuilder, gpuDriver, pmcAddressTable,
			pcieConnector, lastSwitchID)
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
	mmuComponent *mmu.Comp,
) (*pcie.Connector, int) {
	// Utiliza un pcie.Connector para modelar una red PCIe versión 4 con 16 lanes
	// (WithVersion(4, 16)) y una latencia de switch de 140 ciclos (WithSwitchLatency(140)).

	// connection := sim.NewDirectConnection(engine)
	// connection := noc.NewFixedBandwidthConnection(32, engine, 1*sim.GHz)
	// connection.SrcBufferCapacity = 40960000
	pcieConnector := pcie.NewConnector().
		WithEngine(b.simulation.GetEngine()).
		WithVersion(4, 16).
		WithSwitchLatency(140)

	pcieConnector.CreateNetwork("PCIe")

	// Creación del Root Complex que conecta los puertos del driver y de la MMU.
	rootComplexID := pcieConnector.AddRootComplex(
		[]sim.Port{
			gpuDriver.GetPortByName("GPU"),
			gpuDriver.GetPortByName("MMU"),
			mmuComponent.GetPortByName("Migration"),
			mmuComponent.GetPortByName("Top"),
		})

	// --- RED ÓPTICA ---
	// No necesitamos crear un "Root Complex" óptico porque es P2P.
	fmt.Println("[DEBUG_BUILDER] Creating Optical Network...")

	// 1. Instanciar Connector (Switch + Link).
	b.opticalConnector = optical.NewConnector(b.simulation)
	b.simulation.RegisterComponent(b.opticalConnector.Switch)

	found := false
	for _, c := range b.simulation.Components() {
		if c.Name() == "OpticalSwitch" {
			found = true
			break
		}
	}
	if found {
		fmt.Println("[DEBUG_BUILDER] ÉXITO: OpticalSwitch ENCONTRADO en la lista de componentes.")
	} else {
		fmt.Println("[DEBUG_BUILDER] ERROR: OpticalSwitch NO ENCONTRADO en la lista de componentes.")
	}

	fmt.Println("[DEBUG_BUILDER] Optical Network created.")
	return pcieConnector, rootComplexID
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
	pcieConnector *pcie.Connector,
	pcieSwitchID int,
) *sim.Domain {
	name := fmt.Sprintf("GPU[%d]", index)
	memAddrOffset := uint64(index) * b.gpuMemSize // Dinámico!
	gpu := gpuBuilder.
		WithGPUID(uint64(index)).
		WithMemAddrOffset(memAddrOffset).
		WithRDMAAddressMapper(b.rdmaAddressMapper).
		Build(name)

	gpuDriver.RegisterGPU(
		gpu.GetPortByName("CommandProcessor"),
		driver.DeviceProperties{
			CUCount:  b.numCUPerSA * b.numSAPerGPU,
			DRAMSize: b.gpuMemSize,
		},
	)
	// gpu.CommandProcessor.Driver = gpuDriver.GetPortByName("GPU")

	b.configRDMAEngine(gpu)
	b.configPMC(gpu, gpuDriver, pmcAddressTable)

	// pcieConnector.PlugInDevice(pcieSwitchID, gpu.Ports())

	// 1. Obtenemos TODOS los puertos de la GPU.
	ports := gpu.Ports()
	var pciePorts []sim.Port

	fmt.Printf("[DEBUG_BUILDER] Configurando puertos para GPU %d:\n", index)

	// 2. Clasificamos los puertos por red:
	// [1] Control (PCIe) o [2] Datos (Óptico).
	for _, p := range ports {
		fmt.Printf("   - Puerto analizado: '%s' -> ", p.Name())

		// En r9nano/builder.go los puertos del RDMA son:
		isRDMAReq := strings.Contains(p.Name(), "RDMARequest")
		isRDMAData := strings.Contains(p.Name(), "RDMAData")

		if isRDMAReq || isRDMAData {
			fmt.Println("RED ÓPTICA")
			b.opticalConnector.PlugIn(p) // [2] Conexión a la red óptica.
		} else {
			fmt.Println("RED PCIE")
			// [1] Conectar a PCIe ("CommandProcessor", "MMU", etc.)
			pciePorts = append(pciePorts, p)
		}
	}

	// 3. Enchufar puertos de CONTROL a su Switch PCIe.
	pcieConnector.PlugInDevice(pcieSwitchID, pciePorts)

	return gpu
}

func (b *Builder) configRDMAEngine(
	gpu *sim.Domain,
) {
	b.rdmaAddressMapper.LowModules = append(
		b.rdmaAddressMapper.LowModules,
		gpu.GetPortByName("RDMAData").AsRemote())
}

func (b *Builder) configPMC(
	gpu *sim.Domain,
	gpuDriver *driver.Driver,
	addrTable *mem.BankedAddressPortMapper,
) {
	pmcPort := gpu.GetPortByName("PageMigrationController")

	addrTable.LowModules = append(
		addrTable.LowModules,
		pmcPort.AsRemote())

	gpuDriver.RemotePMCPorts = append(
		gpuDriver.RemotePMCPorts, pmcPort)
}
