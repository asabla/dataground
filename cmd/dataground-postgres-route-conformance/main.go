package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/asabla/dataground/internal/execution/recoveryconformance/pgrouteproxy"
	"github.com/asabla/dataground/internal/persistence"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("dataground-postgres-route-conformance", flag.ContinueOnError)
	flags.SetOutput(stderr)
	mode := flags.String("mode", "", "serve, supervise, select, status, state, role or pool")
	listenAddress := flags.String("listen-address", "", "literal loopback client endpoint")
	controlSocket := flags.String("control-socket", "", "absolute private Unix control socket path")
	stateFile := flags.String("state-file", "", "absolute private persistent route state path")
	primaryTarget := flags.String("primary-target", "", "literal loopback primary endpoint")
	promotedTarget := flags.String("promoted-target", "", "literal loopback promoted endpoint")
	routeValue := flags.String("route", "", "primary or promoted")
	promotionGeneration := flags.Uint64("promotion-generation", 0, "expected nonzero PostgreSQL timeline ID")
	supervisorPID := flags.Int("supervisor-pid", 0, "expected conformance supervisor process ID")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "invalid PostgreSQL route conformance configuration")
		return 2
	}

	switch *mode {
	case "serve":
		initializing := *routeValue != "" || *promotionGeneration != 0
		if *listenAddress == "" || *controlSocket == "" || *primaryTarget == "" ||
			*stateFile == "" || *promotedTarget == "" ||
			*supervisorPID < 0 ||
			(initializing && (!validRoute(*routeValue) || *promotionGeneration == 0)) {
			fmt.Fprintln(stderr, "invalid PostgreSQL route conformance configuration")
			return 2
		}
		if err := validateRouteChildOwnership(*supervisorPID); err != nil {
			fmt.Fprintln(stderr, "invalid PostgreSQL route conformance ownership")
			return 1
		}
		probe, err := targetHealthProbe(os.Getenv("DATAGROUND_ROUTER_HEALTH_DATABASE_URL"))
		if err != nil {
			fmt.Fprintln(stderr, "invalid PostgreSQL route conformance configuration")
			return 2
		}
		proxy, err := pgrouteproxy.Start(ctx, pgrouteproxy.Config{
			ListenAddress:              *listenAddress,
			ControlSocket:              *controlSocket,
			StateFile:                  *stateFile,
			PrimaryTarget:              *primaryTarget,
			PromotedTarget:             *promotedTarget,
			InitialRoute:               pgrouteproxy.Route(*routeValue),
			InitialPromotionGeneration: *promotionGeneration,
			HealthProbe:                probe,
		})
		if err != nil {
			fmt.Fprintln(stderr, "could not start PostgreSQL route conformance proxy")
			return 1
		}
		<-ctx.Done()
		if err := proxy.Close(); err != nil {
			fmt.Fprintln(stderr, "could not stop PostgreSQL route conformance proxy")
			return 1
		}
		return 0
	case "supervise":
		initializing := *routeValue != "" || *promotionGeneration != 0
		if *listenAddress == "" || *controlSocket == "" || *primaryTarget == "" ||
			*stateFile == "" || *promotedTarget == "" ||
			*supervisorPID != 0 ||
			(initializing && (!validRoute(*routeValue) || *promotionGeneration == 0)) {
			fmt.Fprintln(stderr, "invalid PostgreSQL route conformance configuration")
			return 2
		}
		if _, err := targetHealthProbe(os.Getenv("DATAGROUND_ROUTER_HEALTH_DATABASE_URL")); err != nil {
			fmt.Fprintln(stderr, "invalid PostgreSQL route conformance configuration")
			return 2
		}
		if err := superviseRoute(ctx, routeSupervisorConfig{
			ListenAddress:              *listenAddress,
			ControlSocket:              *controlSocket,
			StateFile:                  *stateFile,
			PrimaryTarget:              *primaryTarget,
			PromotedTarget:             *promotedTarget,
			InitialRoute:               *routeValue,
			InitialPromotionGeneration: *promotionGeneration,
		}, stdout); err != nil {
			fmt.Fprintln(stderr, "PostgreSQL route supervision failed")
			return 1
		}
		return 0
	case "select":
		if *controlSocket == "" || *routeValue != "" || *listenAddress != "" ||
			*stateFile != "" || *primaryTarget != "" || *promotedTarget != "" ||
			*promotionGeneration == 0 || *supervisorPID != 0 {
			fmt.Fprintln(stderr, "invalid PostgreSQL route conformance configuration")
			return 2
		}
		bounded, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		route, err := pgrouteproxy.SelectWritable(bounded, *controlSocket, *promotionGeneration)
		if err != nil {
			fmt.Fprintln(stderr, "could not select writable PostgreSQL route conformance target")
			return 1
		}
		fmt.Fprintln(stdout, route)
		return 0
	case "status":
		if *controlSocket == "" || *routeValue != "" || *listenAddress != "" ||
			*stateFile != "" || *primaryTarget != "" || *promotedTarget != "" ||
			*promotionGeneration != 0 || *supervisorPID != 0 {
			fmt.Fprintln(stderr, "invalid PostgreSQL route conformance configuration")
			return 2
		}
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		route, err := pgrouteproxy.Status(bounded, *controlSocket)
		if err != nil {
			fmt.Fprintln(stderr, "could not read PostgreSQL route conformance proxy status")
			return 1
		}
		fmt.Fprintln(stdout, route)
		return 0
	case "state":
		if *controlSocket == "" || *routeValue != "" || *listenAddress != "" ||
			*stateFile != "" || *primaryTarget != "" || *promotedTarget != "" ||
			*promotionGeneration != 0 || *supervisorPID != 0 {
			fmt.Fprintln(stderr, "invalid PostgreSQL route conformance configuration")
			return 2
		}
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		route, generation, err := pgrouteproxy.StateStatus(bounded, *controlSocket)
		if err != nil {
			fmt.Fprintln(stderr, "could not read PostgreSQL route conformance state")
			return 1
		}
		fmt.Fprintf(stdout, "%s %d\n", route, generation)
		return 0
	case "role":
		if *controlSocket != "" || *routeValue != "" || *listenAddress != "" ||
			*stateFile != "" || *primaryTarget != "" || *promotedTarget != "" ||
			*promotionGeneration != 0 || *supervisorPID != 0 {
			fmt.Fprintln(stderr, "invalid PostgreSQL route conformance configuration")
			return 2
		}
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		role, err := databaseRole(bounded, os.Getenv("DATAGROUND_TEST_DATABASE_URL"))
		if err != nil {
			fmt.Fprintln(stderr, "could not read PostgreSQL route conformance role")
			return 1
		}
		fmt.Fprintln(stdout, role)
		return 0
	case "pool":
		if *controlSocket != "" || *routeValue != "" || *listenAddress != "" ||
			*stateFile != "" || *primaryTarget != "" || *promotedTarget != "" ||
			*promotionGeneration != 0 || *supervisorPID != 0 {
			fmt.Fprintln(stderr, "invalid PostgreSQL route conformance configuration")
			return 2
		}
		if err := runPoolConformance(ctx, os.Getenv("DATAGROUND_TEST_DATABASE_URL"), stdout); err != nil {
			fmt.Fprintln(stderr, "PostgreSQL pool reconnection conformance failed")
			return 1
		}
		return 0
	default:
		fmt.Fprintln(stderr, "invalid PostgreSQL route conformance configuration")
		return 2
	}
}

func targetHealthProbe(databaseURL string) (pgrouteproxy.HealthProbe, error) {
	if err := validateLoopbackDatabaseURL(databaseURL); err != nil {
		return nil, err
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL route conformance database URL")
	}
	return func(ctx context.Context, target string) (pgrouteproxy.Health, error) {
		candidate := *parsed
		candidate.Host = target
		health, err := databaseHealth(ctx, candidate.String())
		if err != nil {
			return pgrouteproxy.Health{}, err
		}
		return health, nil
	}, nil
}

func validRoute(route string) bool {
	return route == string(pgrouteproxy.Primary) || route == string(pgrouteproxy.Promoted)
}

func databaseRole(ctx context.Context, databaseURL string) (string, error) {
	health, err := databaseHealth(ctx, databaseURL)
	if err != nil {
		return "", err
	}
	if health.Writable {
		return "writable-primary", nil
	}
	return "read-only-standby", nil
}

func databaseHealth(ctx context.Context, databaseURL string) (pgrouteproxy.Health, error) {
	if err := validateLoopbackDatabaseURL(databaseURL); err != nil {
		return pgrouteproxy.Health{}, err
	}
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		return pgrouteproxy.Health{}, errors.New("open PostgreSQL route conformance database")
	}
	defer database.Close()
	var inRecovery bool
	var readOnly bool
	var promotionGeneration int64
	if err := database.QueryRowContext(
		ctx,
		`SELECT pg_is_in_recovery(),
          current_setting('transaction_read_only') = 'on',
          CASE
            WHEN NOT pg_is_in_recovery()
              AND current_setting('transaction_read_only') = 'off'
            THEN (pg_split_walfile_name(pg_walfile_name(pg_current_wal_lsn()))).timeline_id::bigint
            ELSE 0::bigint
          END`,
	).Scan(&inRecovery, &readOnly, &promotionGeneration); err != nil {
		return pgrouteproxy.Health{}, errors.New("query PostgreSQL route conformance health")
	}
	if !inRecovery && !readOnly {
		if promotionGeneration <= 0 {
			return pgrouteproxy.Health{}, errors.New("invalid PostgreSQL promotion generation")
		}
		return pgrouteproxy.Health{
			Writable:            true,
			PromotionGeneration: uint64(promotionGeneration),
		}, nil
	}
	if inRecovery && readOnly {
		return pgrouteproxy.Health{}, nil
	}
	return pgrouteproxy.Health{}, errors.New("inconsistent PostgreSQL route conformance role")
}

func validateLoopbackDatabaseURL(databaseURL string) error {
	parsed, err := url.Parse(databaseURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") ||
		parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.Fragment != "" ||
		parsed.User == nil || parsed.User.Username() == "" || parsed.Path == "" || parsed.Path == "/" {
		return errors.New("invalid PostgreSQL route conformance database URL")
	}
	host, _, err := net.SplitHostPort(parsed.Host)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.Equal(net.IPv4(127, 0, 0, 1)) {
		return errors.New("invalid PostgreSQL route conformance database URL")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(query) != 1 || len(query["sslmode"]) != 1 || query.Get("sslmode") != "disable" {
		return errors.New("invalid PostgreSQL route conformance database URL")
	}
	return nil
}
