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
	mode := flags.String("mode", "", "serve, route, status or role")
	listenAddress := flags.String("listen-address", "", "literal loopback client endpoint")
	controlSocket := flags.String("control-socket", "", "absolute private Unix control socket path")
	primaryTarget := flags.String("primary-target", "", "literal loopback primary endpoint")
	promotedTarget := flags.String("promoted-target", "", "literal loopback promoted endpoint")
	routeValue := flags.String("route", "", "primary or promoted")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "invalid PostgreSQL route conformance configuration")
		return 2
	}

	switch *mode {
	case "serve":
		if *listenAddress == "" || *controlSocket == "" || *primaryTarget == "" ||
			*promotedTarget == "" || !validRoute(*routeValue) {
			fmt.Fprintln(stderr, "invalid PostgreSQL route conformance configuration")
			return 2
		}
		proxy, err := pgrouteproxy.Start(ctx, pgrouteproxy.Config{
			ListenAddress:  *listenAddress,
			ControlSocket:  *controlSocket,
			PrimaryTarget:  *primaryTarget,
			PromotedTarget: *promotedTarget,
			InitialRoute:   pgrouteproxy.Route(*routeValue),
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
	case "route":
		if *controlSocket == "" || !validRoute(*routeValue) || *listenAddress != "" ||
			*primaryTarget != "" || *promotedTarget != "" {
			fmt.Fprintln(stderr, "invalid PostgreSQL route conformance configuration")
			return 2
		}
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := pgrouteproxy.RouteTo(bounded, *controlSocket, pgrouteproxy.Route(*routeValue)); err != nil {
			fmt.Fprintln(stderr, "could not change PostgreSQL route conformance proxy")
			return 1
		}
		return 0
	case "status":
		if *controlSocket == "" || *routeValue != "" || *listenAddress != "" ||
			*primaryTarget != "" || *promotedTarget != "" {
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
	case "role":
		if *controlSocket != "" || *routeValue != "" || *listenAddress != "" ||
			*primaryTarget != "" || *promotedTarget != "" {
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
	default:
		fmt.Fprintln(stderr, "invalid PostgreSQL route conformance configuration")
		return 2
	}
}

func validRoute(route string) bool {
	return route == string(pgrouteproxy.Primary) || route == string(pgrouteproxy.Promoted)
}

func databaseRole(ctx context.Context, databaseURL string) (string, error) {
	parsed, err := url.Parse(databaseURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") ||
		parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.Fragment != "" {
		return "", errors.New("invalid PostgreSQL route conformance database URL")
	}
	host, _, err := net.SplitHostPort(parsed.Host)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.Equal(net.IPv4(127, 0, 0, 1)) {
		return "", errors.New("invalid PostgreSQL route conformance database URL")
	}
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		return "", errors.New("open PostgreSQL route conformance database")
	}
	defer database.Close()
	var inRecovery bool
	var readOnly bool
	if err := database.QueryRowContext(
		ctx,
		"SELECT pg_is_in_recovery(), current_setting('transaction_read_only') = 'on'",
	).Scan(&inRecovery, &readOnly); err != nil {
		return "", errors.New("query PostgreSQL route conformance role")
	}
	if !inRecovery && !readOnly {
		return "writable-primary", nil
	}
	if inRecovery && readOnly {
		return "read-only-standby", nil
	}
	return "", errors.New("inconsistent PostgreSQL route conformance role")
}
