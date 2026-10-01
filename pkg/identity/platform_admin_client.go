package identity

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

const platformAdminCheckPath = "/tenant.PlatformAdminService/CheckActive"

type PlatformAdminClient interface {
	CheckActive(ctx context.Context, userID string, version int64) (bool, error)
	Close() error
}

type platformAdminClient struct {
	conn    *grpc.ClientConn
	timeout time.Duration
}

func NewPlatformAdminClient(target string, timeout time.Duration) (PlatformAdminClient, error) {
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &platformAdminClient{conn: conn, timeout: timeout}, nil
}

func (c *platformAdminClient) CheckActive(ctx context.Context, userID string, version int64) (bool, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	req, err := structpb.NewStruct(map[string]interface{}{"user_id": userID, "platform_factor_version": float64(version)})
	if err != nil {
		return false, err
	}
	resp := new(structpb.Struct)
	if err := c.conn.Invoke(ctx, platformAdminCheckPath, req, resp, grpc.StaticMethod()); err != nil {
		return false, err
	}
	return resp.GetFields()["allowed"].GetBoolValue(), nil
}

func (c *platformAdminClient) Close() error { return c.conn.Close() }
