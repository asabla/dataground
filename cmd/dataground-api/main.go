package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultAddress = "127.0.0.1:8082"

type apiRuntime struct {
	handler           http.Handler
	pool              *pgxpool.Pool
	securityLifecycle func(context.Context) error
	mode              string
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := runAPI(logger); err != nil {
		logger.Error("DataGround API stopped", "error", err)
		os.Exit(1)
	}
}

func runAPI(logger *slog.Logger) error {
	if logger == nil {
		return errors.New("API logger is required")
	}
	address := os.Getenv("DATAGROUND_HTTP_ADDRESS")
	if address == "" {
		address = defaultAddress
	}

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()

	runtime, err := assembleAPIRuntime(ctx, address)
	if err != nil {
		return err
	}
	defer runtime.Close()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("bind API listener: %w", err)
	}
	server := &http.Server{
		Addr:              address,
		Handler:           runtime.handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	logger.Info("starting DataGround API", "address", address, "mode", runtime.mode)
	return serveAPIRuntime(ctx, cancel, logger, server, listener, runtime.securityLifecycle)
}

func serveAPIRuntime(
	ctx context.Context,
	cancel context.CancelFunc,
	logger *slog.Logger,
	server *http.Server,
	listener net.Listener,
	securityLifecycle func(context.Context) error,
) error {
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	var lifecycleErrors chan error
	if securityLifecycle != nil {
		lifecycleErrors = make(chan error, 1)
		go func() {
			lifecycleErrors <- securityLifecycle(ctx)
		}()
	}

	serveDone := false
	lifecycleDone := securityLifecycle == nil
	var result error
	select {
	case serveErr := <-serveErrors:
		serveDone = true
		if serveErr == nil || !errors.Is(serveErr, http.ErrServerClosed) {
			result = fmt.Errorf("HTTP server failed: %w", serveErr)
		}
	case lifecycleErr := <-lifecycleErrors:
		lifecycleDone = true
		if lifecycleErr == nil {
			result = errors.New("API security lifecycle stopped unexpectedly")
		} else if errors.Is(lifecycleErr, context.Canceled) && ctx.Err() == nil {
			result = errors.New("API security lifecycle cancelled without its owner")
		} else if !errors.Is(lifecycleErr, context.Canceled) {
			result = fmt.Errorf("API security lifecycle failed: %w", lifecycleErr)
		}
	case <-ctx.Done():
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	shutdownErr := server.Shutdown(shutdownCtx)
	shutdownCancel()
	if shutdownErr != nil {
		logger.Error("HTTP server shutdown failed", "error", shutdownErr)
		_ = server.Close()
		if result == nil {
			result = shutdownErr
		}
	}
	if !serveDone {
		if serveErr := <-serveErrors; serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && result == nil {
			result = fmt.Errorf("HTTP server failed: %w", serveErr)
		}
	}
	if !lifecycleDone {
		if lifecycleErr := <-lifecycleErrors; lifecycleErr != nil &&
			!errors.Is(lifecycleErr, context.Canceled) && result == nil {
			result = fmt.Errorf("API security lifecycle failed: %w", lifecycleErr)
		}
	}
	return result
}

func assembleAPIRuntime(ctx context.Context, address string) (*apiRuntime, error) {
	if ctx == nil {
		return nil, errors.New("API startup context is required")
	}
	dispatchTarget, err := loadGovernedDispatchTarget(os.LookupEnv)
	if err != nil {
		return nil, err
	}
	configurationPath, oidcMode := os.LookupEnv("DATAGROUND_API_SECURITY_CONFIG_FILE")
	if oidcMode {
		if configurationPath == "" {
			return nil, errors.New("DATAGROUND_API_SECURITY_CONFIG_FILE must not be empty")
		}
		if err := requireExplicitLoopbackAddress(address); err != nil {
			return nil, err
		}
		if _, exists := os.LookupEnv("DATAGROUND_DEVELOPMENT_BEARER_TOKEN"); exists {
			return nil, errors.New("development bearer credentials cannot be configured in OIDC mode")
		}
		certificationPath := os.Getenv("DATAGROUND_RELEASE_CERTIFICATION_FILE")
		trustProfilePath := os.Getenv("DATAGROUND_RELEASE_CERTIFICATION_TRUST_FILE")
		if certificationPath == "" || trustProfilePath == "" {
			return nil, errors.New("OIDC mode requires release certification and trust profile files")
		}
		configuration, policy, err := loadOIDCSecurityConfiguration(
			configurationPath,
			certificationPath,
			trustProfilePath,
		)
		if err != nil {
			return nil, err
		}
		defer clear(policy)
		databaseURL := os.Getenv("DATAGROUND_DATABASE_URL")
		if databaseURL == "" {
			return nil, errors.New("DATAGROUND_DATABASE_URL is required in OIDC mode")
		}
		pool, repository, err := openDurableRepository(ctx, databaseURL)
		if err != nil {
			return nil, err
		}
		assembly, err := composeOIDCSecurity(ctx, repository, configuration, policy, dispatchTarget)
		if err != nil {
			pool.Close()
			return nil, err
		}
		mode := "oidc-dpop"
		if dispatchTarget != nil {
			mode = "oidc-dpop-governed-development"
		}
		return &apiRuntime{
			handler:           assembly.Handler(),
			pool:              pool,
			securityLifecycle: assembly.RunOIDCKeysetRefresh,
			mode:              mode,
		}, nil
	}

	authenticator, authorizer, err := developmentSecurity(address)
	if err != nil {
		return nil, fmt.Errorf("development security configuration: %w", err)
	}
	handler, err := api.NewHandler(authenticator, authorizer)
	if err != nil {
		return nil, fmt.Errorf("API authentication assembly: %w", err)
	}
	runtime := &apiRuntime{handler: handler, mode: "reference"}
	databaseURL := os.Getenv("DATAGROUND_DATABASE_URL")
	if databaseURL == "" {
		if dispatchTarget != nil {
			return nil, errors.New("governed dispatch requires durable API mode")
		}
		if address != defaultAddress {
			return nil, errors.New("process-local reference mode may only bind to the default loopback address")
		}
		return runtime, nil
	}
	pool, repository, err := openDurableRepository(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	auditedAuthenticator, err := authn.NewAuditedAuthenticator(authenticator, repository)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("durable authentication audit assembly: %w", err)
	}
	auditedAuthorizer, err := authz.NewAuditedAuthorizer(authorizer, repository)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("durable authorization audit assembly: %w", err)
	}
	if dispatchTarget == nil {
		handler, err = api.NewDurableHandler(repository, auditedAuthenticator, auditedAuthorizer)
	} else {
		handler, err = api.NewGovernedDurableHandler(
			ctx,
			repository,
			auditedAuthenticator,
			auditedAuthorizer,
			*dispatchTarget,
		)
	}
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("durable API security assembly: %w", err)
	}
	runtime.handler = handler
	runtime.pool = pool
	if dispatchTarget == nil {
		runtime.mode = "durable-development"
	} else {
		runtime.mode = "durable-governed-development"
	}
	return runtime, nil
}

func openDurableRepository(
	ctx context.Context,
	databaseURL string,
) (*pgxpool.Pool, *persistence.Repository, error) {
	startupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	database, err := persistence.OpenSQL(startupCtx, databaseURL)
	if err == nil {
		err = persistence.RequireCurrentSchema(startupCtx, database)
		closeErr := database.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		return nil, nil, fmt.Errorf("durable API startup: %w", err)
	}
	pool, err := persistence.OpenPool(startupCtx, databaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("durable API startup: %w", err)
	}
	return pool, persistence.NewRepository(pool), nil
}

func (runtime *apiRuntime) Close() {
	if runtime != nil && runtime.pool != nil {
		runtime.pool.Close()
	}
}

func developmentSecurity(address string) (authn.Authenticator, authz.Authorizer, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, nil, fmt.Errorf("parse HTTP address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, nil, errors.New("development security requires an explicit loopback IP address")
	}

	token, exists := os.LookupEnv("DATAGROUND_DEVELOPMENT_BEARER_TOKEN")
	if !exists {
		return nil, nil, errors.New("DATAGROUND_DEVELOPMENT_BEARER_TOKEN is required")
	}
	if err := os.Unsetenv("DATAGROUND_DEVELOPMENT_BEARER_TOKEN"); err != nil {
		return nil, nil, fmt.Errorf("remove development bearer token from environment: %w", err)
	}
	principalID := os.Getenv("DATAGROUND_DEVELOPMENT_PRINCIPAL_ID")
	domainID := os.Getenv("DATAGROUND_DEVELOPMENT_ISOLATION_DOMAIN_ID")
	authorizer, err := authz.NewDevelopmentCedarAuthorizer(principalID, domainID)
	if err != nil {
		return nil, nil, fmt.Errorf("development Cedar authorization: %w", err)
	}
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{
		BearerToken:       []byte(token),
		PrincipalID:       principalID,
		IsolationDomainID: domainID,
	})
	if err != nil {
		return nil, nil, err
	}
	return authenticator, authorizer, nil
}
