package httpapi

import (
	"context"

	"github.com/MuunBob/hoodalpha/internal/application"
)

// contextWithIdentity attaches a verified identity to a request context.
func contextWithIdentity(ctx context.Context, id application.Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

// identityFrom returns the identity placed by requireInitData.
//
// Handlers only run behind that middleware, so a missing identity is a wiring
// bug rather than a runtime condition. It returns the zero value instead of
// panicking, and the zero UserID matches no rows, so a mistake fails closed.
func identityFrom(r interface{ Context() context.Context }) application.Identity {
	id, _ := r.Context().Value(identityKey).(application.Identity)
	return id
}
