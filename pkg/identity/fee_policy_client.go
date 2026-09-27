package identity

import (
	"context"
	"fmt"
	"math"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/kelolakelas/kelolakelas-billing-service/internal/domain"
)

const feePolicyServiceGetPlatformFeePolicyPath = "/tenant.FeePolicyService/GetPlatformFeePolicy"

// FeePolicyClient reads the applied platform fee policy (KEL-99) from identity
// over the same raw structpb gRPC contract as CheckPermission. It implements
// domain.PlatformFeePolicyReader.
type FeePolicyClient interface {
	domain.PlatformFeePolicyReader
	Close() error
}

type feePolicyClient struct {
	conn    *grpc.ClientConn
	timeout time.Duration
}

// NewFeePolicyClient creates a lazily connected client for target, so billing
// starts while identity is unreachable; each read then fails and invoice
// creation is refused.
func NewFeePolicyClient(target string, timeout time.Duration) (FeePolicyClient, error) {
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &feePolicyClient{conn: conn, timeout: timeout}, nil
}

// AppliedPlatformFeePolicy returns the applied rule. Any transport error, a
// missing or non-integral field, or a value outside the owner-approved bounds
// is domain.ErrPlatformFeePolicyUnavailable: there is no 0% fallback.
func (c *feePolicyClient) AppliedPlatformFeePolicy(ctx context.Context) (domain.PlatformFeePolicy, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	resp := new(structpb.Struct)
	if err := c.conn.Invoke(ctx, feePolicyServiceGetPlatformFeePolicyPath, &structpb.Struct{}, resp, grpc.StaticMethod()); err != nil {
		return domain.PlatformFeePolicy{}, fmt.Errorf("%w: %v", domain.ErrPlatformFeePolicyUnavailable, err)
	}
	return ParsePlatformFeePolicy(resp)
}

func (c *feePolicyClient) Close() error { return c.conn.Close() }

// ParsePlatformFeePolicy validates identity's response strictly. It is
// exported so the malformed-response cases are testable without a server.
func ParsePlatformFeePolicy(resp *structpb.Struct) (domain.PlatformFeePolicy, error) {
	fields := resp.GetFields()
	integer := func(key string) (int64, bool) {
		value, ok := fields[key]
		if !ok {
			return 0, false
		}
		number, ok := value.GetKind().(*structpb.Value_NumberValue)
		if !ok {
			return 0, false
		}
		f := number.NumberValue
		if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || f < 0 || f > 1<<53 {
			return 0, false
		}
		return int64(f), true
	}
	version, versionOK := integer("applied_version")
	percent, percentOK := integer("percent_bps")
	fixed, fixedOK := integer("fixed_fee")
	if !versionOK || !percentOK || !fixedOK {
		return domain.PlatformFeePolicy{}, fmt.Errorf("%w: malformed response", domain.ErrPlatformFeePolicyUnavailable)
	}
	policy := domain.PlatformFeePolicy{Version: version, PercentBps: percent, FixedFee: fixed}
	if err := policy.Validate(); err != nil {
		return domain.PlatformFeePolicy{}, fmt.Errorf("%w: out of bounds", domain.ErrPlatformFeePolicyUnavailable)
	}
	return policy, nil
}
