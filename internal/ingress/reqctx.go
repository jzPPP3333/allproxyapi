package ingress

import "context"

// Request attribution.
//
// The logging middleware wraps the whole request, but only the chat handlers
// know which model was requested and which account served it. They record that
// on the request context and the middleware reads it back, which keeps the
// attribution out of the middleware's control flow.

type reqInfoKey struct{}

// requestInfo carries what a handler learned about a request, for the metrics
// record.
type requestInfo struct {
	Model    string
	Provider string
	Account  string
	Tokens   int
	Err      string
}

// withRequestInfo attaches a mutable info box to the context.
func withRequestInfo(ctx context.Context) (context.Context, *requestInfo) {
	info := &requestInfo{}
	return context.WithValue(ctx, reqInfoKey{}, info), info
}

// requestInfoFrom reads the info box, or nil when none was attached.
func requestInfoFrom(ctx context.Context) *requestInfo {
	info, _ := ctx.Value(reqInfoKey{}).(*requestInfo)
	return info
}
