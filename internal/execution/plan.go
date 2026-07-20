package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const ExecutionPlanSchemaV1 = "dataground.execution-plan/v1"

var (
	ErrExecutionPlanMissing          = errors.New("execution plan not found")
	ErrExecutionPlanConflict         = errors.New("execution plan conflicts with persisted plan")
	ErrExecutionPlanRevisionMissing  = errors.New("execution plan revision not found")
	ErrExecutionPlanRevisionMismatch = errors.New("execution plan does not match service revision")

	isolationDomainPattern  = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)
	revisionPattern         = regexp.MustCompile(`^rev_[0-9a-z]{20,32}$`)
	digestPattern           = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	opaqueIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// ExecutionPlan is the immutable, internal bridge between a service
// revision and provider placement. It records portable references and digests;
// gateway-local paths and native provider endpoints do not belong here.
type ExecutionPlan struct {
	SchemaVersion             string   `json:"schemaVersion"`
	IsolationDomainID         string   `json:"isolationDomainId"`
	RevisionID                string   `json:"revisionId"`
	RuntimeProfile            string   `json:"runtimeProfile"`
	EnvironmentRevisionID     string   `json:"environmentRevisionId"`
	ImageReference            string   `json:"imageReference"`
	EnvironmentManifestDigest string   `json:"environmentManifestDigest"`
	EnforcementBundleID       string   `json:"enforcementBundleId"`
	EnforcementBundleDigest   string   `json:"enforcementBundleDigest"`
	RuntimeMatrixID           string   `json:"runtimeMatrixId"`
	RuntimeMatrixDigest       string   `json:"runtimeMatrixDigest"`
	ProviderProfiles          []string `json:"providerProfiles"`
	RequiredCapabilities      []string `json:"requiredCapabilities"`
}

type ExecutionPlanBinding struct {
	Plan          ExecutionPlan
	ActorID       string
	CorrelationID string
}

// ExecutionPlanStore owns an append-only binding. Replaying the same
// normalized plan succeeds; trying to replace it fails closed.
type ExecutionPlanStore interface {
	BindExecutionPlan(context.Context, ExecutionPlanBinding) (ExecutionPlan, error)
	GetExecutionPlan(context.Context, string, string) (ExecutionPlan, error)
}

func NormalizeExecutionPlan(plan ExecutionPlan) (ExecutionPlan, error) {
	if plan.SchemaVersion != ExecutionPlanSchemaV1 {
		return ExecutionPlan{}, errors.New("unsupported execution plan schema version")
	}
	if !isolationDomainPattern.MatchString(plan.IsolationDomainID) {
		return ExecutionPlan{}, errors.New("invalid execution plan isolation domain")
	}
	if !revisionPattern.MatchString(plan.RevisionID) {
		return ExecutionPlan{}, errors.New("invalid execution plan revision")
	}
	if !validPortableValue(plan.RuntimeProfile, 128) {
		return ExecutionPlan{}, errors.New("invalid execution plan runtime profile")
	}
	if !opaqueIdentifierPattern.MatchString(plan.EnvironmentRevisionID) {
		return ExecutionPlan{}, errors.New("invalid execution plan environment revision")
	}
	if !validImmutableImageReference(plan.ImageReference) {
		return ExecutionPlan{}, errors.New("execution plan image must use an immutable sha256 reference")
	}
	if !digestPattern.MatchString(plan.EnvironmentManifestDigest) {
		return ExecutionPlan{}, errors.New("invalid execution plan environment manifest digest")
	}
	if !opaqueIdentifierPattern.MatchString(plan.EnforcementBundleID) {
		return ExecutionPlan{}, errors.New("invalid execution plan enforcement bundle identifier")
	}
	if !digestPattern.MatchString(plan.EnforcementBundleDigest) {
		return ExecutionPlan{}, errors.New("invalid execution plan enforcement bundle digest")
	}
	if !opaqueIdentifierPattern.MatchString(plan.RuntimeMatrixID) {
		return ExecutionPlan{}, errors.New("invalid execution plan runtime matrix")
	}
	if !digestPattern.MatchString(plan.RuntimeMatrixDigest) {
		return ExecutionPlan{}, errors.New("invalid execution plan runtime matrix digest")
	}

	providerProfiles, err := normalizePortableValues(plan.ProviderProfiles, 128, 64, true)
	if err != nil {
		return ExecutionPlan{}, errors.New("invalid execution plan provider profile")
	}
	requiredCapabilities, err := normalizePortableValues(plan.RequiredCapabilities, 128, 256, false)
	if err != nil {
		return ExecutionPlan{}, errors.New("invalid execution plan required capability")
	}
	plan.ProviderProfiles = providerProfiles
	plan.RequiredCapabilities = requiredCapabilities
	return plan, nil
}

func NormalizeExecutionPlanBinding(binding ExecutionPlanBinding) (ExecutionPlanBinding, error) {
	plan, err := NormalizeExecutionPlan(binding.Plan)
	if err != nil {
		return ExecutionPlanBinding{}, err
	}
	if !validPortableValue(binding.ActorID, 256) {
		return ExecutionPlanBinding{}, errors.New("invalid execution plan actor")
	}
	if !validPortableValue(binding.CorrelationID, 256) {
		return ExecutionPlanBinding{}, errors.New("invalid execution plan correlation identifier")
	}
	binding.Plan = plan
	return binding, nil
}

func DigestExecutionPlan(plan ExecutionPlan) (string, error) {
	normalized, err := NormalizeExecutionPlan(plan)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", errors.New("encode execution plan digest input")
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func EqualExecutionPlans(left, right ExecutionPlan) bool {
	return left.SchemaVersion == right.SchemaVersion &&
		left.IsolationDomainID == right.IsolationDomainID &&
		left.RevisionID == right.RevisionID &&
		left.RuntimeProfile == right.RuntimeProfile &&
		left.EnvironmentRevisionID == right.EnvironmentRevisionID &&
		left.ImageReference == right.ImageReference &&
		left.EnvironmentManifestDigest == right.EnvironmentManifestDigest &&
		left.EnforcementBundleID == right.EnforcementBundleID &&
		left.EnforcementBundleDigest == right.EnforcementBundleDigest &&
		left.RuntimeMatrixID == right.RuntimeMatrixID &&
		left.RuntimeMatrixDigest == right.RuntimeMatrixDigest &&
		slices.Equal(left.ProviderProfiles, right.ProviderProfiles) &&
		slices.Equal(left.RequiredCapabilities, right.RequiredCapabilities)
}

func CloneExecutionPlan(plan ExecutionPlan) ExecutionPlan {
	plan.ProviderProfiles = slices.Clone(plan.ProviderProfiles)
	plan.RequiredCapabilities = slices.Clone(plan.RequiredCapabilities)
	return plan
}

func validImmutableImageReference(value string) bool {
	if !validPortableValue(value, 2048) || strings.Count(value, "@") != 1 {
		return false
	}
	parts := strings.SplitN(value, "@", 2)
	return parts[0] != "" && digestPattern.MatchString(parts[1])
}

func normalizePortableValues(values []string, maximumLength, maximumItems int, rejectEquals bool) ([]string, error) {
	if len(values) > maximumItems {
		return nil, errors.New("too many portable values")
	}
	normalized := slices.Clone(values)
	for _, value := range normalized {
		if !validPortableValue(value, maximumLength) || rejectEquals && strings.Contains(value, "=") {
			return nil, errors.New("invalid portable value")
		}
	}
	sort.Strings(normalized)
	return slices.Compact(normalized), nil
}

func validPortableValue(value string, maximumLength int) bool {
	if value == "" || len(value) > maximumLength || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}
	return true
}
