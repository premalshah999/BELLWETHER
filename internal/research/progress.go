package research

import "context"

type progressKey struct{}

// WithProgress attaches a per-request observer; engines are shared across jobs.
func WithProgress(ctx context.Context, report func(stage, detail string)) context.Context {
	return context.WithValue(ctx, progressKey{}, report)
}

func reportProgress(ctx context.Context, stage, detail string) {
	if report, ok := ctx.Value(progressKey{}).(func(string, string)); ok {
		report(stage, detail)
	}
}
