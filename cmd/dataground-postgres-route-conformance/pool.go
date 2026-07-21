package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	poolConformanceSize       = int32(3)
	poolConnectTimeout        = 2 * time.Second
	poolOperationTimeout      = 2 * time.Second
	poolPhaseTimeout          = 45 * time.Second
	poolRetryInterval         = 200 * time.Millisecond
	poolPrimaryReadyState     = "pool-primary-ready"
	poolFailureObservedState  = "pool-failure-observed"
	poolPromotedReadyState    = "pool-promoted-ready"
	poolConformanceTableQuery = `CREATE TEMPORARY TABLE dataground_pool_write_probe (
		value integer NOT NULL
	) ON COMMIT DROP`
)

type poolSnapshot struct {
	postmasterStarted time.Time
}

type poolProbe interface {
	Snapshot(context.Context, bool) (poolSnapshot, error)
	Close()
}

type postgresPoolProbe struct {
	pool *pgxpool.Pool
}

type poolStateMachineConfig struct {
	phaseTimeout  time.Duration
	retryInterval time.Duration
}

func runPoolConformance(ctx context.Context, databaseURL string, output io.Writer) error {
	if err := validateLoopbackDatabaseURL(databaseURL); err != nil {
		return err
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return errors.New("parse PostgreSQL pool conformance database URL")
	}
	config.ConnConfig.ConnectTimeout = poolConnectTimeout
	config.ConnConfig.RuntimeParams["application_name"] = "dataground-pool-reconnection-conformance"
	config.MinConns = poolConformanceSize
	config.MaxConns = poolConformanceSize
	config.HealthCheckPeriod = poolRetryInterval
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return errors.New("create PostgreSQL pool conformance client")
	}
	probe := &postgresPoolProbe{pool: pool}
	defer probe.Close()
	return runPoolStateMachine(ctx, output, probe, poolStateMachineConfig{
		phaseTimeout:  poolPhaseTimeout,
		retryInterval: poolRetryInterval,
	})
}

func runPoolStateMachine(
	ctx context.Context,
	output io.Writer,
	probe poolProbe,
	config poolStateMachineConfig,
) error {
	if config.phaseTimeout <= 0 || config.retryInterval <= 0 {
		return errors.New("invalid PostgreSQL pool conformance timing")
	}
	initialContext, cancelInitial := context.WithTimeout(ctx, config.phaseTimeout)
	initial, err := probe.Snapshot(initialContext, false)
	cancelInitial()
	if err != nil {
		return errors.New("establish initial PostgreSQL pool conformance sessions")
	}
	if _, err := fmt.Fprintln(output, poolPrimaryReadyState); err != nil {
		return errors.New("write PostgreSQL pool conformance state")
	}

	failureContext, cancelFailure := context.WithTimeout(ctx, config.phaseTimeout)
	err = waitForPoolFailure(failureContext, probe, config.retryInterval)
	cancelFailure()
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, poolFailureObservedState); err != nil {
		return errors.New("write PostgreSQL pool conformance state")
	}

	promotedContext, cancelPromoted := context.WithTimeout(ctx, config.phaseTimeout)
	err = waitForPromotedPool(promotedContext, probe, initial, config.retryInterval)
	cancelPromoted()
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, poolPromotedReadyState); err != nil {
		return errors.New("write PostgreSQL pool conformance state")
	}
	return nil
}

func waitForPoolFailure(ctx context.Context, probe poolProbe, retryInterval time.Duration) error {
	ticker := time.NewTicker(retryInterval)
	defer ticker.Stop()
	for {
		operationContext, cancel := context.WithTimeout(ctx, poolOperationTimeout)
		_, err := probe.Snapshot(operationContext, false)
		cancel()
		if err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("PostgreSQL pool did not observe an unavailable operation")
		case <-ticker.C:
		}
	}
}

func waitForPromotedPool(
	ctx context.Context,
	probe poolProbe,
	initial poolSnapshot,
	retryInterval time.Duration,
) error {
	ticker := time.NewTicker(retryInterval)
	defer ticker.Stop()
	for {
		operationContext, cancel := context.WithTimeout(ctx, poolOperationTimeout)
		current, err := probe.Snapshot(operationContext, true)
		cancel()
		if err == nil {
			if current.postmasterStarted.Equal(initial.postmasterStarted) {
				return errors.New("PostgreSQL pool reconnected to the original primary")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("PostgreSQL pool did not reconnect to the promoted primary")
		case <-ticker.C:
		}
	}
}

func (probe *postgresPoolProbe) Snapshot(ctx context.Context, requireWrite bool) (poolSnapshot, error) {
	connections := make([]*pgxpool.Conn, 0, poolConformanceSize)
	defer func() {
		for _, connection := range connections {
			connection.Release()
		}
	}()
	for range poolConformanceSize {
		connection, err := probe.pool.Acquire(ctx)
		if err != nil {
			return poolSnapshot{}, errors.New("acquire PostgreSQL pool conformance session")
		}
		connections = append(connections, connection)
	}

	var snapshot poolSnapshot
	backendProcesses := make(map[int32]struct{}, poolConformanceSize)
	for index, connection := range connections {
		var backendProcess int32
		var postmasterStarted time.Time
		var inRecovery bool
		var readOnly bool
		if err := connection.QueryRow(
			ctx,
			`SELECT pg_backend_pid(), pg_postmaster_start_time(), pg_is_in_recovery(),
				current_setting('transaction_read_only') = 'on'`,
		).Scan(&backendProcess, &postmasterStarted, &inRecovery, &readOnly); err != nil {
			return poolSnapshot{}, errors.New("query PostgreSQL pool conformance session")
		}
		if inRecovery || readOnly {
			return poolSnapshot{}, errors.New("PostgreSQL pool session is not a writable primary")
		}
		if _, duplicate := backendProcesses[backendProcess]; duplicate {
			return poolSnapshot{}, errors.New("PostgreSQL pool reused a concurrent backend session")
		}
		backendProcesses[backendProcess] = struct{}{}
		if index == 0 {
			snapshot.postmasterStarted = postmasterStarted
		} else if !postmasterStarted.Equal(snapshot.postmasterStarted) {
			return poolSnapshot{}, errors.New("PostgreSQL pool spans multiple postmasters")
		}
		if requireWrite {
			if err := verifyPoolWrite(ctx, connection); err != nil {
				return poolSnapshot{}, err
			}
		}
	}
	return snapshot, nil
}

func (probe *postgresPoolProbe) Close() {
	probe.pool.Close()
}

func verifyPoolWrite(ctx context.Context, connection *pgxpool.Conn) error {
	transaction, err := connection.Begin(ctx)
	if err != nil {
		return errors.New("begin PostgreSQL pool conformance write")
	}
	defer func() {
		rollbackContext, cancel := context.WithTimeout(context.Background(), poolOperationTimeout)
		defer cancel()
		_ = transaction.Rollback(rollbackContext)
	}()
	if _, err := transaction.Exec(ctx, poolConformanceTableQuery); err != nil {
		return errors.New("create PostgreSQL pool conformance temporary table")
	}
	if _, err := transaction.Exec(
		ctx,
		"INSERT INTO dataground_pool_write_probe (value) VALUES (1)",
	); err != nil {
		return errors.New("write PostgreSQL pool conformance temporary table")
	}
	var value int
	if err := transaction.QueryRow(
		ctx,
		"SELECT value FROM dataground_pool_write_probe",
	).Scan(&value); err != nil || value != 1 {
		return errors.New("read PostgreSQL pool conformance temporary table")
	}
	if err := transaction.Rollback(ctx); err != nil {
		return errors.New("rollback PostgreSQL pool conformance write")
	}
	return nil
}
