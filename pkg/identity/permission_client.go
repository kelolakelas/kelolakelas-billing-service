// Package identity is billing's client for the identity service's authorization
// contract. It only speaks the raw `tenant.PermissionService/CheckPermission` wire
// contract (a structpb request and response), the same one academic uses, so billing
// does not need to import identity's module or generated code.
package identity

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

const permissionServiceCheckPermissionPath = "/tenant.PermissionService/CheckPermission"

// PermissionClient asks identity whether a role grants a permission.
type PermissionClient interface {
	// CheckPermission asks identity whether roleID grants permission while operating on
	// tenantID. The tenant is part of the question because identity only accepts a role
	// that belongs to that tenant or is a system role, so a role lifted from another
	// tenant can never satisfy the check.
	CheckPermission(ctx context.Context, tenantID, roleID, permission string) (bool, error)
	Close() error
}

type permissionClient struct {
	conn    *grpc.ClientConn
	timeout time.Duration
}

// NewPermissionClient creates a lazily connected client for target. The connection is
// not dialled here, so billing starts even while identity is unreachable; each check
// then fails and the caller answers 503 instead of serving data.
func NewPermissionClient(target string, timeout time.Duration) (PermissionClient, error) {
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &permissionClient{conn: conn, timeout: timeout}, nil
}

func (c *permissionClient) CheckPermission(ctx context.Context, tenantID, roleID, permission string) (bool, error) {
	// A hung identity must not hold a transaction read open indefinitely; the deadline
	// turns it into an error, which the middleware reports as 503.
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	req, err := structpb.NewStruct(map[string]interface{}{
		"role_id":    roleID,
		"permission": permission,
		"tenant_id":  tenantID,
	})
	if err != nil {
		return false, err
	}
	resp := new(structpb.Struct)
	if err := c.conn.Invoke(ctx, permissionServiceCheckPermissionPath, req, resp, grpc.StaticMethod()); err != nil {
		return false, err
	}
	value, ok := resp.GetFields()["allowed"]
	return ok && value.GetBoolValue(), nil
}

func (c *permissionClient) Close() error { return c.conn.Close() }
