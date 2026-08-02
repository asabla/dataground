package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

const (
	authenticationRateLimitPolicyContract = "dataground.authentication-rate-limit-policy/v1"
	maximumActivationRequestBytes         = 64 << 10
	maximumActivationRequestDepth         = 16
	maximumActivationReasonBytes          = 512
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

type activationRequest struct {
	Contract        string        `json:"contract"`
	Generation      uint64        `json:"generation"`
	Window          durationValue `json:"window"`
	GlobalBurst     uint32        `json:"globalBurst"`
	DomainBurst     uint32        `json:"isolationDomainBurst"`
	CredentialBurst uint32        `json:"credentialBurst"`
	ActorID         string        `json:"actorId"`
	Reason          string        `json:"reason"`
	CorrelationID   string        `json:"correlationId"`
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	if ctx == nil {
		return errors.New("authentication rate limit policy activation context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	flags := flag.NewFlagSet("dataground-auth-rate-limit-policy", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var requestFile string
	flags.StringVar(&requestFile, "request-file", "", "owner-only policy activation request")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || requestFile == "" {
		return errors.New("exactly one request-file is required")
	}
	request, err := readActivationRequest(ctx, requestFile)
	if err != nil {
		return err
	}
	reason := []byte(request.Reason)
	request.Reason = ""
	defer clear(reason)
	reasonDigest := sha256.Sum256(reason)
	activation := persistence.AuthenticationRateLimitPolicyActivation{
		Contract:   request.Contract,
		Generation: request.Generation,
		Policy: persistence.AuthenticationRateLimitPolicy{
			Window:               request.Window.value,
			GlobalBurst:          request.GlobalBurst,
			IsolationDomainBurst: request.DomainBurst,
			CredentialBurst:      request.CredentialBurst,
		},
		ActivatedBy:   request.ActorID,
		CorrelationID: request.CorrelationID,
		ReasonDigest:  append([]byte(nil), reasonDigest[:]...),
	}
	if !activation.Valid() {
		return errors.New("authentication rate limit policy activation request is invalid")
	}
	databaseURL := os.Getenv("DATAGROUND_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATAGROUND_DATABASE_URL is required")
	}
	operationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
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
	pool, err := persistence.OpenPool(operationCtx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	return persistence.NewRepository(pool).ActivateAuthenticationRateLimitPolicy(operationCtx, activation)
}

func readActivationRequest(ctx context.Context, path string) (activationRequest, error) {
	var request activationRequest
	if err := ctx.Err(); err != nil {
		return request, err
	}
	encoded, err := readStableActivationRequest(path)
	if err != nil {
		return request, err
	}
	defer clear(encoded)
	if err := ctx.Err(); err != nil {
		return request, err
	}
	if err := requireUniqueActivationJSON(encoded); err != nil {
		return request, errors.New("authentication rate limit policy activation request is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, errors.New("authentication rate limit policy activation request is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return request, errors.New("authentication rate limit policy activation request is invalid")
	}
	if request.Contract != authenticationRateLimitPolicyContract ||
		request.Generation == 0 || !request.Window.set ||
		request.Reason == "" || len(request.Reason) > maximumActivationReasonBytes {
		return request, errors.New("authentication rate limit policy activation request is invalid")
	}
	return request, nil
}

func readStableActivationRequest(path string) ([]byte, error) {
	if !canonicalAbsolutePath(path) {
		return nil, errors.New("authentication rate limit policy activation request path is invalid")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !safeActivationRequestFile(pathInfo) {
		return nil, errors.New("authentication rate limit policy activation request file is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("authentication rate limit policy activation request file is unavailable")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !sameActivationRequestFile(pathInfo, before) {
		return nil, errors.New("authentication rate limit policy activation request changed before reading")
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumActivationRequestBytes+1))
	if err != nil || len(content) == 0 || len(content) > maximumActivationRequestBytes ||
		int64(len(content)) != before.Size() {
		clear(content)
		return nil, errors.New("authentication rate limit policy activation request content is invalid")
	}
	after, err := file.Stat()
	if err != nil || !sameActivationRequestFile(before, after) {
		clear(content)
		return nil, errors.New("authentication rate limit policy activation request changed while reading")
	}
	pathAfter, err := os.Lstat(path)
	if err != nil || !sameActivationRequestFile(after, pathAfter) {
		clear(content)
		return nil, errors.New("authentication rate limit policy activation request path changed while reading")
	}
	return content, nil
}

func safeActivationRequestFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0o077 == 0 &&
		info.Size() > 0 && info.Size() <= maximumActivationRequestBytes
}

func sameActivationRequestFile(expected os.FileInfo, actual os.FileInfo) bool {
	return expected != nil && actual != nil && os.SameFile(expected, actual) &&
		expected.Mode() == actual.Mode() && expected.Size() == actual.Size() &&
		expected.ModTime().Equal(actual.ModTime())
}

func canonicalAbsolutePath(path string) bool {
	return path != "" && strings.IndexByte(path, 0) < 0 && filepath.IsAbs(path) &&
		filepath.Clean(path) == path
}

func requireUniqueActivationJSON(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := validateActivationJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("activation JSON has trailing data")
	}
	return nil
}

func validateActivationJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maximumActivationRequestDepth {
		return errors.New("activation JSON is too deeply nested")
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
				return errors.New("activation JSON object key is invalid")
			}
			if _, exists := seen[key]; exists {
				return errors.New("activation JSON contains a duplicate member")
			}
			seen[key] = struct{}{}
			if err := validateActivationJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := validateActivationJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("activation JSON delimiter is invalid")
	}
	closing, err := decoder.Token()
	if err != nil || closing != matchingActivationJSONDelimiter(delimiter) {
		return errors.New("activation JSON delimiter is unbalanced")
	}
	return nil
}

func matchingActivationJSONDelimiter(open json.Delim) json.Delim {
	if open == '{' {
		return '}'
	}
	return ']'
}
