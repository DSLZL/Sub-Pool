package connection

import ()

const simulationProtocol = "sub-pool-simulator-v1"

// SimulationProvenance survives replacement and keeps synthetic facts distinguishable from native facts.
type SimulationProvenance struct {
	Protocol    string `json:"protocol"`
	Environment string `json:"environment"`
	Epoch       string `json:"epoch"`
}
