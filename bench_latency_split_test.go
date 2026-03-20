package arbiter

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"testing"
	"time"

	arbiterv1 "github.com/odvcencio/arbiter/api/arbiter/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

//go:embed examples/battletest/fraud.arb
var fraudLatencyBenchmarkSource []byte

func BenchmarkLatencySplit(b *testing.B) {
	ctxMap := fraudLatencyBenchmarkContext()

	b.Run("in_process_governed_eval", func(b *testing.B) {
		full, err := CompileFull(fraudLatencyBenchmarkSource)
		if err != nil {
			b.Fatalf("CompileFull: %v", err)
		}
		dc := DataFromMap(ctxMap, full.Ruleset)

		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			matched, _, err := EvalGoverned(full.Ruleset, dc, full.Segments, ctxMap)
			if err != nil {
				b.Fatalf("EvalGoverned: %v", err)
			}
			if len(matched) == 0 {
				b.Fatal("expected at least one matched rule")
			}
		}
	})

	b.Run("grpc_in_cluster", func(b *testing.B) {
		runGRPCEvalBenchmark(b, os.Getenv("ARBITER_BENCH_IN_CLUSTER_ADDR"), fraudLatencyBenchmarkSource, ctxMap)
	})

	b.Run("grpc_port_forward", func(b *testing.B) {
		runGRPCEvalBenchmark(b, os.Getenv("ARBITER_BENCH_PORT_FORWARD_ADDR"), fraudLatencyBenchmarkSource, ctxMap)
	})
}

func runGRPCEvalBenchmark(b *testing.B, addr string, source []byte, ctxMap map[string]any) {
	b.Helper()
	if addr == "" {
		b.Skip("set benchmark address env var to run this benchmark")
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		b.Fatalf("grpc.NewClient(%q): %v", addr, err)
	}
	b.Cleanup(func() { conn.Close() })

	client := arbiterv1.NewArbiterServiceClient(conn)
	ctx := context.Background()
	pub, err := client.PublishBundle(ctx, &arbiterv1.PublishBundleRequest{
		Name:   fmt.Sprintf("bench-fraud-%d", time.Now().UnixNano()),
		Source: source,
	})
	if err != nil {
		b.Fatalf("PublishBundle: %v", err)
	}

	payload, err := structpb.NewStruct(ctxMap)
	if err != nil {
		b.Fatalf("structpb.NewStruct: %v", err)
	}
	req := &arbiterv1.EvaluateRulesRequest{
		BundleId: pub.BundleId,
		Context:  payload,
	}

	for i := 0; i < 10; i++ {
		resp, err := client.EvaluateRules(ctx, req)
		if err != nil {
			b.Fatalf("warmup EvaluateRules: %v", err)
		}
		if len(resp.Matched) == 0 {
			b.Fatal("expected at least one matched rule during warmup")
		}
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		resp, err := client.EvaluateRules(ctx, req)
		if err != nil {
			b.Fatalf("EvaluateRules: %v", err)
		}
		if len(resp.Matched) == 0 {
			b.Fatal("expected at least one matched rule")
		}
	}
}

func fraudLatencyBenchmarkContext() map[string]any {
	return map[string]any{
		"account": map[string]any{
			"flagged":          false,
			"age_days":         200.0,
			"verified":         true,
			"chargeback_count": 0.0,
			"home_currency":    "USD",
		},
		"model": map[string]any{
			"risk_score": 0.2,
		},
		"tx": map[string]any{
			"amount":          50.0,
			"country":         "US",
			"currency":        "USD",
			"count_last_hour": 1.0,
			"total_last_hour": 50.0,
		},
	}
}
