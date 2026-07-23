package service

import "context"

// mergeServiceContext returns a child context canceled when either parent or the
// service lifetime context is canceled.
func mergeServiceContext(parent, serviceCtx context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if serviceCtx == nil {
		return context.WithCancel(parent)
	}

	ctx, cancel := context.WithCancel(parent)
	if serviceCtx.Err() != nil {
		cancel()
		return ctx, func() {}
	}

	stop := context.AfterFunc(serviceCtx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}
