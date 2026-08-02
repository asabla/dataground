package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"runtime/debug"

	"github.com/asabla/dataground/internal/persistence"
)

const maximumAuthenticationCapacityEvidenceBytes = 64 << 10

func currentOIDCSecurityBuild() (string, string, error) {
	build, ok := debug.ReadBuildInfo()
	if !ok {
		return "", "", errors.New("OIDC security requires clean source revision metadata")
	}
	var revision, modified string
	var revisionSet, modifiedSet bool
	for _, setting := range build.Settings {
		switch setting.Key {
		case "vcs.revision":
			if revisionSet {
				return "", "", errors.New("OIDC security requires unambiguous source revision metadata")
			}
			revision = setting.Value
			revisionSet = true
		case "vcs.modified":
			if modifiedSet {
				return "", "", errors.New("OIDC security requires unambiguous source revision metadata")
			}
			modified = setting.Value
			modifiedSet = true
		}
	}
	decodedRevision, decodeErr := hex.DecodeString(revision)
	if decodeErr != nil || len(decodedRevision) != 20 || hex.EncodeToString(decodedRevision) != revision ||
		modified != "false" {
		return "", "", errors.New("OIDC security requires clean source revision metadata")
	}
	return revision, runtime.Version(), nil
}

func loadAuthenticationRateLimitCapacityEvidence(
	path string,
	expectedDigestHex string,
	sourceRevision string,
	goVersion string,
	deploymentProfile string,
	policy persistence.AuthenticationRateLimitPolicy,
) (persistence.AuthenticationRateLimitCapacityEvidence, error) {
	var evidence persistence.AuthenticationRateLimitCapacityEvidence
	expectedDigest, err := hex.DecodeString(expectedDigestHex)
	if err != nil || len(expectedDigest) != sha256.Size ||
		hex.EncodeToString(expectedDigest) != expectedDigestHex {
		return evidence, errors.New("authentication admission capacity evidence digest is invalid")
	}
	encoded, err := readStablePrivateConfigurationFile(path, maximumAuthenticationCapacityEvidenceBytes)
	if err != nil {
		return evidence, errors.New("authentication admission capacity evidence file is invalid")
	}
	defer clear(encoded)
	digest := sha256.Sum256(encoded)
	if subtle.ConstantTimeCompare(digest[:], expectedDigest) != 1 {
		return evidence, errors.New("authentication admission capacity evidence digest does not match")
	}
	if err := requireUniqueConfigurationJSON(encoded); err != nil {
		return evidence, errors.New("authentication admission capacity evidence is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil {
		return evidence, errors.New("authentication admission capacity evidence is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return evidence, errors.New("authentication admission capacity evidence is invalid")
	}
	if !evidence.AcceptedFor(sourceRevision, deploymentProfile, goVersion, policy) {
		return evidence, errors.New("authentication admission capacity evidence is not accepted for this deployment")
	}
	return evidence, nil
}
