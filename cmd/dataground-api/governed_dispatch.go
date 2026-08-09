package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/asabla/dataground/internal/persistence"
)

const (
	governedDispatchConfigurationEnvironment  = "DATAGROUND_GOVERNED_DISPATCH_CONFIG_FILE"
	governedDispatchConfigurationContract     = "dataground.api-governed-dispatch/v1"
	maximumGovernedDispatchConfigurationBytes = 4 << 10
)

type governedDispatchConfiguration struct {
	Contract          string `json:"contract"`
	IsolationDomainID string `json:"isolationDomainId"`
	ServiceID         string `json:"serviceId"`
	RevisionID        string `json:"revisionId"`
	RuntimeProfile    string `json:"runtimeProfile"`
}

func loadGovernedDispatchTarget(
	lookup func(string) (string, bool),
) (*persistence.InvocationDispatchTarget, error) {
	path, configured := lookup(governedDispatchConfigurationEnvironment)
	if !configured {
		return nil, nil
	}
	if path == "" {
		return nil, errors.New("governed dispatch configuration path must not be empty")
	}
	encoded, err := readStableConfigurationFile(path, maximumGovernedDispatchConfigurationBytes)
	if err != nil {
		return nil, errors.New("governed dispatch configuration file is invalid")
	}
	defer clear(encoded)
	if err := requireUniqueConfigurationJSON(encoded); err != nil {
		return nil, errors.New("governed dispatch configuration file is invalid")
	}
	var configuration governedDispatchConfiguration
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configuration); err != nil {
		return nil, errors.New("governed dispatch configuration file is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("governed dispatch configuration file is invalid")
	}
	target := persistence.InvocationDispatchTarget{
		IsolationDomainID: configuration.IsolationDomainID,
		ServiceID:         configuration.ServiceID,
		RevisionID:        configuration.RevisionID,
		RuntimeProfile:    configuration.RuntimeProfile,
	}
	if configuration.Contract != governedDispatchConfigurationContract ||
		!target.Valid() {
		return nil, errors.New("governed dispatch configuration is incomplete or unsupported")
	}
	return &target, nil
}
