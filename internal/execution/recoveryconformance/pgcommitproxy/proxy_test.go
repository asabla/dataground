package pgcommitproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestProxyDropsCommittedResponseBeforeClientObservation(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	serverDone := make(chan error, 1)
	go func() {
		connection, acceptErr := upstream.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer connection.Close()
		if err := discardStartup(connection); err != nil {
			serverDone <- err
			return
		}
		if _, err := connection.Write(message('R', []byte{0, 0, 0, 0})); err != nil {
			serverDone <- err
			return
		}
		if _, err := connection.Write(message('Z', []byte{'I'})); err != nil {
			serverDone <- err
			return
		}
		kind, payload, _, err := readTypedMessage(connection)
		if err != nil {
			serverDone <- err
			return
		}
		if kind != 'Q' || string(payload) != "commit\x00" {
			serverDone <- errors.New("proxy forwarded an unexpected frontend message")
			return
		}
		_, err = connection.Write(message('C', []byte("COMMIT\x00")))
		serverDone <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proxy, err := Start(ctx, upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	client, err := net.Dial("tcp", proxy.Address())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	startup := make([]byte, 8)
	binary.BigEndian.PutUint32(startup, uint32(len(startup)))
	binary.BigEndian.PutUint32(startup[4:], 196608)
	if _, err := client.Write(startup); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readTypedMessage(client); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readTypedMessage(client); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write(message('Q', []byte("commit\x00"))); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readTypedMessage(client); err == nil {
		t.Fatal("client observed the intercepted COMMIT response")
	}
	if err := proxy.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestProxyRejectsRemoteTarget(t *testing.T) {
	if _, err := Start(context.Background(), "192.0.2.1:5432"); err == nil {
		t.Fatal("remote PostgreSQL target accepted")
	}
}

func TestProxyWaitPreservesCancellation(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	proxy, err := Start(context.Background(), upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := proxy.Wait(ctx); err != context.Canceled {
		t.Fatalf("wait error = %v, want context.Canceled", err)
	}
}

func TestMessageReaderRejectsMalformedLengths(t *testing.T) {
	for _, length := range []uint32{3, maxMessageBytes + 1} {
		header := make([]byte, 5)
		header[0] = 'Q'
		binary.BigEndian.PutUint32(header[1:], length)
		if _, _, _, err := readTypedMessage(bytes.NewReader(header)); err == nil {
			t.Fatalf("message length %d accepted", length)
		}
	}
}

func TestWriteAllHandlesShortWrites(t *testing.T) {
	written := &shortWriter{limit: 2}
	if err := writeAll(written, []byte("commit")); err != nil {
		t.Fatal(err)
	}
	if got := written.Buffer.String(); got != "commit" {
		t.Fatalf("written content = %q", got)
	}
}

func TestCommitRequestRecognition(t *testing.T) {
	for _, test := range []struct {
		kind    byte
		payload []byte
		want    bool
	}{
		{kind: 'Q', payload: []byte("commit\x00"), want: true},
		{kind: 'Q', payload: []byte(" COMMIT; \x00"), want: true},
		{kind: 'P', payload: []byte("statement\x00commit\x00\x00"), want: true},
		{kind: 'Q', payload: []byte("rollback\x00")},
		{kind: 'Q', payload: []byte("commit work\x00")},
		{kind: 'Q', payload: []byte("select 'commit'\x00")},
		{kind: 'P', payload: []byte("broken")},
	} {
		if got := isCommitRequest(test.kind, test.payload); got != test.want {
			t.Fatalf("isCommitRequest(%q, %q) = %t, want %t", test.kind, test.payload, got, test.want)
		}
	}
}

func discardStartup(source io.Reader) error {
	header := make([]byte, 4)
	if _, err := io.ReadFull(source, header); err != nil {
		return err
	}
	length := int(binary.BigEndian.Uint32(header))
	_, err := io.CopyN(io.Discard, source, int64(length-4))
	return err
}

func message(kind byte, payload []byte) []byte {
	encoded := make([]byte, 5+len(payload))
	encoded[0] = kind
	binary.BigEndian.PutUint32(encoded[1:], uint32(len(payload)+4))
	copy(encoded[5:], payload)
	return encoded
}

type shortWriter struct {
	bytes.Buffer
	limit int
}

func (writer *shortWriter) Write(content []byte) (int, error) {
	if len(content) > writer.limit {
		content = content[:writer.limit]
	}
	return writer.Buffer.Write(content)
}
