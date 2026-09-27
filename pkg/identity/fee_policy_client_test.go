package identity

import (
	"context"
	"errors"
	"math"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

// newFeePolicyClientForTest serves the identity wire contract
// tenant.FeePolicyService/GetPlatformFeePolicy with the given handler.
func newFeePolicyClientForTest(t *testing.T, handle func(context.Context) (*structpb.Struct, error), timeout time.Duration) FeePolicyClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	grpcServer.RegisterService(&grpc.ServiceDesc{
		ServiceName: "tenant.FeePolicyService",
		HandlerType: (*interface{})(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "GetPlatformFeePolicy",
			Handler: func(_ interface{}, ctx context.Context, dec func(interface{}) error, _ grpc.UnaryServerInterceptor) (interface{}, error) {
				req := new(structpb.Struct)
				if err := dec(req); err != nil {
					return nil, err
				}
				return handle(ctx)
			},
		}},
	}, struct{}{})
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial fake identity: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &feePolicyClient{conn: conn, timeout: timeout}
}

func TestFeePolicyClientReadsTheAppliedPolicy(t *testing.T) {
	client := newFeePolicyClientForTest(t, func(context.Context) (*structpb.Struct, error) {
		return structpb.NewStruct(map[string]interface{}{"percent_bps": 500.0, "fixed_fee": 1000.0, "applied_version": 3.0, "desired_version": 4.0})
	}, time.Second)
	policy, err := client.AppliedPlatformFeePolicy(context.Background())
	if err != nil {
		t.Fatalf("AppliedPlatformFeePolicy() error = %v", err)
	}
	// The applied version is what prices the invoice, never the desired one.
	if policy != (domain.PlatformFeePolicy{Version: 3, PercentBps: 500, FixedFee: 1000}) {
		t.Fatalf("policy = %+v", policy)
	}
}

func TestFeePolicyClientFailsClosed(t *testing.T) {
	cases := map[string]func(context.Context) (*structpb.Struct, error){
		"identity unavailable": func(context.Context) (*structpb.Struct, error) {
			return nil, status.Error(codes.Unavailable, "platform fee policy unavailable")
		},
		"not applied yet": func(context.Context) (*structpb.Struct, error) {
			return nil, status.Error(codes.FailedPrecondition, "platform fee policy has no applied version")
		},
		"timeout": func(ctx context.Context) (*structpb.Struct, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	for name, handle := range cases {
		t.Run(name, func(t *testing.T) {
			client := newFeePolicyClientForTest(t, handle, 50*time.Millisecond)
			if _, err := client.AppliedPlatformFeePolicy(context.Background()); !errors.Is(err, domain.ErrPlatformFeePolicyUnavailable) {
				t.Fatalf("error = %v, want ErrPlatformFeePolicyUnavailable", err)
			}
		})
	}
}

func TestParsePlatformFeePolicyRejectsMalformedResponses(t *testing.T) {
	valid := func() map[string]*structpb.Value {
		return map[string]*structpb.Value{
			"percent_bps": structpb.NewNumberValue(500), "fixed_fee": structpb.NewNumberValue(1000), "applied_version": structpb.NewNumberValue(3),
		}
	}
	mutations := map[string]func(map[string]*structpb.Value){
		"missing percent":      func(f map[string]*structpb.Value) { delete(f, "percent_bps") },
		"missing fixed fee":    func(f map[string]*structpb.Value) { delete(f, "fixed_fee") },
		"missing version":      func(f map[string]*structpb.Value) { delete(f, "applied_version") },
		"string percent":       func(f map[string]*structpb.Value) { f["percent_bps"] = structpb.NewStringValue("500") },
		"fractional fixed fee": func(f map[string]*structpb.Value) { f["fixed_fee"] = structpb.NewNumberValue(10.5) },
		"negative version":     func(f map[string]*structpb.Value) { f["applied_version"] = structpb.NewNumberValue(-1) },
		"NaN percent":          func(f map[string]*structpb.Value) { f["percent_bps"] = structpb.NewNumberValue(math.NaN()) },
		"percent above bound":  func(f map[string]*structpb.Value) { f["percent_bps"] = structpb.NewNumberValue(2001) },
		"fixed above bound":    func(f map[string]*structpb.Value) { f["fixed_fee"] = structpb.NewNumberValue(50001) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			fields := valid()
			mutate(fields)
			if _, err := ParsePlatformFeePolicy(&structpb.Struct{Fields: fields}); !errors.Is(err, domain.ErrPlatformFeePolicyUnavailable) {
				t.Fatalf("error = %v, want ErrPlatformFeePolicyUnavailable", err)
			}
		})
	}
	if _, err := ParsePlatformFeePolicy(&structpb.Struct{Fields: valid()}); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}
}
