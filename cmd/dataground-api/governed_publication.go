package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/asabla/dataground/internal/persistence"
)

const (
	governedPublicationConfigurationEnvironment  = "DATAGROUND_GOVERNED_PUBLICATION_CONFIG_FILE"
	governedPublicationConfigurationContract     = "dataground.api-governed-publication/v1"
	maximumGovernedPublicationConfigurationBytes = 4 << 10
)

type governedPublicationConfiguration struct {
	Contract           string `json:"contract"`
	IsolationDomainID  string `json:"isolationDomainId"`
	ServiceID          string `json:"serviceId"`
	RevisionID         string `json:"revisionId"`
	RuntimeProfile     string `json:"runtimeProfile"`
	ExpectedVersion    int    `json:"expectedVersion"`
	PlanDigest         string `json:"planDigest"`
	PolicyDigest       string `json:"policyDigest"`
	VerificationDigest string `json:"verificationDigest"`
}

func loadGovernedPublicationTarget(
	lookup func(string) (string, bool),
) (*persistence.DevelopmentPublicationInput, error) {
	path, configured := lookup(governedPublicationConfigurationEnvironment)
	if !configured {
		return nil, nil
	}
	if path == "" {
		return nil, errors.New("governed publication configuration path must not be empty")
	}
	encoded, err := readStableConfigurationFile(path, maximumGovernedPublicationConfigurationBytes)
	if err != nil {
		return nil, errors.New("governed publication configuration file is invalid")
	}
	defer clear(encoded)
	if err := requireUniqueConfigurationJSON(encoded); err != nil {
		return nil, errors.New("governed publication configuration file is invalid")
	}
	var configuration governedPublicationConfiguration
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configuration); err != nil {
		return nil, errors.New("governed publication configuration file is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("governed publication configuration file is invalid")
	}
	target := persistence.DevelopmentPublicationInput{Contract: persistence.DevelopmentPublicationContract, ExpectedVersion: configuration.ExpectedVersion, PlanDigest: configuration.PlanDigest, PolicyDigest: configuration.PolicyDigest, VerificationDigest: configuration.VerificationDigest, Target: persistence.InvocationDispatchTarget{
		IsolationDomainID: configuration.IsolationDomainID,
		ServiceID:         configuration.ServiceID,
		RevisionID:        configuration.RevisionID,
		RuntimeProfile:    configuration.RuntimeProfile,
	}}
	if configuration.Contract != governedPublicationConfigurationContract ||
		!target.ValidReviewedInputs() {
		return nil, errors.New("governed publication configuration is incomplete or unsupported")
	}
	return &target, nil
}
