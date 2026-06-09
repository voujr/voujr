package k8s

import (
	"context"

	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// authReview wraps a SelfSubjectAccessReview. The agent uses this to preflight
// every mutation: if the caller's RBAC does not permit the action, the agent
// tells the user precisely which verb/resource is missing instead of failing
// opaquely mid-apply. In CLI mode this reflects the operator's own kubeconfig
// permissions, so the agent can never exceed what the human could do manually.
type authReview struct {
	verb, group, resource, namespace string
}

func (a authReview) run(ctx context.Context, c kubernetes.Interface) (bool, error) {
	ssar := &authzv1.SelfSubjectAccessReview{
		Spec: authzv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authzv1.ResourceAttributes{
				Namespace: a.namespace,
				Verb:      a.verb,
				Group:     a.group,
				Resource:  a.resource,
			},
		},
	}
	out, err := c.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, ssar, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return out.Status.Allowed, nil
}
