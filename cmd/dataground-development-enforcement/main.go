package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/asabla/dataground/internal/execution"
	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/execution/s3store"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/policy/rosetta"
)

const (
	developmentInputPath           = "deploy/openshell/codex-compatibility/rosetta-runtime-input.json"
	developmentInputSHA256         = "ebc963948fa45e278b0554f23734a16d78df7654ba140d8afdb5c0fc15aa0e89"
	developmentPolicyDigest        = "sha256:a1d56c0470c3264c4c37183352d783ebb67911d92ef2eb6ec5f7c76c61f69f39"
	developmentCompilerInputDigest = "sha256:b2895b9172c50ba7a5fdf574cebdf6789258cc8ce9f90ce5ad8f2b1ff0a825ab"
	developmentRuntimeProfile      = "codex.app-server/v1"
)

var (
	errInstallation = errors.New("development enforcement installation unavailable; retry the exact request after checking dependencies")
	domainPattern   = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)
	revisionPattern = regexp.MustCompile(`^rev_[0-9a-z]{20,32}$`)
)

type configuration struct {
	root            string
	domainID        string
	revisionID      string
	actorID         string
	correlationID   string
	rosettaEndpoint string
	s3Endpoint      string
	bucket          string
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	config, err := parseConfiguration(args)
	if err != nil {
		return err
	}
	request, err := readCheckedRequest(config)
	if err != nil {
		return err
	}
	databaseURL := os.Getenv("DATAGROUND_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATAGROUND_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	compiler, err := rosetta.New(rosetta.Config{Endpoint: config.rosettaEndpoint, ExpectedCompilerVersion: rosetta.CompilerVersionV1, ExpectedTargetContract: rosetta.OpenShellTargetContractV1, AllowInsecureLoopback: true}, client)
	if err != nil {
		return errInstallation
	}
	objects, err := s3store.New(s3store.Config{Endpoint: config.s3Endpoint, Bucket: config.bucket, AddressingStyle: s3store.PathStyle, AllowHTTPForLoopback: true, HTTPClient: client})
	if err != nil {
		return errInstallation
	}
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		return errInstallation
	}
	err = persistence.RequireCurrentSchema(ctx, database)
	closeErr := database.Close()
	if err != nil || closeErr != nil {
		return errInstallation
	}
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		return errInstallation
	}
	defer pool.Close()
	var profile, state string
	var capabilities []string
	if err := pool.QueryRow(ctx, `SELECT runtime_profile, required_capabilities, state FROM service_revisions WHERE isolation_domain_id=$1 AND id=$2`, config.domainID, config.revisionID).Scan(&profile, &capabilities, &state); err != nil || profile != developmentRuntimeProfile || !slices.Equal(capabilities, []string{developmentRuntimeProfile}) || (state != "draft" && state != "published") {
		return errInstallation
	}
	finalizer, err := execution.NewEnforcementBundleFinalizer(executionpostgres.New(pool), objects, objects)
	if err != nil {
		return errInstallation
	}
	record, err := install(ctx, config, request, compiler, finalizer)
	if err != nil {
		return err
	}
	// Emit only portable plan inputs, never compiled bytes or storage routing.
	receipt := struct {
		Contract              string `json:"contract"`
		IsolationDomainID     string `json:"isolationDomainId"`
		RevisionID            string `json:"revisionId"`
		BundleID              string `json:"bundleId"`
		Digest                string `json:"digest"`
		BindingDigest         string `json:"bindingDigest"`
		CertificationEligible bool   `json:"certificationEligible"`
	}{"dataground.dev.enforcement-installation/v1", record.IsolationDomainID, record.RevisionID, record.ID, record.Digest, record.Provenance.BindingDigest, false}
	if json.NewEncoder(output).Encode(receipt) != nil {
		return errors.New("installation receipt could not be written; retry the exact request")
	}
	return nil
}

type compilerPort interface {
	VerifyCompatibility(context.Context) (rosetta.Compatibility, error)
	Materialize(context.Context, rosetta.MaterializeRequest) (rosetta.Materialization, error)
}
type finalizerPort interface {
	Finalize(context.Context, execution.EnforcementBundleFinalization) (execution.EnforcementBundleRecord, error)
}

func install(ctx context.Context, config configuration, request rosetta.MaterializeRequest, compiler compilerPort, finalizer finalizerPort) (execution.EnforcementBundleRecord, error) {
	if request.Context != (rosetta.BindingContext{IsolationDomainID: config.domainID, ResourceType: "service-revision", ResourceID: config.revisionID}) {
		return execution.EnforcementBundleRecord{}, errInstallation
	}
	if _, err := compiler.VerifyCompatibility(ctx); err != nil {
		return execution.EnforcementBundleRecord{}, errInstallation
	}
	material, err := compiler.Materialize(ctx, request)
	defer clear(material.Content)
	if err != nil || material.Context != request.Context || material.Provenance.ArtifactDigest != developmentPolicyDigest || material.Provenance.InputDigest != developmentCompilerInputDigest || material.Provenance.CompilerVersion != rosetta.CompilerVersionV1 || material.Provenance.CatalogVersion != rosetta.CatalogVersion || material.Provenance.TargetContractVersion != rosetta.OpenShellTargetContractV1 || material.Provenance.Mode != rosetta.ModeStrict || execution.VerifyEnforcementPolicy(material.Content, developmentPolicyDigest) != nil || ctx.Err() != nil {
		return execution.EnforcementBundleRecord{}, errInstallation
	}
	provenance := material.Provenance
	binding, err := execution.NormalizeEnforcementBundleBinding(execution.EnforcementBundleBinding{
		ActorID: config.actorID, CorrelationID: config.correlationID,
		Record: execution.EnforcementBundleRecord{
			SchemaVersion:     execution.EnforcementBundleSchemaV1,
			IsolationDomainID: config.domainID, RevisionID: config.revisionID,
			ID: material.BundleID, Digest: provenance.ArtifactDigest,
			MediaType: material.MediaType, SizeBytes: int64(len(material.Content)),
			Provenance: execution.EnforcementBundleProvenance{
				Producer: "rosetta", SourceRevision: rosetta.CandidateSourceRevisionV1,
				CompilerVersion: provenance.CompilerVersion, CatalogVersion: provenance.CatalogVersion,
				TargetContractVersion: provenance.TargetContractVersion, Mode: provenance.Mode,
				InputDigest: provenance.InputDigest, BindingDigest: provenance.BindingDigest,
			},
		},
	})
	if err != nil {
		return execution.EnforcementBundleRecord{}, errInstallation
	}
	record, err := finalizer.Finalize(ctx, execution.EnforcementBundleFinalization{Binding: binding, Content: material.Content})
	if err != nil || record != binding.Record || ctx.Err() != nil {
		return execution.EnforcementBundleRecord{}, errInstallation
	}
	return record, nil
}

func parseConfiguration(args []string) (configuration, error) {
	var config configuration
	flags := flag.NewFlagSet("dataground-development-enforcement", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	for _, input := range []struct {
		name  string
		value *string
	}{
		{"repository-root", &config.root}, {"isolation-domain", &config.domainID},
		{"revision", &config.revisionID}, {"actor", &config.actorID},
		{"correlation-id", &config.correlationID}, {"rosetta-endpoint", &config.rosettaEndpoint},
		{"s3-endpoint", &config.s3Endpoint}, {"s3-bucket", &config.bucket},
	} {
		seen := false
		flags.Func(input.name, "exact reviewed development installation value", func(value string) error {
			if seen || value == "" {
				return errInstallation
			}
			seen = true
			*input.value = value
			return nil
		})
	}
	if flags.Parse(args) != nil || flags.NArg() != 0 || flags.NFlag() != 8 || !domainPattern.MatchString(config.domainID) || !revisionPattern.MatchString(config.revisionID) || !portableAttribution(config.actorID) || !portableAttribution(config.correlationID) || !filepath.IsAbs(config.root) || filepath.Clean(config.root) != config.root || !loopbackOrigin(config.rosettaEndpoint) || !loopbackOrigin(config.s3Endpoint) {
		return configuration{}, errors.New("complete unique development enforcement configuration is required")
	}
	return config, nil
}

func portableAttribution(value string) bool {
	if len(value) == 0 || len(value) > 256 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func loopbackOrigin(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return false
	}
	ip := net.ParseIP(parsed.Hostname())
	port, err := strconv.ParseUint(parsed.Port(), 10, 16)
	return ip != nil && ip.IsLoopback() && err == nil && port > 0
}

func readCheckedRequest(config configuration) (rosetta.MaterializeRequest, error) {
	path := filepath.Join(config.root, developmentInputPath)
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > 16<<10 {
		return rosetta.MaterializeRequest{}, errInstallation
	}
	file, err := os.Open(path)
	if err != nil {
		return rosetta.MaterializeRequest{}, errInstallation
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return rosetta.MaterializeRequest{}, errInstallation
	}
	content, err := io.ReadAll(io.LimitReader(file, (16<<10)+1))
	defer clear(content)
	digest := sha256.Sum256(content)
	if err != nil || hex.EncodeToString(digest[:]) != developmentInputSHA256 {
		return rosetta.MaterializeRequest{}, errInstallation
	}
	var input struct {
		Source  string          `json:"source"`
		Target  string          `json:"target"`
		Mode    string          `json:"mode"`
		Catalog rosetta.Catalog `json:"catalog"`
		Options struct {
			OpenShell rosetta.OpenShellOptions `json:"openShell"`
			Codex     struct{}                 `json:"codex"`
		} `json:"options"`
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.Target != rosetta.TargetOpenShell || input.Mode != rosetta.ModeStrict {
		return rosetta.MaterializeRequest{}, errInstallation
	}
	return rosetta.MaterializeRequest{CedarSource: input.Source, Catalog: input.Catalog, OpenShell: input.Options.OpenShell, Context: rosetta.BindingContext{IsolationDomainID: config.domainID, ResourceType: "service-revision", ResourceID: config.revisionID}}, nil
}
