package identity

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"
)

// fakeIdentityServer implements the raw CheckPermission wire contract and records what
// billing sent, so the test proves the request shape identity actually receives.
type fakeIdentityServer struct {
	lastTenantID string
	lastRoleID   string
	lastPerm     string
	allowed      bool
	delay        time.Duration
}

func (s *fakeIdentityServer) checkPermission(ctx context.Context, req *structpb.Struct) (*structpb.Struct, error) {
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	fields := req.GetFields()
	s.lastTenantID = fields["tenant_id"].GetStringValue()
	s.lastRoleID = fields["role_id"].GetStringValue()
	s.lastPerm = fields["permission"].GetStringValue()
	return structpb.NewStruct(map[string]interface{}{"allowed": s.allowed})
}

func newClientForTest(t *testing.T, server *fakeIdentityServer, timeout time.Duration) PermissionClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	grpcServer.RegisterService(&grpc.ServiceDesc{
		ServiceName: "tenant.PermissionService",
		HandlerType: (*interface{})(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "CheckPermission",
			Handler: func(_ interface{}, ctx context.Context, dec func(interface{}) error, _ grpc.UnaryServerInterceptor) (interface{}, error) {
				req := new(structpb.Struct)
				if err := dec(req); err != nil {
					return nil, err
				}
				return server.checkPermission(ctx, req)
			},
		}},
	}, server)
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
	return &permissionClient{conn: conn, timeout: timeout}
}

const (
	testTenantID = "11111111-1111-1111-1111-111111111111"
	testRoleID   = "22222222-2222-2222-2222-222222222222"
)

// KEL-57: billing asks identity the same tenant-scoped question academic asks (ADR 0002),
// so tenant_id must travel with role_id and the permission name.
func TestCheckPermissionSendsTenantRoleAndPermission(t *testing.T) {
	server := &fakeIdentityServer{allowed: true}
	client := newClientForTest(t, server, time.Second)

	allowed, err := client.CheckPermission(context.Background(), testTenantID, testRoleID, "billing:read")
	if err != nil || !allowed {
		t.Fatalf("allowed=%t err=%v, want allowed", allowed, err)
	}
	if server.lastTenantID != testTenantID || server.lastRoleID != testRoleID || server.lastPerm != "billing:read" {
		t.Fatalf("identity received (%q,%q,%q)", server.lastTenantID, server.lastRoleID, server.lastPerm)
	}
}

func TestCheckPermissionReportsDenial(t *testing.T) {
	client := newClientForTest(t, &fakeIdentityServer{allowed: false}, time.Second)
	allowed, err := client.CheckPermission(context.Background(), testTenantID, testRoleID, "billing:read")
	if err != nil || allowed {
		t.Fatalf("allowed=%t err=%v, want a clean denial", allowed, err)
	}
}

// A hung identity must surface as an error (the middleware turns it into 503) rather
// than holding the request open.
func TestCheckPermissionTimesOut(t *testing.T) {
	client := newClientForTest(t, &fakeIdentityServer{allowed: true, delay: 2 * time.Second}, 50*time.Millisecond)
	started := time.Now()
	allowed, err := client.CheckPermission(context.Background(), testTenantID, testRoleID, "billing:read")
	if err == nil || allowed {
		t.Fatalf("allowed=%t err=%v, want a timeout error", allowed, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("check took %s, want it bounded by the client timeout", elapsed)
	}
}
