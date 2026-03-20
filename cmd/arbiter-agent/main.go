package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	arbiterv1 "github.com/odvcencio/arbiter/api/arbiter/v1"
	"github.com/odvcencio/arbiter/audit"
	"github.com/odvcencio/arbiter/dataplane"
	"github.com/odvcencio/arbiter/grpcserver"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type bundleNamesFlag struct {
	values []string
	seen   map[string]struct{}
}

func (f *bundleNamesFlag) String() string {
	return strings.Join(f.values, ",")
}

func (f *bundleNamesFlag) Set(value string) error {
	for _, name := range splitBundleNames(value) {
		if f.seen == nil {
			f.seen = make(map[string]struct{})
		}
		if _, ok := f.seen[name]; ok {
			continue
		}
		f.seen[name] = struct{}{}
		f.values = append(f.values, name)
	}
	return nil
}

func (f *bundleNamesFlag) Values() []string {
	return append([]string(nil), f.values...)
}

func main() {
	bundleNames := bundleNamesFlag{}
	if err := bundleNames.Set(envOr("ARBITER_BUNDLE_NAMES", envOr("ARBITER_BUNDLE_NAME", ""))); err != nil {
		log.Fatalf("parse bundle names: %v", err)
	}
	readyMaxStalenessDefault, err := parseDurationEnv("ARBITER_AGENT_READY_MAX_STALENESS", "0s")
	if err != nil {
		log.Fatalf("parse ARBITER_AGENT_READY_MAX_STALENESS: %v", err)
	}
	var (
		upstreamAddr      = flag.String("upstream", envOr("ARBITER_UPSTREAM_ADDR", "127.0.0.1:8081"), "upstream Arbiter control-plane gRPC address")
		listenAddr        = flag.String("grpc", envOr("ARBITER_AGENT_ADDR", "127.0.0.1:7081"), "local agent gRPC listen address")
		statusAddr        = flag.String("status", envOr("ARBITER_AGENT_STATUS_ADDR", "127.0.0.1:7082"), "local agent health/status HTTP listen address")
		overridesFile     = flag.String("overrides-file", envOr("ARBITER_OVERRIDES_FILE", ""), "optional override snapshot file to sync")
		readyMaxStaleness = flag.Duration("ready-max-staleness", readyMaxStalenessDefault, "max acceptable age for bundle/override sync before /readyz returns 503; 0 disables freshness enforcement")
	)
	flag.Var(&bundleNames, "bundle-name", "active bundle name to sync from the control plane; repeat or comma-separate to sync multiple bundles")
	flag.Parse()

	names := bundleNames.Values()
	if len(names) == 0 {
		log.Fatal("at least one --bundle-name is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	upstreamConn, err := grpc.NewClient(*upstreamAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("connect upstream: %v", err)
	}
	defer func() { _ = upstreamConn.Close() }()

	upstreamCP := dataplane.NewGRPCControlPlane(arbiterv1.NewArbiterServiceClient(upstreamConn))
	var overrideCP dataplane.OverrideControlPlane = upstreamCP
	if *overridesFile != "" {
		overrideCP = dataplane.NewFileOverrideControlPlane(*overridesFile)
	}
	syncer := dataplane.New(upstreamCP, overrideCP)

	localListener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", *listenAddr, err)
	}
	defer func() { _ = localListener.Close() }()

	localServer := grpc.NewServer()
	arbiterv1.RegisterArbiterServiceServer(localServer, grpcserver.NewServer(syncer.Registry(), syncer.Overrides(), audit.NopSink{}))
	go func() {
		if serveErr := localServer.Serve(localListener); serveErr != nil && ctx.Err() == nil {
			log.Printf("arbiter-agent serve: %v", serveErr)
			stop()
		}
	}()
	defer localServer.Stop()

	var statusServer *http.Server
	if *statusAddr != "" {
		statusServer = &http.Server{
			Addr:              *statusAddr,
			Handler:           newStatusHandler(syncer, readinessPolicy{maxStaleness: *readyMaxStaleness}),
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			if serveErr := statusServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && ctx.Err() == nil {
				log.Printf("arbiter-agent status: %v", serveErr)
				stop()
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = statusServer.Shutdown(shutdownCtx)
		}()
	}

	go func() {
		if runErr := syncer.RunMany(ctx, names); runErr != nil && runErr != context.Canceled {
			log.Printf("arbiter-agent sync: %v", runErr)
			stop()
		}
	}()

	select {
	case <-syncer.Ready():
		status := syncer.Status()
		if snap, ok := syncer.Current(); ok {
			log.Printf(
				"arbiter-agent: synced primary=%s bundle=%s checksum=%s bundles=%d listening=%s upstream=%s status=%s ready_max_staleness=%s",
				status.PrimaryName,
				snap.Bundle.Name,
				snap.Bundle.Checksum,
				len(status.Bundles),
				*listenAddr,
				*upstreamAddr,
				*statusAddr,
				readyMaxStaleness.String(),
			)
		}
	case <-ctx.Done():
		return
	}

	<-ctx.Done()
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func splitBundleNames(value string) []string {
	parts := strings.Split(value, ",")
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	return names
}

func parseDurationEnv(key, fallback string) (time.Duration, error) {
	value := envOr(key, fallback)
	if value == "" {
		return 0, nil
	}
	return time.ParseDuration(value)
}
