package auditseal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	RevocationNoticePurposeRecipientProof   = "recipient-proof"
	RevocationNoticePurposeWorkloadIdentity = "workload-identity"

	revocationSourceRegistryContract        = "dataground.audit-export-revocation-source-registry/v1"
	revocationSourceCredentialContract      = "dataground.audit-export-revocation-source-credential/v1"
	maximumRevocationSourceRegistryBytes    = 256 << 10
	maximumRevocationSourceCredentialBytes  = 16 << 10
	maximumRevocationSourceBearerTokenBytes = 8 << 10
	maximumRevocationSources                = 64
	maximumRevocationCredentialLifetime     = 24 * time.Hour
	maximumRevocationResponseHeaderBytes    = 32 << 10
)

var (
	ErrRevocationNoticeAcquisitionInvalid     = errors.New("audit export revocation notice acquisition is invalid")
	ErrRevocationNoticeAcquisitionUnavailable = errors.New("audit export revocation notice acquisition is unavailable")
)

// RevocationNoticeAcquisitionConfig selects one immutable, digest-pinned source
// profile. Registry and credential files remain deployment-owned inputs.
type RevocationNoticeAcquisitionConfig struct {
	IsolationDomainID    string
	Purpose              string
	SourceID             string
	SourceRegistryFile   string
	SourceRegistrySHA256 string
	Transport            *http.Transport
}

// AcquiredRevocationNotice carries verified signed evidence plus minimized
// source provenance. Exactly one purpose-specific result is non-nil.
type AcquiredRevocationNotice struct {
	Purpose              string
	SourceID             string
	SourceRegistrySHA256 string
	NoticeCredential     RevocationSourceCredentialEvidence
	TrustCredential      RevocationSourceCredentialEvidence
	RecipientProof       *VerifiedRecipientProofRevocation
	WorkloadIdentity     *VerifiedWorkloadIdentityRevocation
}

// RevocationSourceEvidence identifies one validated profile in a canonical
// source registry without loading either endpoint credential.
type RevocationSourceEvidence struct {
	Purpose              string
	SourceID             string
	SourceRegistrySHA256 string
}

// RevocationSourceCredentialEvidence binds one loaded endpoint token without
// exposing it. The digest covers the exact canonical credential document.
type RevocationSourceCredentialEvidence struct {
	Endpoint         string
	CredentialSHA256 string
	ActivatedAt      time.Time
	ExpiresAt        time.Time
}

// InspectRevocationSourceRegistryFile validates the complete canonical
// registry and returns the exact digest of the selected source profile's
// containing registry. Endpoint credentials are intentionally not read.
func InspectRevocationSourceRegistryFile(
	registryFile string,
	purpose string,
	sourceID string,
) (RevocationSourceEvidence, error) {
	var evidence RevocationSourceEvidence
	if !validRevocationNoticePurpose(purpose) ||
		!auditExportDeliveryRecipientPattern.MatchString(sourceID) {
		return evidence, ErrRevocationNoticeAcquisitionInvalid
	}
	encoded, err := readStablePrivateFile(registryFile, maximumRevocationSourceRegistryBytes)
	if err != nil {
		return evidence, ErrRevocationNoticeAcquisitionInvalid
	}
	defer clear(encoded)
	if _, err := selectRevocationSourceProfile(encoded, purpose, sourceID); err != nil {
		return evidence, err
	}
	digest := sha256.Sum256(encoded)
	return RevocationSourceEvidence{
		Purpose: purpose, SourceID: sourceID, SourceRegistrySHA256: digestString(digest),
	}, nil
}

// InspectRevocationSourceCredentialFile validates one exact endpoint
// credential and returns only its non-secret authorization evidence.
func InspectRevocationSourceCredentialFile(
	credentialFile string,
	config RevocationNoticeAcquisitionConfig,
	endpoint string,
	now time.Time,
) (RevocationSourceCredentialEvidence, error) {
	if !validRevocationNoticePurpose(config.Purpose) ||
		!auditExportIsolationDomainPattern.MatchString(config.IsolationDomainID) ||
		!auditExportDeliveryRecipientPattern.MatchString(config.SourceID) ||
		!digestPattern.MatchString(config.SourceRegistrySHA256) ||
		(endpoint != "notice" && endpoint != "trust") || now.IsZero() {
		return RevocationSourceCredentialEvidence{}, ErrRevocationNoticeAcquisitionInvalid
	}
	credential, evidence, err := loadRevocationSourceCredential(
		credentialFile, config, endpoint, now.UTC(),
	)
	clear(credential.BearerToken)
	return evidence, err
}

type revocationSourceRegistry struct {
	Contract string                    `json:"contract"`
	Sources  []revocationSourceProfile `json:"sources"`
}

type revocationSourceProfile struct {
	ID                   string                         `json:"id"`
	Purpose              string                         `json:"purpose"`
	NoticeURL            string                         `json:"noticeUrl"`
	TrustURL             string                         `json:"trustUrl"`
	NoticeAuthentication revocationSourceAuthentication `json:"noticeAuthentication"`
	TrustAuthentication  revocationSourceAuthentication `json:"trustAuthentication"`
}

type revocationSourceAuthentication struct {
	Kind           string `json:"kind"`
	CredentialFile string `json:"credentialFile"`
}

type revocationSourceCredential struct {
	Contract             string
	IsolationDomainID    string
	SourceID             string
	SourceRegistrySHA256 string
	Endpoint             string
	ActivatedAt          time.Time
	ExpiresAt            time.Time
	BearerToken          []byte
}

type revocationSourceCredentialDocument struct {
	Contract             string          `json:"contract"`
	IsolationDomainID    string          `json:"isolationDomainId"`
	SourceID             string          `json:"sourceId"`
	SourceRegistrySHA256 string          `json:"sourceRegistrySha256"`
	Endpoint             string          `json:"endpoint"`
	ActivatedAt          time.Time       `json:"activatedAt"`
	ExpiresAt            time.Time       `json:"expiresAt"`
	BearerToken          json.RawMessage `json:"bearerToken"`
}

type RevocationNoticeAcquirer struct {
	isolationDomainID    string
	purpose              string
	sourceID             string
	sourceRegistrySHA256 string
	noticeURL            string
	trustURL             string
	client               *http.Client
	mu                   sync.Mutex
	used                 bool
	noticeCredential     revocationSourceCredential
	trustCredential      revocationSourceCredential
	noticeEvidence       RevocationSourceCredentialEvidence
	trustEvidence        RevocationSourceCredentialEvidence
}

func NewRevocationNoticeAcquirer(
	config RevocationNoticeAcquisitionConfig,
) (*RevocationNoticeAcquirer, error) {
	if !validRevocationNoticePurpose(config.Purpose) ||
		!auditExportIsolationDomainPattern.MatchString(config.IsolationDomainID) ||
		!auditExportDeliveryRecipientPattern.MatchString(config.SourceID) ||
		!digestPattern.MatchString(config.SourceRegistrySHA256) {
		return nil, ErrRevocationNoticeAcquisitionInvalid
	}
	registryBytes, err := readStablePrivateFile(config.SourceRegistryFile, maximumRevocationSourceRegistryBytes)
	if err != nil {
		return nil, ErrRevocationNoticeAcquisitionInvalid
	}
	defer clear(registryBytes)
	registryDigest := sha256.Sum256(registryBytes)
	if digestString(registryDigest) != config.SourceRegistrySHA256 {
		return nil, ErrRevocationNoticeAcquisitionInvalid
	}
	profile, err := selectRevocationSourceProfile(registryBytes, config.Purpose, config.SourceID)
	if err != nil || profile.NoticeAuthentication.CredentialFile == profile.TrustAuthentication.CredentialFile ||
		profile.NoticeAuthentication.CredentialFile == config.SourceRegistryFile ||
		profile.TrustAuthentication.CredentialFile == config.SourceRegistryFile {
		return nil, ErrRevocationNoticeAcquisitionInvalid
	}
	now := time.Now().UTC()
	noticeCredential, noticeEvidence, err := loadRevocationSourceCredential(
		profile.NoticeAuthentication.CredentialFile, config, "notice", now,
	)
	if err != nil {
		return nil, err
	}
	trustCredential, trustEvidence, err := loadRevocationSourceCredential(
		profile.TrustAuthentication.CredentialFile, config, "trust", now,
	)
	if err != nil {
		clear(noticeCredential.BearerToken)
		return nil, err
	}
	transport := config.Transport
	if transport == nil {
		defaultTransport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			clear(noticeCredential.BearerToken)
			clear(trustCredential.BearerToken)
			return nil, ErrRevocationNoticeAcquisitionUnavailable
		}
		transport = defaultTransport
	}
	transport = transport.Clone()
	transport.DisableCompression = true
	transport.MaxResponseHeaderBytes = maximumRevocationResponseHeaderBytes
	transport.Proxy = nil
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		if transport.TLSClientConfig.MinVersion == 0 || transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
			transport.TLSClientConfig.MinVersion = tls.VersionTLS12
		}
	}
	return &RevocationNoticeAcquirer{
		isolationDomainID: config.IsolationDomainID, purpose: config.Purpose,
		sourceID: config.SourceID, sourceRegistrySHA256: config.SourceRegistrySHA256,
		noticeURL: profile.NoticeURL, trustURL: profile.TrustURL,
		noticeCredential: noticeCredential, trustCredential: trustCredential,
		noticeEvidence: noticeEvidence, trustEvidence: trustEvidence,
		client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}, nil
}

func (acquirer *RevocationNoticeAcquirer) Acquire(
	ctx context.Context,
	now time.Time,
) (AcquiredRevocationNotice, error) {
	var acquired AcquiredRevocationNotice
	if acquirer == nil || acquirer.client == nil {
		return acquired, ErrRevocationNoticeAcquisitionUnavailable
	}
	noticeCredential, trustCredential, ok := acquirer.takeCredentials()
	if !ok {
		return acquired, ErrRevocationNoticeAcquisitionUnavailable
	}
	defer clear(noticeCredential.BearerToken)
	defer clear(trustCredential.BearerToken)
	if ctx == nil || now.IsZero() {
		return acquired, ErrRevocationNoticeAcquisitionInvalid
	}
	if err := ctx.Err(); err != nil {
		return acquired, err
	}
	now = now.UTC()
	if !validRevocationSourceCredentialAt(noticeCredential, now) ||
		!validRevocationSourceCredentialAt(trustCredential, now) {
		return acquired, ErrRevocationNoticeAcquisitionUnavailable
	}
	trust, err := acquirer.fetchJSON(ctx, acquirer.trustURL, trustCredential.BearerToken)
	if err != nil {
		return acquired, err
	}
	defer clear(trust)
	notice, err := acquirer.fetchJSON(ctx, acquirer.noticeURL, noticeCredential.BearerToken)
	if err != nil {
		return acquired, err
	}
	defer clear(notice)
	acquired = AcquiredRevocationNotice{
		Purpose: acquirer.purpose, SourceID: acquirer.sourceID,
		SourceRegistrySHA256: acquirer.sourceRegistrySHA256,
		NoticeCredential: acquirer.noticeEvidence, TrustCredential: acquirer.trustEvidence,
	}
	switch acquirer.purpose {
	case RevocationNoticePurposeRecipientProof:
		verified, err := VerifyRecipientProofRevocation(notice, trust, acquirer.isolationDomainID, now)
		if err != nil {
			return AcquiredRevocationNotice{}, err
		}
		acquired.RecipientProof = &verified
	case RevocationNoticePurposeWorkloadIdentity:
		verified, err := VerifyWorkloadIdentityRevocation(notice, trust, acquirer.isolationDomainID, now)
		if err != nil {
			return AcquiredRevocationNotice{}, err
		}
		acquired.WorkloadIdentity = &verified
	default:
		return AcquiredRevocationNotice{}, ErrRevocationNoticeAcquisitionInvalid
	}
	return acquired, nil
}

func (acquirer *RevocationNoticeAcquirer) fetchJSON(
	ctx context.Context,
	endpoint string,
	bearerToken []byte,
) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, ErrRevocationNoticeAcquisitionInvalid
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+string(bearerToken))
	response, err := acquirer.client.Do(request)
	request.Header.Del("Authorization")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, ErrRevocationNoticeAcquisitionUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Request == nil || response.Request.URL.String() != endpoint {
		return nil, ErrRevocationNoticeAcquisitionUnavailable
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || response.ContentLength == 0 ||
		response.ContentLength > maximumControlBytes {
		return nil, ErrRevocationNoticeAcquisitionInvalid
	}
	return readBoundedRevocationResponse(ctx, response.Body)
}

// CredentialEvidence returns the exact non-secret endpoint credential bindings
// loaded by this single-use acquirer.
func (acquirer *RevocationNoticeAcquirer) CredentialEvidence() (
	RevocationSourceCredentialEvidence,
	RevocationSourceCredentialEvidence,
	error,
) {
	if acquirer == nil || acquirer.client == nil ||
		acquirer.noticeEvidence.Endpoint != "notice" ||
		acquirer.trustEvidence.Endpoint != "trust" {
		return RevocationSourceCredentialEvidence{}, RevocationSourceCredentialEvidence{},
			ErrRevocationNoticeAcquisitionUnavailable
	}
	return acquirer.noticeEvidence, acquirer.trustEvidence, nil
}

func (acquirer *RevocationNoticeAcquirer) takeCredentials() (
	revocationSourceCredential,
	revocationSourceCredential,
	bool,
) {
	acquirer.mu.Lock()
	defer acquirer.mu.Unlock()
	if acquirer.used {
		return revocationSourceCredential{}, revocationSourceCredential{}, false
	}
	acquirer.used = true
	notice := acquirer.noticeCredential
	trust := acquirer.trustCredential
	acquirer.noticeCredential.BearerToken = nil
	acquirer.trustCredential.BearerToken = nil
	return notice, trust, true
}

// Close clears any endpoint credentials that were not consumed by Acquire.
func (acquirer *RevocationNoticeAcquirer) Close() {
	if acquirer == nil {
		return
	}
	acquirer.mu.Lock()
	defer acquirer.mu.Unlock()
	clear(acquirer.noticeCredential.BearerToken)
	clear(acquirer.trustCredential.BearerToken)
	acquirer.noticeCredential.BearerToken = nil
	acquirer.trustCredential.BearerToken = nil
	acquirer.used = true
	if acquirer.client != nil {
		acquirer.client.CloseIdleConnections()
	}
}

func selectRevocationSourceProfile(
	encoded []byte,
	purpose string,
	sourceID string,
) (revocationSourceProfile, error) {
	var registry revocationSourceRegistry
	if err := decodeCanonicalJSON(encoded, &registry, maximumRevocationSourceRegistryBytes); err != nil {
		return revocationSourceProfile{}, ErrRevocationNoticeAcquisitionInvalid
	}
	canonical, err := canonicalJSON(registry)
	if err != nil || !bytes.Equal(canonical, encoded) {
		clear(canonical)
		return revocationSourceProfile{}, ErrRevocationNoticeAcquisitionInvalid
	}
	clear(canonical)
	if registry.Contract != revocationSourceRegistryContract || len(registry.Sources) == 0 ||
		len(registry.Sources) > maximumRevocationSources {
		return revocationSourceProfile{}, ErrRevocationNoticeAcquisitionInvalid
	}
	if !sort.SliceIsSorted(registry.Sources, func(left, right int) bool {
		if registry.Sources[left].Purpose == registry.Sources[right].Purpose {
			return registry.Sources[left].ID < registry.Sources[right].ID
		}
		return registry.Sources[left].Purpose < registry.Sources[right].Purpose
	}) {
		return revocationSourceProfile{}, ErrRevocationNoticeAcquisitionInvalid
	}
	seen := make(map[string]struct{}, len(registry.Sources))
	var selected revocationSourceProfile
	for _, profile := range registry.Sources {
		key := profile.Purpose + "\n" + profile.ID
		if _, exists := seen[key]; exists || !validRevocationSourceProfile(profile) {
			return revocationSourceProfile{}, ErrRevocationNoticeAcquisitionInvalid
		}
		seen[key] = struct{}{}
		if profile.Purpose == purpose && profile.ID == sourceID {
			selected = profile
		}
	}
	if selected.ID == "" {
		return revocationSourceProfile{}, ErrRevocationNoticeAcquisitionInvalid
	}
	return selected, nil
}

func validRevocationSourceProfile(profile revocationSourceProfile) bool {
	noticeURL, noticeOK := parseRevocationSourceURL(profile.NoticeURL)
	trustURL, trustOK := parseRevocationSourceURL(profile.TrustURL)
	return auditExportDeliveryRecipientPattern.MatchString(profile.ID) &&
		validRevocationNoticePurpose(profile.Purpose) && noticeOK && trustOK &&
		profile.NoticeURL != profile.TrustURL && noticeURL.Scheme == trustURL.Scheme &&
		noticeURL.Host == trustURL.Host &&
		validRevocationSourceAuthentication(profile.NoticeAuthentication) &&
		validRevocationSourceAuthentication(profile.TrustAuthentication)
}

func validRevocationSourceAuthentication(authentication revocationSourceAuthentication) bool {
	return authentication.Kind == "bearer-credential-file" && canonicalAbsolutePath(authentication.CredentialFile)
}

func parseRevocationSourceURL(value string) (*url.URL, bool) {
	parsed, err := url.Parse(value)
	return parsed, err == nil && parsed.String() == value && parsed.Scheme == "https" &&
		parsed.Host != "" && parsed.Hostname() != "" && parsed.User == nil && parsed.Fragment == "" &&
		parsed.RawQuery == "" && parsed.RawPath == "" && !parsed.OmitHost && parsed.Path != "" &&
		path.Clean(parsed.Path) == parsed.Path
}

func loadRevocationSourceCredential(
	path string,
	config RevocationNoticeAcquisitionConfig,
	endpoint string,
	now time.Time,
) (revocationSourceCredential, RevocationSourceCredentialEvidence, error) {
	var credential revocationSourceCredential
	encoded, err := readStablePrivateFile(path, maximumRevocationSourceCredentialBytes)
	if err != nil {
		return credential, RevocationSourceCredentialEvidence{},
			ErrRevocationNoticeAcquisitionUnavailable
	}
	defer clear(encoded)
	var document revocationSourceCredentialDocument
	if err := decodeCanonicalJSON(encoded, &document, maximumRevocationSourceCredentialBytes); err != nil {
		return revocationSourceCredential{}, RevocationSourceCredentialEvidence{}, ErrRevocationNoticeAcquisitionInvalid
	}
	canonical, err := canonicalJSON(document)
	if err != nil || !bytes.Equal(canonical, encoded) {
		clear(canonical)
		return revocationSourceCredential{}, RevocationSourceCredentialEvidence{}, ErrRevocationNoticeAcquisitionInvalid
	}
	clear(canonical)
	token := bytes.TrimSpace(document.BearerToken)
	if len(token) < 3 || token[0] != '"' || token[len(token)-1] != '"' ||
		bytes.IndexByte(token[1:len(token)-1], '\\') >= 0 ||
		bytes.IndexByte(token[1:len(token)-1], '"') >= 0 {
		return revocationSourceCredential{}, RevocationSourceCredentialEvidence{}, ErrRevocationNoticeAcquisitionInvalid
	}
	credential = revocationSourceCredential{
		Contract: document.Contract, IsolationDomainID: document.IsolationDomainID,
		SourceID: document.SourceID, SourceRegistrySHA256: document.SourceRegistrySHA256,
		Endpoint: document.Endpoint, ActivatedAt: document.ActivatedAt.UTC(),
		ExpiresAt: document.ExpiresAt.UTC(), BearerToken: append([]byte(nil), token[1:len(token)-1]...),
	}
	if credential.Contract != revocationSourceCredentialContract ||
		credential.IsolationDomainID != config.IsolationDomainID || credential.SourceID != config.SourceID ||
		credential.SourceRegistrySHA256 != config.SourceRegistrySHA256 || credential.Endpoint != endpoint ||
		!validRevocationSourceCredentialAt(credential, now) {
		clear(credential.BearerToken)
		return revocationSourceCredential{}, RevocationSourceCredentialEvidence{},
			ErrRevocationNoticeAcquisitionInvalid
	}
	digest := sha256.Sum256(encoded)
	return credential, RevocationSourceCredentialEvidence{
		Endpoint: endpoint, CredentialSHA256: digestString(digest),
		ActivatedAt: credential.ActivatedAt, ExpiresAt: credential.ExpiresAt,
	}, nil
}

func validRevocationSourceCredentialAt(credential revocationSourceCredential, now time.Time) bool {
	return credential.Contract == revocationSourceCredentialContract &&
		auditExportIsolationDomainPattern.MatchString(credential.IsolationDomainID) &&
		auditExportDeliveryRecipientPattern.MatchString(credential.SourceID) &&
		digestPattern.MatchString(credential.SourceRegistrySHA256) &&
		(credential.Endpoint == "notice" || credential.Endpoint == "trust") &&
		canonicalRecipientIdentityProofTime(credential.ActivatedAt) &&
		canonicalRecipientIdentityProofTime(credential.ExpiresAt) &&
		credential.ExpiresAt.After(credential.ActivatedAt) &&
		credential.ExpiresAt.Sub(credential.ActivatedAt) <= maximumRevocationCredentialLifetime &&
		!credential.ActivatedAt.After(now) &&
		credential.ExpiresAt.After(now) && validRevocationSourceBearerToken(credential.BearerToken)
}

func validRevocationSourceBearerToken(token []byte) bool {
	if len(token) == 0 || len(token) > maximumRevocationSourceBearerTokenBytes {
		return false
	}
	padding := false
	for _, value := range token {
		if value == '=' {
			padding = true
			continue
		}
		if padding || !((value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') ||
			(value >= '0' && value <= '9') || strings.ContainsRune("-._~+/", rune(value))) {
			return false
		}
	}
	return true
}

func readBoundedRevocationResponse(ctx context.Context, reader io.Reader) ([]byte, error) {
	content := make([]byte, 0, maximumControlBytes)
	buffer := make([]byte, 16<<10)
	for {
		if err := ctx.Err(); err != nil {
			clear(content)
			return nil, err
		}
		count, err := reader.Read(buffer)
		if count > 0 {
			if len(content)+count > maximumControlBytes {
				clear(content)
				return nil, ErrRevocationNoticeAcquisitionInvalid
			}
			content = append(content, buffer[:count]...)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || count == 0 {
			clear(content)
			return nil, ErrRevocationNoticeAcquisitionUnavailable
		}
	}
	if len(content) == 0 {
		return nil, ErrRevocationNoticeAcquisitionInvalid
	}
	return content, nil
}

func validRevocationNoticePurpose(purpose string) bool {
	return purpose == RevocationNoticePurposeRecipientProof ||
		purpose == RevocationNoticePurposeWorkloadIdentity
}

func (*RevocationNoticeAcquirer) MarshalJSON() ([]byte, error) {
	return nil, errors.New("audit export revocation notice acquirers cannot be serialized")
}
