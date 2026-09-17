//go:build !unix

package runtimeevidence

import "os"

func lockDevelopmentGateway(string, string) (*os.File, error) { return nil, ErrDevelopmentGateway }
func closeDevelopmentGatewayLock(*os.File)                    {}
