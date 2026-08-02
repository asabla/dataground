package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

const (
	authenticationRateLimitCapacityContract = "dataground.authentication-rate-limit-capacity/v1"
	maximumCapacityRequestBytes             = 64 << 10
	maximumCapacityRequestDepth             = 16
	maximumCapacityRunDuration              = 30 * time.Minute
)

type durationValue struct {
	value time.Duration
	set   bool
}

func (value *durationValue) UnmarshalJSON(encoded []byte) error {
	var text string
	if err := json.Unmarshal(encoded, &text); err != nil || text == "" {
		return errors.New("duration must be a non-empty string")
	}
	duration, err := time.ParseDuration(text)
	if err != nil {
		return errors.New("duration is invalid")
	}
	value.value = duration
	value.set = true
	return nil
}

type capacityRequest struct {
	Contract                   string        `json:"contract"`
	RunID                      string        `json:"runId"`
	SourceRevision             string        `json:"sourceRevision"`
	DeploymentProfile          string        `json:"deploymentProfile"`
	DatabaseName               string        `json:"databaseName"`
	Window                     durationValue `json:"window"`
	GlobalBurst                uint32        `json:"globalBurst"`
	IsolationDomainBurst       uint32        `json:"isolationDomainBurst"`
	CredentialBurst            uint32        `json:"credentialBurst"`
	AttemptsPerPhase           uint32        `json:"attemptsPerPhase"`
	Workers                    uint32        `json:"workers"`
	MaximumP99Latency          durationValue `json:"maximumP99Latency"`
	MinimumThroughputPerSecond uint32        `json:"minimumThroughputPerSecond"`
	MaximumRunDuration         durationValue `json:"maximumRunDuration"`
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	if ctx == nil {
		return errors.New("authentication rate limit capacity context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	flags := flag.NewFlagSet("dataground-auth-rate-limit-capacity", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var requestFile, outputFile string
	flags.StringVar(&requestFile, "request-file", "", "owner-only capacity request")
	flags.StringVar(&outputFile, "output-file", "", "new capacity evidence file")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || requestFile == "" || outputFile == "" {
		return errors.New("exactly one request-file and output-file are required")
	}
	request, err := readCapacityRequest(ctx, requestFile)
	if err != nil {
		return err
	}
	config := persistence.AuthenticationRateLimitCapacityConfig{
		Contract:          request.Contract,
		RunID:             request.RunID,
		SourceRevision:    request.SourceRevision,
		DeploymentProfile: request.DeploymentProfile,
		DatabaseName:      request.DatabaseName,
		Policy: persistence.AuthenticationRateLimitPolicy{
			Window:               request.Window.value,
			GlobalBurst:          request.GlobalBurst,
			IsolationDomainBurst: request.IsolationDomainBurst,
			CredentialBurst:      request.CredentialBurst,
		},
		AttemptsPerPhase:  request.AttemptsPerPhase,
		Workers:           request.Workers,
		MaximumP99Latency: request.MaximumP99Latency.value,
		MinimumThroughput: request.MinimumThroughputPerSecond,
	}
	if !config.Valid() || !request.MaximumRunDuration.set ||
		request.MaximumRunDuration.value <= 0 || request.MaximumRunDuration.value > maximumCapacityRunDuration {
		return errors.New("authentication rate limit capacity request is invalid")
	}
	if err := requireCapacitySourceRevision(request.SourceRevision); err != nil {
		return err
	}
	if err := validateCapacityOutputPath(outputFile); err != nil {
		return err
	}
	databaseURL := os.Getenv("DATAGROUND_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATAGROUND_DATABASE_URL is required")
	}
	operationCtx, cancel := context.WithTimeout(ctx, request.MaximumRunDuration.value)
	defer cancel()
	database, err := persistence.OpenSQL(operationCtx, databaseURL)
	if err != nil {
		return err
	}
	if err := persistence.RequireCurrentSchema(operationCtx, database); err != nil {
		database.Close()
		return err
	}
	if err := database.Close(); err != nil {
		return err
	}
	pool, err := persistence.OpenAuthenticationRateLimitCapacityPool(operationCtx, databaseURL, request.Workers)
	if err != nil {
		return err
	}
	defer pool.Close()
	evidence, err := persistence.NewRepository(pool).MeasureAuthenticationRateLimitCapacity(operationCtx, config)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return errors.New("encode authentication rate limit capacity evidence")
	}
	encoded = append(encoded, '\n')
	defer clear(encoded)
	if err := writeCapacityEvidence(outputFile, encoded); err != nil {
		return err
	}
	if !evidence.Accepted {
		return errors.New("authentication rate limit capacity thresholds were not met")
	}
	return nil
}

func requireCapacitySourceRevision(expected string) error {
	build, ok := debug.ReadBuildInfo()
	if !ok || !capacitySourceRevisionMatches(expected, build.Settings) {
		return errors.New("authentication rate limit capacity source revision does not match a clean build")
	}
	return nil
}

func capacitySourceRevisionMatches(expected string, settings []debug.BuildSetting) bool {
	var revision, modified string
	for _, setting := range settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value
		}
	}
	return revision == expected && modified == "false"
}

func readCapacityRequest(ctx context.Context, path string) (capacityRequest, error) {
	var request capacityRequest
	if err := ctx.Err(); err != nil {
		return request, err
	}
	encoded, err := readStableCapacityFile(path, maximumCapacityRequestBytes)
	if err != nil {
		return request, err
	}
	defer clear(encoded)
	if err := ctx.Err(); err != nil {
		return request, err
	}
	if err := requireUniqueCapacityJSON(encoded); err != nil {
		return request, errors.New("authentication rate limit capacity request is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, errors.New("authentication rate limit capacity request is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return request, errors.New("authentication rate limit capacity request is invalid")
	}
	if request.Contract != authenticationRateLimitCapacityContract ||
		!request.Window.set || !request.MaximumP99Latency.set || !request.MaximumRunDuration.set {
		return request, errors.New("authentication rate limit capacity request is invalid")
	}
	return request, nil
}

func readStableCapacityFile(path string, maximumBytes int64) ([]byte, error) {
	if !canonicalAbsolutePath(path) || maximumBytes <= 0 {
		return nil, errors.New("authentication rate limit capacity file path is invalid")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !safeCapacityFile(pathInfo, maximumBytes) {
		return nil, errors.New("authentication rate limit capacity file is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("authentication rate limit capacity file is unavailable")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !sameCapacityFile(pathInfo, before) {
		return nil, errors.New("authentication rate limit capacity file changed before reading")
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	if err != nil || len(content) == 0 || int64(len(content)) > maximumBytes || int64(len(content)) != before.Size() {
		clear(content)
		return nil, errors.New("authentication rate limit capacity file content is invalid")
	}
	after, err := file.Stat()
	if err != nil || !sameCapacityFile(before, after) {
		clear(content)
		return nil, errors.New("authentication rate limit capacity file changed while reading")
	}
	pathAfter, err := os.Lstat(path)
	if err != nil || !sameCapacityFile(after, pathAfter) {
		clear(content)
		return nil, errors.New("authentication rate limit capacity file path changed while reading")
	}
	return content, nil
}

func safeCapacityFile(info os.FileInfo, maximumBytes int64) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0o077 == 0 &&
		info.Size() > 0 && info.Size() <= maximumBytes
}

func sameCapacityFile(expected os.FileInfo, actual os.FileInfo) bool {
	return expected != nil && actual != nil && os.SameFile(expected, actual) &&
		expected.Mode() == actual.Mode() && expected.Size() == actual.Size() &&
		expected.ModTime().Equal(actual.ModTime())
}

func validateCapacityOutputPath(path string) error {
	if !canonicalAbsolutePath(path) {
		return errors.New("authentication rate limit capacity output path is invalid")
	}
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return errors.New("authentication rate limit capacity output directory is invalid")
	}
	if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("authentication rate limit capacity output already exists or is unavailable")
	}
	return nil
}

func writeCapacityEvidence(path string, content []byte) error {
	if len(content) == 0 {
		return errors.New("authentication rate limit capacity evidence is empty")
	}
	if err := validateCapacityOutputPath(path); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".dataground-auth-capacity-*")
	if err != nil {
		return errors.New("create authentication rate limit capacity evidence")
	}
	temporaryPath := temporary.Name()
	installed := false
	defer func() {
		if !installed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return errors.New("set authentication rate limit capacity evidence mode")
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return errors.New("write authentication rate limit capacity evidence")
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return errors.New("sync authentication rate limit capacity evidence")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("close authentication rate limit capacity evidence")
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return errors.New("install authentication rate limit capacity evidence")
	}
	installed = true
	if err := syncCapacityDirectory(directory); err != nil {
		return err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return errors.New("remove authentication rate limit capacity temporary file")
	}
	return syncCapacityDirectory(directory)
}

func syncCapacityDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return errors.New("open authentication rate limit capacity output directory")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errors.New("sync authentication rate limit capacity output directory")
	}
	return nil
}

func canonicalAbsolutePath(path string) bool {
	return path != "" && strings.IndexByte(path, 0) < 0 && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func requireUniqueCapacityJSON(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := validateCapacityJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("capacity JSON has trailing data")
	}
	return nil
}

func validateCapacityJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maximumCapacityRequestDepth {
		return errors.New("capacity JSON is too deeply nested")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("capacity JSON object key is invalid")
			}
			if _, exists := seen[key]; exists {
				return errors.New("capacity JSON contains a duplicate member")
			}
			seen[key] = struct{}{}
			if err := validateCapacityJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := validateCapacityJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("capacity JSON delimiter is invalid")
	}
	closing, err := decoder.Token()
	if err != nil || closing != matchingCapacityJSONDelimiter(delimiter) {
		return errors.New("capacity JSON delimiter is unbalanced")
	}
	return nil
}

func matchingCapacityJSONDelimiter(open json.Delim) json.Delim {
	if open == '{' {
		return '}'
	}
	return ']'
}
