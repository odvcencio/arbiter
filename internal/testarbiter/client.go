package testarbiter

import (
	"context"
	"net"
	"os"
	"testing"

	arbiterv1 "github.com/odvcencio/arbiter/api/arbiter/v1"
	"github.com/odvcencio/arbiter/audit"
	"github.com/odvcencio/arbiter/grpcserver"
	"github.com/odvcencio/arbiter/overrides"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// NewClient returns a client connected either to ARBITER_ADDR or to a local
// in-memory Arbiter gRPC server when no external address is configured.
func NewClient(t testing.TB) arbiterv1.ArbiterServiceClient {
	t.Helper()

	if addr := os.Getenv("ARBITER_ADDR"); addr != "" {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatalf("grpc.NewClient(%q): %v", addr, err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return arbiterv1.NewArbiterServiceClient(conn)
	}

	listener := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	arbiterv1.RegisterArbiterServiceServer(grpcSrv, grpcserver.NewServer(grpcserver.NewRegistry(), overrides.NewStore(), audit.NopSink{}))
	go func() {
		_ = grpcSrv.Serve(listener)
	}()

	dialer := func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		grpcSrv.Stop()
		_ = listener.Close()
		t.Fatalf("grpc.NewClient(bufnet): %v", err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
		grpcSrv.Stop()
		_ = listener.Close()
	})
	return arbiterv1.NewArbiterServiceClient(conn)
}
