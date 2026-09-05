package authz

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	cedar "github.com/cedar-policy/cedar-go"

	"github.com/asabla/dataground/internal/authn"
)

const (
	maximumPolicyBytes = 1 << 20
	actionEntityType   = "DataGround::Action"
)

var (
	ErrDenied      = errors.New("authorization is denied")
	ErrInvalid     = errors.New("authorization request is invalid")
	ErrUnavailable = errors.New("authorization is unavailable")

	policySetIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	resourceIDPattern  = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,127}$`)
	domainIDPattern    = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)
)

type Action string

const (
	CreateAgentService        Action = "createAgentService"
	ListAgentServices         Action = "listAgentServices"
	CreateServiceRevision     Action = "createServiceRevision"
	ListServiceRevisions      Action = "listServiceRevisions"
	PublishServiceRevision    Action = "publishServiceRevision"
	ReadServiceAlias          Action = "readServiceAlias"
	AssignServiceAlias        Action = "assignServiceAlias"
	InvokeAgentService        Action = "invokeAgentService"
	ListInvocations           Action = "listInvocations"
	ReadInvocation            Action = "readInvocation"
	ReadOperation             Action = "readOperation"
	CancelInvocation          Action = "cancelInvocation"
	ReadInvocationApproval    Action = "readInvocationApproval"
	ResolveInvocationApproval Action = "resolveInvocationApproval"
	ReadInvocationEvents      Action = "readInvocationEvents"
	ReadInvocationArtifact    Action = "readInvocationArtifact"
)

type ResourceType string

const (
	IsolationDomain    ResourceType = "DataGround::IsolationDomain"
	AgentService       ResourceType = "DataGround::AgentService"
	ServiceRevision    ResourceType = "DataGround::ServiceRevision"
	Invocation         ResourceType = "DataGround::Invocation"
	InvocationApproval ResourceType = "DataGround::InvocationApproval"
	Operation          ResourceType = "DataGround::Operation"
	Artifact           ResourceType = "DataGround::Artifact"
)

type Request struct {
	Principal         authn.Principal
	Action            Action
	ResourceType      ResourceType
	ResourceID        string
	IsolationDomainID string
	CorrelationID     string
}

type Authorizer interface {
	Authorize(context.Context, Request) error
}

type StaticCedarConfig struct {
	PolicySetID string
	Schema      []byte
	Policies    []byte
}

type StaticCedarAuthorizer struct {
	policies *cedar.PolicySet
	policy   PolicyDescriptor
}

func NewStaticCedarAuthorizer(config StaticCedarConfig) (*StaticCedarAuthorizer, error) {
	if !policySetIDPattern.MatchString(config.PolicySetID) ||
		!bytes.Equal(config.Schema, []byte(apiCedarSchemaV1)) ||
		len(config.Policies) == 0 ||
		len(config.Policies) > maximumPolicyBytes {
		return nil, errors.New("API Cedar policy configuration is invalid")
	}
	policies, err := cedar.NewPolicySetFromBytes(
		config.PolicySetID+".cedar",
		append([]byte(nil), config.Policies...),
	)
	if err != nil {
		return nil, errors.New("API Cedar policy configuration is invalid")
	}
	for range policies.All() {
		return &StaticCedarAuthorizer{
			policies: policies,
			policy: PolicyDescriptor{
				PolicySetID: config.PolicySetID,
				Digest:      authorizationPolicyDigest(config.Schema, config.Policies),
			},
		}, nil
	}
	return nil, errors.New("API Cedar policy configuration is empty")
}

func NewDevelopmentCedarAuthorizer(principalID, isolationDomainID string) (*StaticCedarAuthorizer, error) {
	if !resourceIDPattern.MatchString(principalID) || !domainIDPattern.MatchString(isolationDomainID) {
		return nil, errors.New("development authorization scope is invalid")
	}
	policy := fmt.Sprintf(
		`permit (
  principal == DataGround::User::"%s",
  action,
  resource
)
when { context.isolationDomainID == "%s" };`,
		principalID,
		isolationDomainID,
	)
	return NewStaticCedarAuthorizer(StaticCedarConfig{
		PolicySetID: "dataground-development-api",
		Schema:      CanonicalAPICedarSchema(),
		Policies:    []byte(policy),
	})
}

func CanonicalAPICedarSchema() []byte {
	return []byte(apiCedarSchemaV1)
}

func (authorizer *StaticCedarAuthorizer) AuthorizationPolicy() PolicyDescriptor {
	if authorizer == nil {
		return PolicyDescriptor{}
	}
	return authorizer.policy
}

func authorizationPolicyDigest(schema, policies []byte) string {
	digest := sha256.New()
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(schema)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write(schema)
	binary.BigEndian.PutUint64(size[:], uint64(len(policies)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write(policies)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func (authorizer *StaticCedarAuthorizer) Authorize(ctx context.Context, request Request) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if authorizer == nil || authorizer.policies == nil {
		return ErrUnavailable
	}
	cedarRequest, err := mapCedarRequest(request)
	if err != nil {
		return ErrInvalid
	}
	decision, diagnostic := cedar.Authorize(authorizer.policies, nil, cedarRequest)
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(diagnostic.Errors) != 0 {
		return ErrUnavailable
	}
	if decision == cedar.Deny {
		return ErrDenied
	}
	return nil
}

func (*StaticCedarAuthorizer) MarshalJSON() ([]byte, error) {
	return nil, errors.New("authorizers cannot be serialized")
}

func mapCedarRequest(request Request) (cedar.Request, error) {
	if !validRequest(request) {
		return cedar.Request{}, ErrInvalid
	}
	principalType, ok := cedarPrincipalType(request.Principal.Kind())
	if !ok {
		return cedar.Request{}, ErrInvalid
	}
	return cedar.Request{
		Principal: cedar.NewEntityUID(cedar.EntityType(principalType), cedar.String(request.Principal.ID())),
		Action:    cedar.NewEntityUID(cedar.EntityType(actionEntityType), cedar.String(request.Action)),
		Resource:  cedar.NewEntityUID(cedar.EntityType(request.ResourceType), cedar.String(request.ResourceID)),
		Context: cedar.NewRecord(cedar.RecordMap{
			"isolationDomainID": cedar.String(request.IsolationDomainID),
		}),
	}, nil
}

func validRequest(request Request) bool {
	if !request.Principal.Valid() ||
		!domainIDPattern.MatchString(request.IsolationDomainID) ||
		!request.Principal.AllowsIsolationDomain(request.IsolationDomainID) ||
		!validActionResource(request.Action, request.ResourceType) ||
		!resourceIDPattern.MatchString(request.ResourceID) {
		return false
	}
	return true
}

func validActionResource(action Action, resourceType ResourceType) bool {
	switch action {
	case CreateAgentService, ListAgentServices:
		return resourceType == IsolationDomain
	case CreateServiceRevision, ListServiceRevisions, ReadServiceAlias, AssignServiceAlias, InvokeAgentService, ListInvocations:
		return resourceType == AgentService
	case PublishServiceRevision:
		return resourceType == ServiceRevision
	case ReadInvocation, CancelInvocation, ReadInvocationEvents:
		return resourceType == Invocation
	case ReadInvocationApproval, ResolveInvocationApproval:
		return resourceType == InvocationApproval
	case ReadOperation:
		return resourceType == Operation
	case ReadInvocationArtifact:
		return resourceType == Artifact
	default:
		return false
	}
}

func cedarPrincipalType(kind authn.PrincipalKind) (string, bool) {
	switch kind {
	case authn.PrincipalHuman:
		return "DataGround::User", true
	case authn.PrincipalService:
		return "DataGround::ServicePrincipal", true
	case authn.PrincipalPlatformService:
		return "DataGround::PlatformService", true
	case authn.PrincipalSandboxWorkload, authn.PrincipalDistributedCompute:
		return "DataGround::Workload", true
	default:
		return "", false
	}
}

const apiCedarSchemaV1 = `{"DataGround":{"entityTypes":{"User":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}},"ServicePrincipal":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}},"PlatformService":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}},"Workload":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}},"IsolationDomain":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}},"AgentService":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}},"ServiceRevision":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}},"Invocation":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}},"InvocationApproval":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}},"Operation":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}},"Artifact":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}}},"actions":{"createAgentService":{"appliesTo":{"principalTypes":["User","ServicePrincipal","PlatformService","Workload"],"resourceTypes":["IsolationDomain"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true}},"additionalAttributes":false}}},"listAgentServices":{"appliesTo":{"principalTypes":["User","ServicePrincipal","PlatformService","Workload"],"resourceTypes":["IsolationDomain"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true}},"additionalAttributes":false}}},"createServiceRevision":{"appliesTo":{"principalTypes":["User","ServicePrincipal","PlatformService","Workload"],"resourceTypes":["AgentService"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true}},"additionalAttributes":false}}},"listServiceRevisions":{"appliesTo":{"principalTypes":["User","ServicePrincipal","PlatformService","Workload"],"resourceTypes":["AgentService"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true}},"additionalAttributes":false}}},"publishServiceRevision":{"appliesTo":{"principalTypes":["User","ServicePrincipal","PlatformService","Workload"],"resourceTypes":["ServiceRevision"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true}},"additionalAttributes":false}}},"readServiceAlias":{"appliesTo":{"principalTypes":["User","ServicePrincipal","PlatformService","Workload"],"resourceTypes":["AgentService"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true}},"additionalAttributes":false}}},"assignServiceAlias":{"appliesTo":{"principalTypes":["User","ServicePrincipal","PlatformService","Workload"],"resourceTypes":["AgentService"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true}},"additionalAttributes":false}}},"invokeAgentService":{"appliesTo":{"principalTypes":["User","ServicePrincipal","PlatformService","Workload"],"resourceTypes":["AgentService"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true}},"additionalAttributes":false}}},"readInvocation":{"appliesTo":{"principalTypes":["User","ServicePrincipal","PlatformService","Workload"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true}},"additionalAttributes":false}}},"readOperation":{"appliesTo":{"principalTypes":["User","ServicePrincipal","PlatformService","Workload"],"resourceTypes":["Operation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true}},"additionalAttributes":false}}},"cancelInvocation":{"appliesTo":{"principalTypes":["User","ServicePrincipal","PlatformService","Workload"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true}},"additionalAttributes":false}}},"readInvocationApproval":{"appliesTo":{"principalTypes":["User","ServicePrincipal","PlatformService","Workload"],"resourceTypes":["InvocationApproval"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true}},"additionalAttributes":false}}},"resolveInvocationApproval":{"appliesTo":{"principalTypes":["User","ServicePrincipal","PlatformService","Workload"],"resourceTypes":["InvocationApproval"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true}},"additionalAttributes":false}}},"readInvocationEvents":{"appliesTo":{"principalTypes":["User","ServicePrincipal","PlatformService","Workload"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true}},"additionalAttributes":false}}},"readInvocationArtifact":{"appliesTo":{"principalTypes":["User","ServicePrincipal","PlatformService","Workload"],"resourceTypes":["Artifact"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true}},"additionalAttributes":false}}},"listInvocations":{"appliesTo":{"principalTypes":["User","ServicePrincipal","PlatformService","Workload"],"resourceTypes":["AgentService"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true}},"additionalAttributes":false}}}}}}`

var _ Authorizer = (*StaticCedarAuthorizer)(nil)
var _ json.Marshaler = (*StaticCedarAuthorizer)(nil)
