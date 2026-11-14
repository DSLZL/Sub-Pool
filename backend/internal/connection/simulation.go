package connection

import (
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"strings"
)

const simulationProtocol = "sub-pool-simulator-v1"
const simulationPrefix = "/api/v1/sub-pool-simulator/v1"

// SimulationProvenance survives replacement and keeps synthetic facts distinguishable from native facts.
type SimulationProvenance struct {
	Protocol    string `json:"protocol"`
	Environment string `json:"environment"`
	Epoch       string `json:"epoch"`
}

// ConfigureSimulation must run before serving or starting workers; an opt-in never qualifies native capabilities.
func (s *Service) ConfigureSimulation(binding string) error {
	if binding != "" {
		parts := strings.Split(binding, ":")
		if !s.allowHTTP || len(parts) != 3 || !managedID(parts[0]) || !validRevision(parts[1]) || !managedID(parts[2]) {
			return errors.New("invalid SUB_POOL_SIMULATION_BINDING; loopback HTTP and source:revision:epoch are required")
		}
	}
	s.simulationBinding = binding
	return nil
}

func (s *Service) simulationEpoch(c storedConfig) string {
	if !s.allowHTTP || c.SourceID == nil || c.CredentialKind == nil || !c.CredentialSet || !c.SecretStorageAvailable || c.reverificationRequired {
		return ""
	}
	parts := strings.Split(s.simulationBinding, ":")
	if len(parts) != 3 || parts[0] != *c.SourceID || parts[1] != c.Revision {
		return ""
	}
	base, err := url.Parse(c.BaseURL)
	if err != nil || base.Scheme != "http" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return ""
	}
	ip, err := netip.ParseAddr(base.Hostname())
	if err != nil || !ip.IsLoopback() {
		return ""
	}
	return parts[2]
}

func (s *Service) collectionSourceAvailable(a storedAccount, c storedConfig) bool {
	return a.SourceMode == "passive" || a.SourceMode == "active" && s.simulationEpoch(c) != ""
}

func (s *Service) simulationQuota(body json.RawMessage, c storedConfig) (json.RawMessage, *SimulationProvenance, bool) {
	fields, valid := usageObject(body)
	if !valid || len(fields) != 5 || !json.Valid(body) {
		return nil, nil, false
	}
	var provenance SimulationProvenance
	var readOnly bool
	if decode(fields["protocol"], &provenance.Protocol) != nil ||
		decode(fields["environment"], &provenance.Environment) != nil ||
		decode(fields["epoch"], &provenance.Epoch) != nil ||
		decode(fields["read_only"], &readOnly) != nil ||
		provenance.Protocol != simulationProtocol || provenance.Environment != "simulated" ||
		!readOnly || provenance.Epoch == "" || provenance.Epoch != s.simulationEpoch(c) {
		return nil, nil, false
	}
	account, exists := fields["account"]
	if !exists {
		return nil, nil, false
	}
	return account, &provenance, true
}
