package s3artifactconformance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/execution/s3store"
)

const maximumArtifactBytes int64 = 1024 * 1024

type Config struct {
	RunID string
}

func Run(ctx context.Context, store *s3store.Store, config Config) error {
	if store == nil || len(config.RunID) != 32 {
		return errors.New("invalid invocation-artifact conformance configuration")
	}
	objects, err := s3store.NewArtifactStore(store, maximumArtifactBytes)
	if err != nil {
		return errors.New("invalid invocation-artifact conformance store")
	}

	domainID := scopedID("iso", config.RunID+":domain")
	invocationID := scopedID("inv", config.RunID+":invocation")
	missing := conformanceObject(domainID, invocationID, scopedID("art", config.RunID+":missing"), []byte("missing"))
	if _, err := objects.OpenInvocationArtifactObject(ctx, missing.key); !errors.Is(
		err,
		artifact.ErrInvocationArtifactObjectMissing,
	) {
		return errors.New("invocation-artifact missing-read conformance failed")
	}

	exact := conformanceObject(
		domainID,
		invocationID,
		scopedID("art", config.RunID+":exact"),
		[]byte("dataground invocation artifact conformance"),
	)
	if err := put(ctx, objects, exact); err != nil {
		return errors.New("invocation-artifact create conformance failed")
	}
	if err := requireObject(ctx, objects, exact); err != nil {
		return errors.New("invocation-artifact exact-read conformance failed")
	}
	if err := put(ctx, objects, exact); !errors.Is(err, artifact.ErrInvocationArtifactObjectConflict) {
		return errors.New("invocation-artifact immutable-replay conformance failed")
	}

	oversized := conformanceObject(
		domainID,
		invocationID,
		scopedID("art", config.RunID+":oversized"),
		bytes.Repeat([]byte{1}, int(maximumArtifactBytes)+1),
	)
	if err := put(ctx, objects, oversized); !errors.Is(
		err,
		artifact.ErrInvocationArtifactObjectConflict,
	) {
		return errors.New("invocation-artifact size-bound conformance failed")
	}

	return requireConcurrentWinner(
		ctx,
		objects,
		domainID,
		invocationID,
		scopedID("art", config.RunID+":concurrent"),
	)
}

type object struct {
	key       string
	content   []byte
	digest    string
	mediaType string
}

func conformanceObject(domainID, invocationID, artifactID string, content []byte) object {
	digest := sha256.Sum256(content)
	digestHex := hex.EncodeToString(digest[:])
	return object{
		key: fmt.Sprintf(
			"invocation-artifacts/v1/%s/%s/%s/%s",
			domainID,
			invocationID,
			artifactID,
			digestHex,
		),
		content:   bytes.Clone(content),
		digest:    "sha256:" + digestHex,
		mediaType: "application/octet-stream",
	}
}

func put(ctx context.Context, store *s3store.ArtifactStore, value object) error {
	return store.PutInvocationArtifactObjectIfAbsent(
		ctx,
		value.key,
		bytes.NewReader(value.content),
		int64(len(value.content)),
		value.digest,
		value.mediaType,
	)
}

func requireObject(ctx context.Context, store *s3store.ArtifactStore, value object) error {
	reader, err := store.OpenInvocationArtifactObject(ctx, value.key)
	if err != nil {
		return err
	}
	content, readErr := io.ReadAll(io.LimitReader(reader, maximumArtifactBytes+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(content, value.content) {
		return artifact.ErrInvocationArtifactObjectConflict
	}
	return nil
}

func requireConcurrentWinner(
	ctx context.Context,
	store *s3store.ArtifactStore,
	domainID string,
	invocationID string,
	artifactID string,
) error {
	left := conformanceObject(domainID, invocationID, artifactID, []byte("left"))
	right := conformanceObject(domainID, invocationID, artifactID, []byte("right"))
	right.key = left.key

	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, value := range []object{left, right} {
		group.Add(1)
		go func(candidate object) {
			defer group.Done()
			<-start
			results <- put(ctx, store, candidate)
		}(value)
	}
	close(start)
	group.Wait()
	close(results)

	var succeeded, conflicted int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, artifact.ErrInvocationArtifactObjectConflict):
			conflicted++
		default:
			return err
		}
	}
	if succeeded != 1 || conflicted != 1 {
		return artifact.ErrInvocationArtifactObjectConflict
	}

	reader, err := store.OpenInvocationArtifactObject(ctx, left.key)
	if err != nil {
		return err
	}
	content, readErr := io.ReadAll(io.LimitReader(reader, maximumArtifactBytes+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil ||
		(!bytes.Equal(content, left.content) && !bytes.Equal(content, right.content)) {
		return artifact.ErrInvocationArtifactObjectConflict
	}
	return nil
}

func scopedID(prefix, seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return prefix + "_" + hex.EncodeToString(digest[:12])
}
