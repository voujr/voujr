package rules

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/voujr/voujr/internal/audit"
	"github.com/voujr/voujr/internal/k8s"
)

func deployment(name string, containers ...corev1.Container) appsv1.Deployment {
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: containers}},
		},
	}
}

func snapWith(deps ...appsv1.Deployment) *k8s.Snapshot {
	return &k8s.Snapshot{Cluster: "prod", Deployments: deps}
}

func boolPtr(b bool) *bool { return &b }

func TestPrivilegedContainerFlagsOnlyOffenders(t *testing.T) {
	priv := corev1.Container{Name: "app", SecurityContext: &corev1.SecurityContext{Privileged: boolPtr(true)}}
	clean := corev1.Container{Name: "app"}
	snap := snapWith(deployment("danger", priv), deployment("safe", clean))

	got := PrivilegedContainer{}.Evaluate(context.Background(), snap)
	if len(got) != 1 {
		t.Fatalf("want exactly 1 finding, got %d", len(got))
	}
	if got[0].Resource.Name != "danger" {
		t.Fatalf("flagged the wrong deployment: %s", got[0].Resource.Name)
	}
	if got[0].Severity != audit.P1 || got[0].Category != audit.Security {
		t.Fatalf("privileged should be P1/security, got %s/%s", got[0].Severity, got[0].Category)
	}
}

func TestHostPathVolumeFlagged(t *testing.T) {
	d := deployment("with-hostpath", corev1.Container{Name: "app"})
	d.Spec.Template.Spec.Volumes = []corev1.Volume{{
		Name:         "sock",
		VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/run/docker.sock"}},
	}}
	got := HostPathVolume{}.Evaluate(context.Background(), snapWith(d))
	if len(got) != 1 || got[0].Severity != audit.P2 {
		t.Fatalf("want 1 P2 finding, got %+v", got)
	}
}

func TestMissingResourceRequestsFlagged(t *testing.T) {
	withReq := corev1.Container{Name: "app", Resources: corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
	}}
	withoutReq := corev1.Container{Name: "app"}
	snap := snapWith(deployment("ok", withReq), deployment("bad", withoutReq))

	got := MissingResourceRequests{}.Evaluate(context.Background(), snap)
	if len(got) != 1 || got[0].Resource.Name != "bad" {
		t.Fatalf("only the request-less deployment should be flagged, got %+v", got)
	}
	if got[0].Category != audit.Cost {
		t.Fatalf("missing requests is a cost finding, got %s", got[0].Category)
	}
}

func TestReadinessSeverityScalesWithReplicas(t *testing.T) {
	d := deployment("api", corev1.Container{Name: "app"}) // no readiness probe
	d.Status.Replicas = 3                                 // production traffic
	got := MissingReadinessProbe{}.Evaluate(context.Background(), snapWith(d))
	if len(got) != 1 || got[0].Severity != audit.P1 {
		t.Fatalf("3-replica deployment without readiness should be P1, got %+v", got)
	}
}

func TestRegisterAllCoversFourCategories(t *testing.T) {
	rs := audit.NewRuleSet()
	RegisterAll(rs)
	cats := map[audit.Category]bool{}
	for _, r := range rs.Enabled(nil) {
		cats[r.Category()] = true
	}
	for _, want := range []audit.Category{audit.Reliability, audit.Security, audit.Cost, audit.Optimization} {
		if !cats[want] {
			t.Fatalf("rule library is missing category %q", want)
		}
	}
}
