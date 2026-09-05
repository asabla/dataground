package authz

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"unicode"
	"unicode/utf8"

	cedar "github.com/cedar-policy/cedar-go"
)

const MaximumInvocationCedarEntitySnapshotBytes = 1 << 20

const maximumInvocationCedarEntities = 4096

const (
	invocationAuthorizationPolicyV2DigestDomain = "dataground.invocation-authorization-policy/v2\x00"
	invocationAuthorizationPolicyV3DigestDomain = "dataground.invocation-authorization-policy/v3\x00"
	invocationAuthorizationPolicyV4DigestDomain = "dataground.invocation-authorization-policy/v4\x00"
)

const maximumInvocationCedarEntityIDBytes = 256

const (
	invocationCedarActorType = "DataGround::Actor"
	invocationCedarRoleType  = "DataGround::Role"
)

type invocationCedarEntityUID struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type invocationCedarEntity struct {
	UID     invocationCedarEntityUID   `json:"uid"`
	Attrs   map[string]json.RawMessage `json:"attrs"`
	Parents []invocationCedarEntityUID `json:"parents"`
	Tags    map[string]json.RawMessage `json:"tags"`
}

func ParseInvocationCedarEntitySnapshot(encoded []byte) (cedar.EntityMap, error) {
	if len(encoded) == 0 || len(encoded) > MaximumInvocationCedarEntitySnapshotBytes {
		return nil, errors.New("invocation Cedar entity snapshot size is invalid")
	}
	var document []invocationCedarEntity
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, errors.New("invocation Cedar entity snapshot is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF ||
		len(document) == 0 ||
		len(document) > maximumInvocationCedarEntities {
		return nil, errors.New("invocation Cedar entity snapshot is invalid")
	}

	identities := make(map[invocationCedarEntityUID]struct{}, len(document))
	for _, entity := range document {
		if !validInvocationCedarEntityUID(entity.UID) ||
			entity.Attrs == nil ||
			len(entity.Attrs) != 0 ||
			entity.Tags == nil ||
			len(entity.Tags) != 0 {
			return nil, errors.New("invocation Cedar entity is invalid")
		}
		if _, exists := identities[entity.UID]; exists {
			return nil, errors.New("invocation Cedar entity identity is duplicated")
		}
		identities[entity.UID] = struct{}{}
		parents := make(map[invocationCedarEntityUID]struct{}, len(entity.Parents))
		for _, parent := range entity.Parents {
			if entity.UID.Type != invocationCedarActorType ||
				parent.Type != invocationCedarRoleType ||
				!validInvocationCedarEntityUID(parent) {
				return nil, errors.New("invocation Cedar entity parent is invalid")
			}
			if _, exists := parents[parent]; exists {
				return nil, errors.New("invocation Cedar entity parent is duplicated")
			}
			parents[parent] = struct{}{}
		}
	}
	for _, entity := range document {
		for _, parent := range entity.Parents {
			if _, exists := identities[parent]; !exists {
				return nil, errors.New("invocation Cedar entity parent is missing")
			}
		}
	}

	var entities cedar.EntityMap
	if err := json.Unmarshal(encoded, &entities); err != nil ||
		len(entities) != len(document) {
		return nil, errors.New("invocation Cedar entity snapshot is invalid")
	}
	canonical, err := json.Marshal(entities)
	if err != nil || !bytes.Equal(canonical, encoded) {
		return nil, errors.New("invocation Cedar entity snapshot is not canonical")
	}
	return entities, nil
}

func InvocationAuthorizationPolicyV2Digest(
	schema []byte,
	policies []byte,
	entities []byte,
) [sha256.Size]byte {
	return invocationAuthorizationPolicyEntityDigest(
		invocationAuthorizationPolicyV2DigestDomain, schema, policies, entities,
	)
}

func InvocationAuthorizationPolicyV3Digest(
	schema []byte,
	policies []byte,
	entities []byte,
) [sha256.Size]byte {
	return invocationAuthorizationPolicyEntityDigest(
		invocationAuthorizationPolicyV3DigestDomain, schema, policies, entities,
	)
}

func InvocationAuthorizationPolicyV4Digest(
	schema []byte,
	policies []byte,
	entities []byte,
) [sha256.Size]byte {
	return invocationAuthorizationPolicyEntityDigest(
		invocationAuthorizationPolicyV4DigestDomain, schema, policies, entities,
	)
}

func invocationAuthorizationPolicyEntityDigest(
	domain string,
	schema []byte,
	policies []byte,
	entities []byte,
) [sha256.Size]byte {
	digest := sha256.New()
	_, _ = digest.Write([]byte(domain))
	var size [8]byte
	for _, content := range [][]byte{schema, policies, entities} {
		binary.BigEndian.PutUint64(size[:], uint64(len(content)))
		_, _ = digest.Write(size[:])
		_, _ = digest.Write(content)
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func validInvocationCedarEntityUID(uid invocationCedarEntityUID) bool {
	if uid.Type != invocationCedarActorType && uid.Type != invocationCedarRoleType {
		return false
	}
	if uid.ID == "" ||
		len(uid.ID) > maximumInvocationCedarEntityIDBytes ||
		!utf8.ValidString(uid.ID) {
		return false
	}
	for _, character := range uid.ID {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
