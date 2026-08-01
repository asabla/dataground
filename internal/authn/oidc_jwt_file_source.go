package authn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maximumOIDCJWTKeysetPublicationBytes = maximumOIDCJWKSBytes + (4 << 10)

// OIDCJWTKeysetFileSource reads complete, atomically replaced deployment
// publications. The file contains public keys, but must not be writable by
// group or other users because its integrity is an authentication boundary.
type OIDCJWTKeysetFileSource struct {
	path string
}

type oidcJWTKeysetPublication struct {
	Sequence  uint64          `json:"sequence"`
	ExpiresAt time.Time       `json:"expiresAt"`
	JWKS      json.RawMessage `json:"jwks"`
}

func NewOIDCJWTKeysetFileSource(path string) (*OIDCJWTKeysetFileSource, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("OIDC JWT keyset publication path must be absolute and canonical")
	}
	return &OIDCJWTKeysetFileSource{path: path}, nil
}

func (source *OIDCJWTKeysetFileSource) Load(ctx context.Context) (OIDCJWTKeysetSnapshot, error) {
	if source == nil || ctx == nil || source.path == "" {
		return OIDCJWTKeysetSnapshot{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return OIDCJWTKeysetSnapshot{}, err
	}
	file, err := os.Open(source.path)
	if err != nil {
		return OIDCJWTKeysetSnapshot{}, ErrUnavailable
	}
	defer file.Close()

	before, err := file.Stat()
	if err != nil {
		return OIDCJWTKeysetSnapshot{}, ErrUnavailable
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o022 != 0 ||
		before.Size() <= 0 || before.Size() > maximumOIDCJWTKeysetPublicationBytes {
		return OIDCJWTKeysetSnapshot{}, ErrOIDCJWTKeysetInvalid
	}
	content, err := readBoundedOIDCJWTKeysetPublication(ctx, file)
	if err != nil {
		return OIDCJWTKeysetSnapshot{}, err
	}
	defer clear(content)
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() {
		return OIDCJWTKeysetSnapshot{}, ErrUnavailable
	}

	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var publication oidcJWTKeysetPublication
	if err := decoder.Decode(&publication); err != nil {
		return OIDCJWTKeysetSnapshot{}, ErrOIDCJWTKeysetInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return OIDCJWTKeysetSnapshot{}, ErrOIDCJWTKeysetInvalid
	}
	if publication.Sequence == 0 || publication.ExpiresAt.IsZero() ||
		len(publication.JWKS) == 0 || len(publication.JWKS) > maximumOIDCJWKSBytes {
		return OIDCJWTKeysetSnapshot{}, ErrOIDCJWTKeysetInvalid
	}
	if err := ctx.Err(); err != nil {
		return OIDCJWTKeysetSnapshot{}, err
	}
	return OIDCJWTKeysetSnapshot{
		Sequence:  publication.Sequence,
		ExpiresAt: publication.ExpiresAt.UTC(),
		JWKS:      append([]byte(nil), publication.JWKS...),
	}, nil
}

func readBoundedOIDCJWTKeysetPublication(ctx context.Context, reader io.Reader) ([]byte, error) {
	content := make([]byte, 0, maximumOIDCJWTKeysetPublicationBytes)
	buffer := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			clear(content)
			return nil, err
		}
		count, err := reader.Read(buffer)
		if count > 0 {
			if len(content)+count > maximumOIDCJWTKeysetPublicationBytes {
				clear(content)
				return nil, ErrOIDCJWTKeysetInvalid
			}
			content = append(content, buffer[:count]...)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			clear(content)
			return nil, ErrUnavailable
		}
		if count == 0 {
			clear(content)
			return nil, ErrUnavailable
		}
	}
	if len(content) == 0 {
		return nil, ErrOIDCJWTKeysetInvalid
	}
	return content, nil
}

func (*OIDCJWTKeysetFileSource) MarshalJSON() ([]byte, error) {
	return nil, errors.New("OIDC JWT keyset file sources cannot be serialized")
}

var _ OIDCJWTKeysetSource = (*OIDCJWTKeysetFileSource)(nil)
var _ json.Marshaler = (*OIDCJWTKeysetFileSource)(nil)
