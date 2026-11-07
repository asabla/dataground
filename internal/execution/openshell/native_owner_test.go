package openshell

import (
	"context"
	"io"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestOwnedRuntimeCancelledHandshakeClosesPrivateTransport(t *testing.T) {
	started := make(chan struct{})
	proxy := newSSHCommandTestProxy(t, "/usr/local/bin/dataground-native-owner", func(channel ssh.Channel) {
		close(started)
		_, _ = io.Copy(io.Discard, channel)
	})
	defer proxy.Close()
	provider, ref, _ := sourceTransferProvider(t, proxy)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		accepted bool
		err      error
	}
	result := make(chan outcome, 1)
	go func() {
		session, err := provider.StartOwnedRuntime(ctx, ref)
		if session != nil {
			_ = session.Close()
		}
		result <- outcome{accepted: session != nil, err: err}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("owner command did not start")
	}
	cancel()
	select {
	case value := <-result:
		if value.err == nil || value.accepted {
			t.Fatal("cancelled handshake succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("owner startup ignored cancellation")
	}
	select {
	case <-proxy.serverDone:
	case <-time.After(time.Second):
		t.Fatal("cancelled startup retained its private connection")
	}
}
