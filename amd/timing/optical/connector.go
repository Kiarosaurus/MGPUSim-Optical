package optical

import (
	"fmt"

	"github.com/sarchlab/akita/v4/sim"
	"github.com/sarchlab/akita/v4/simulation"
)

// defaultSwitchingDelay es el tiempo de reconfiguración del MZI de FlexFly.
const defaultSwitchingDelay sim.VTimeInSec = 820e-9

// fiberLatency modela una fibra intra-rack de 2 metros: t = 2 m / (3e8/1.5) m/s =~ 10 ns.
const fiberLatency sim.VTimeInSec = 1e-8

// Cada GPU/memory port se conecta a un puerto del Switch mediante un Link de fibra óptica.
type Connector struct {
	Simulation *simulation.Simulation
	Switch     *Switch
	portID     int
}

func NewConnector(sim *simulation.Simulation) *Connector {
	sw := NewSwitch("OpticalSwitch", sim.GetEngine(), defaultSwitchingDelay)
	return &Connector{
		Simulation: sim,
		Switch:     sw,
		portID:     0,
	}
}

// PlugIn conecta gpuPort al Switch mediante un nuevo Link de fibra,
// registra una ruta bidireccional, y registra el Link con el monitor de la simulación.
func (c *Connector) PlugIn(gpuPort sim.Port) {
	switchPortName := fmt.Sprintf("Port[%d]", c.portID)
	c.portID++
	switchPort := c.Switch.CreatePort(switchPortName)

	cableName := fmt.Sprintf("Fiber[%d]", c.portID)
	cable := NewLink(cableName, c.Simulation.GetEngine(), fiberLatency)

	c.Simulation.RegisterComponent(cable)

	cable.PlugIn(gpuPort)
	cable.PlugIn(switchPort)

	c.Switch.RegisterRoute(gpuPort.AsRemote(), switchPort)
}
