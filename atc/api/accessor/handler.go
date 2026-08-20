package accessor

import (
	"context"
	"net/http"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/auditor"
)

const accessorContextKey atc.ContextKey = "accessor"
const customRolesContextKey atc.ContextKey = "custom-roles"

//counterfeiter:generate . AccessFactory
type AccessFactory interface {
	Create(req *http.Request, role string) (Access, error)
}

func NewHandler(
	logger lager.Logger,
	action string,
	handler http.Handler,
	accessFactory AccessFactory,
	auditor auditor.Auditor,
	customRoles map[string]string,
) http.Handler {
	return &accessorHandler{
		logger:        logger,
		handler:       handler,
		accessFactory: accessFactory,
		action:        action,
		auditor:       auditor,
		customRoles:   customRoles,
	}
}

type accessorHandler struct {
	logger        lager.Logger
	action        string
	handler       http.Handler
	accessFactory AccessFactory
	auditor       auditor.Auditor
	customRoles   map[string]string
}

func (h *accessorHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := context.WithValue(r.Context(), customRolesContextKey, h.customRoles)
	requiredRole := RequiredRole(ctx, h.action)

	acc, err := h.accessFactory.Create(r, requiredRole)
	if err != nil {
		h.logger.Error("failed-to-construct-accessor", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	claims := acc.Claims()

	ctx = context.WithValue(ctx, accessorContextKey, acc)

	h.auditor.Audit(h.action, claims.UserName, r)
	h.handler.ServeHTTP(w, r.WithContext(ctx))
}

func RequiredRole(ctx context.Context, action string) string {
	if customRoles, ok := ctx.Value(customRolesContextKey).(map[string]string); ok {
		if role := customRoles[action]; role != "" {
			return role
		}
	}

	return DefaultRoles[action]
}

func GetAccessor(r *http.Request) Access {
	accessor := r.Context().Value(accessorContextKey)
	if accessor != nil {
		return accessor.(Access)
	}

	return &access{}
}
