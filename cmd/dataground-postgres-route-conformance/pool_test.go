package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type scriptedPoolResult struct {
	snapshot poolSnapshot
	err      error
}

type scriptedPoolProbe struct {
	results       []scriptedPoolResult
	requireWrites []bool
	closed        bool
}

func (probe *scriptedPoolProbe) Snapshot(_ context.Context, requireWrite bool) (poolSnapshot, error) {
	probe.requireWrites = append(probe.requireWrites, requireWrite)
	if len(probe.results) == 0 {
		return poolSnapshot{}, errors.New("unexpected pool probe")
	}
	result := probe.results[0]
	probe.results = probe.results[1:]
	return result.snapshot, result.err
}

func (probe *scriptedPoolProbe) Close() {
	probe.closed = true
}

func TestRunPoolStateMachineObservesFailureAndPromotedPool(t *testing.T) {
	primaryStart := time.Unix(100, 0)
	promotedStart := time.Unix(200, 0)
	probe := &scriptedPoolProbe{results: []scriptedPoolResult{
		{snapshot: poolSnapshot{postmasterStarted: primaryStart}},
		{err: errors.New("connection closed")},
		{snapshot: poolSnapshot{postmasterStarted: promotedStart}},
	}}
	var output bytes.Buffer
	err := runPoolStateMachine(context.Background(), &output, probe, poolStateMachineConfig{
		phaseTimeout:  time.Second,
		retryInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		poolPrimaryReadyState,
		poolFailureObservedState,
		poolPromotedReadyState,
		"",
	}, "\n")
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if got := probe.requireWrites; len(got) != 3 || got[0] || got[1] || !got[2] {
		t.Fatalf("write requirements = %v", got)
	}
}

func TestRunPoolStateMachineRejectsOriginalPrimaryAfterFailure(t *testing.T) {
	primary := poolSnapshot{postmasterStarted: time.Unix(100, 0)}
	probe := &scriptedPoolProbe{results: []scriptedPoolResult{
		{snapshot: primary},
		{err: errors.New("connection closed")},
		{snapshot: primary},
	}}
	var output bytes.Buffer
	err := runPoolStateMachine(context.Background(), &output, probe, poolStateMachineConfig{
		phaseTimeout:  time.Second,
		retryInterval: time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "original primary") {
		t.Fatalf("error = %v", err)
	}
	want := poolPrimaryReadyState + "\n" + poolFailureObservedState + "\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestRunPoolConformanceRejectsNonLoopbackURLWithoutLeakingIt(t *testing.T) {
	const databaseURL = "postgres://user:secret@192.0.2.1:5432/database?sslmode=disable"
	var output bytes.Buffer
	err := runPoolConformance(context.Background(), databaseURL, &output)
	if err == nil {
		t.Fatal("pool conformance accepted a non-loopback URL")
	}
	if strings.Contains(err.Error(), "secret") || output.Len() != 0 {
		t.Fatalf("unsafe output: error=%q output=%q", err, output.String())
	}
}
