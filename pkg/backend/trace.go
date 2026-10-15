package backend

import (
	"context"

	"github.com/tuna-os/corral/pkg/types"
)

// contextBinder is an adapter whose backend calls can carry a request
// context, so that the commands and API calls it makes log the request ID.
type contextBinder interface {
	withContext(ctx context.Context) Adapter
}

// ForContext is For, with the adapter's backend calls bound to ctx. Only the
// values of ctx travel, for the log records; an adapter that has nothing to
// bind (local QEMU, the SDK-driven backends) is returned unchanged.
func ForContext(ctx context.Context, ref types.InstanceRef) (Adapter, error) {
	a, err := For(ref)
	if err != nil {
		return nil, err
	}
	if b, ok := a.(contextBinder); ok {
		return b.withContext(ctx), nil
	}
	return a, nil
}

func (a kubevirtAdapter) withContext(ctx context.Context) Adapter {
	return kubevirtAdapter{client: a.client.WithContext(ctx)}
}

func (a incusAdapter) withContext(ctx context.Context) Adapter {
	return incusAdapter{client: a.client.WithContext(ctx)}
}

func (a libvirtAdapter) withContext(ctx context.Context) Adapter {
	return libvirtAdapter{client: a.client.WithContext(ctx)}
}

func (a proxmoxAdapter) withContext(ctx context.Context) Adapter {
	a.ctx = ctx
	return a
}
