package v1

import (
	"context"
	"net/http"

	"github.com/maverickman79/reelix/internal/auth"
	"github.com/maverickman79/reelix/internal/domain"
	"github.com/maverickman79/reelix/internal/service"
)

// ctxKey is unexported so no other package can collide with it.
type ctxKey int

const ctxKeyUser ctxKey = iota

// requireAuth rejects requests without a valid bearer token.
//
// Expired tokens are rejected by the repository query, not here — see
// TokenRepository.UserByTokenHash. This layer cannot forget that check because
// it never sees the expiry.
func (a *API) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := auth.ParseBearer(r.Header.Get("Authorization"))

		user, err := a.auth.Authenticate(r.Context(), token)
		if err != nil {
			writeError(w, r, err)
			return
		}

		next(w, r.WithContext(context.WithValue(r.Context(), ctxKeyUser, user)))
	}
}

// requireAdmin rejects authenticated callers who are not administrators.
//
// It wraps requireAuth rather than being composed alongside it, so there is no
// way to mount an admin route without authentication.
func (a *API) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return a.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if !userFrom(r.Context()).IsAdmin {
			writeError(w, r, service.ErrForbidden)
			return
		}
		next(w, r)
	})
}

// userFrom returns the authenticated user.
//
// It is only valid inside a handler mounted behind requireAuth or
// requireAdmin; the zero user it returns otherwise is not an administrator and
// owns nothing, so a routing mistake fails closed.
func userFrom(ctx context.Context) domain.User {
	if u, ok := ctx.Value(ctxKeyUser).(domain.User); ok {
		return u
	}
	return domain.User{}
}
