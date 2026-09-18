package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type podAccess struct {
	config *rest.Config
	client kubernetes.Interface
	role   *rbacv1.Role
}

// The identity is real: the API server authorizes impersonated requests
// against this scenario's namespace-scoped Role and RoleBinding.
func (w RealWatch) withRevocableWatch(rec *brine.Recorder) (RealWatch, error) {
	if w.config == nil || w.Observed != nil {
		return RealWatch{}, fmt.Errorf("configure watch access before the initial read")
	}
	if w.route == nil {
		var err error
		w, err = w.withWatchRoute(rec)
		if err != nil {
			return RealWatch{}, err
		}
	}
	ctx, cancel := context.WithTimeout(w.Ctx, 10*time.Second)
	defer cancel()
	access, err := newPodAccess(ctx, rec, w.Clientset, w.config, &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "watch-reader", Namespace: w.Namespace},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "watch"}}},
	}, "brine-watch-"+w.Namespace)
	if err != nil {
		return RealWatch{}, err
	}
	client := access.client
	if err := awaitWatchAccess(ctx, client, w.Namespace, w.Name, true); err != nil {
		return RealWatch{}, err
	}
	w.Watcher.Stop()
	w.Watcher = jetbridge.NewPodWatcher(client, w.Namespace, w.Name)
	rec.RegisterDisposer(w.Watcher.Stop)
	w.access = access
	return w, nil
}

// Probe actual Get and Watch operations until the real authorizer has observed
// the Role change. These establish the fixture premise, not a request-count
// assertion about the runtime.
func awaitWatchAccess(ctx context.Context, client kubernetes.Interface, namespace, name string, allowed bool) error {
	pods := client.CoreV1().Pods(namespace)
	for {
		pod, err := pods.Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			stream, watchErr := pods.Watch(ctx, metav1.ListOptions{
				FieldSelector: "metadata.name=" + name, ResourceVersion: pod.ResourceVersion,
			})
			if watchErr == nil {
				stream.Stop()
				if allowed {
					return nil
				}
			} else if apierrors.IsForbidden(watchErr) {
				if !allowed {
					return nil
				}
			} else {
				return fmt.Errorf("verify real watch permission: %w", watchErr)
			}
		} else if !allowed || !apierrors.IsForbidden(err) {
			return fmt.Errorf("direct pod reads must remain available: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("watch permission did not become allowed=%t: %w", allowed, ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (w RealWatch) dropRevokedWatch(rec *brine.Recorder) (RealWatch, error) {
	if w.access == nil || w.route == nil || w.Err != nil || w.Observed == nil {
		return RealWatch{}, fmt.Errorf("revocation requires an initial read with revocable watch access")
	}
	ctx, cancel := context.WithTimeout(w.Ctx, 10*time.Second)
	defer cancel()
	if _, err := w.watchCheckpoint(ctx); err != nil {
		return RealWatch{}, err
	}
	role := w.access.role.DeepCopy()
	role.Rules[0].Verbs = []string{"get"}
	updated, err := w.Clientset.RbacV1().Roles(w.Namespace).Update(ctx, role, metav1.UpdateOptions{})
	if err != nil {
		return RealWatch{}, err
	}
	w.access.role = updated
	if err := awaitWatchAccess(ctx, w.access.client, w.Namespace, w.Name, false); err != nil {
		return RealWatch{}, err
	}
	// Revoking permission does not terminate an existing watch. Close the
	// actual TCP stream so Next must re-establish it and meet the denial.
	address := w.route.Addr().String()
	if err := w.route.Close(); err != nil {
		return RealWatch{}, err
	}
	w.route, err = ownWatchRoute(rec, address, w.target)
	if err != nil {
		return RealWatch{}, err
	}
	return w, nil
}

// newPodAccess owns one real namespace-scoped identity and its RBAC lifecycle.
// Watch and process-read faults share this setup, not a simulated authorizer.
func newPodAccess(ctx context.Context, rec *brine.Recorder, admin kubernetes.Interface, base *rest.Config, requested *rbacv1.Role, user string) (*podAccess, error) {
	role, err := admin.RbacV1().Roles(requested.Namespace).Create(ctx, requested, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	registerAPICleanup(rec, "pod access Role", func(ctx context.Context) error {
		return admin.RbacV1().Roles(role.Namespace).Delete(ctx, role.Name,
			metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &role.UID}})
	})
	binding, err := admin.RbacV1().RoleBindings(requested.Namespace).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: role.Name, Namespace: requested.Namespace},
		Subjects:   []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: user}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	registerAPICleanup(rec, "pod access RoleBinding", func(ctx context.Context) error {
		return admin.RbacV1().RoleBindings(binding.Namespace).Delete(ctx, binding.Name,
			metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &binding.UID}})
	})
	config := rest.CopyConfig(base)
	config.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: []string{"system:authenticated"}}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &podAccess{client: client, role: role, config: config}, nil
}
