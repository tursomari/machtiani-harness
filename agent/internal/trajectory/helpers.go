package trajectory

import "context"

// EmitFromContext retrieves the writer from the provided context and emits the
// event if available.
func EmitFromContext(ctx context.Context, event Event) error {
	w, ok := FromContext(ctx)
	if !ok {
		return nil
	}
	return w.Emit(ctx, event)
}

type parentSpanKey struct{}

// ContextWithParentSpan annotates the context with a parent span identifier so
// nested emitters can link their events.
func ContextWithParentSpan(ctx context.Context, spanID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if spanID == "" {
		return ctx
	}
	return context.WithValue(ctx, parentSpanKey{}, spanID)
}

// ParentSpanID extracts the parent span identifier stored on the context.
func ParentSpanID(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	if v, ok := ctx.Value(parentSpanKey{}).(string); ok && v != "" {
		return v, true
	}
	return "", false
}
