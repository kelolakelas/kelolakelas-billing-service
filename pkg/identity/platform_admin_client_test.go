package identity

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"
)

type platformAdminWireService interface {
	CheckActive(context.Context, *structpb.Struct) (*structpb.Struct, error)
}
type platformAdminWireStub struct {
	allowed bool
	wait    bool
	user    string
	version float64
}

func (s *platformAdminWireStub) CheckActive(ctx context.Context, req *structpb.Struct) (*structpb.Struct, error) {
	s.user = req.GetFields()["user_id"].GetStringValue()
	s.version = req.GetFields()["platform_factor_version"].GetNumberValue()
	if s.wait {
		<-ctx.Done()
		return nil, status.Error(codes.DeadlineExceeded, "deadline")
	}
	return structpb.NewStruct(map[string]interface{}{"allowed": s.allowed})
}
func TestPlatformAdminClientWireAndTimeout(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	stub := &platformAdminWireStub{allowed: true}
	server.RegisterService(&grpc.ServiceDesc{ServiceName: "tenant.PlatformAdminService", HandlerType: (*platformAdminWireService)(nil), Methods: []grpc.MethodDesc{{MethodName: "CheckActive", Handler: func(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
		req := new(structpb.Struct)
		if err := dec(req); err != nil {
			return nil, err
		}
		return srv.(platformAdminWireService).CheckActive(ctx, req)
	}}}}, stub)
	go server.Serve(listener)
	defer server.Stop()
	defer listener.Close()
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	client := &platformAdminClient{conn: conn, timeout: time.Second}
	defer client.Close()
	allowed, err := client.CheckActive(context.Background(), "admin-id", 4)
	if err != nil || !allowed || stub.user != "admin-id" || stub.version != 4 {
		t.Fatalf("allowed=%v err=%v stub=%+v", allowed, err, stub)
	}
	stub.allowed = false
	allowed, err = client.CheckActive(context.Background(), "revoked", 4)
	if err != nil || allowed {
		t.Fatalf("revoked allowed=%v err=%v", allowed, err)
	}
	stub.wait = true
	client.timeout = 20 * time.Millisecond
	allowed, err = client.CheckActive(context.Background(), "admin-id", 4)
	if allowed || !errors.Is(err, context.DeadlineExceeded) && status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("timeout allowed=%v err=%v", allowed, err)
	}
}
