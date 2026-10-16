//go:build !linux || (!amd64 && !arm64)

package processowner

import "context"

type Owner struct{}

func New() (*Owner, error)                                       { return nil, ErrUnavailable }
func (*Owner) Start(string, []string, []string) (Streams, error) { return Streams{}, ErrUnavailable }
func (*Owner) Stop(context.Context) error                        { return ErrUnavailable }
