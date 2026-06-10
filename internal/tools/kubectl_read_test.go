package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/voujr/voujr/internal/k8s"
)

// stubClusters is a clusterSource backed by an injected (fake) cluster.
type stubClusters struct{ c *k8s.Cluster }

func (s stubClusters) Active() (*k8s.Cluster, error) { return s.c, nil }

func TestKubectlEventsWarningsOnly(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: "prod"},
			Type:           "Normal",
			Reason:         "Scheduled",
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "api-1"},
			Message:        "Successfully assigned",
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "e2", Namespace: "prod"},
			Type:           "Warning",
			Reason:         "BackOff",
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "api-1"},
			Message:        "Back-off restarting failed container",
		},
	)
	tool := KubectlEvents{Clusters: stubClusters{k8s.NewClusterWithClientset("prod", cs)}}
	args, _ := json.Marshal(map[string]any{"namespace": "prod", "warnings_only": true})

	res, err := tool.Execute(context.Background(), args, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.ModelView, "BackOff") {
		t.Fatalf("expected the warning event, got:\n%s", res.ModelView)
	}
	if strings.Contains(res.ModelView, "Scheduled") {
		t.Fatalf("normal event should be filtered out:\n%s", res.ModelView)
	}
}

func TestKubectlDescribePodSurfacesCrashReason(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "prod"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "app",
				Ready:        false,
				RestartCount: 7,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason: "CrashLoopBackOff", Message: "back-off 5m0s restarting failed container",
				}},
			}},
		},
	}
	tool := KubectlDescribe{Clusters: stubClusters{k8s.NewClusterWithClientset("prod", fake.NewSimpleClientset(pod))}}
	args, _ := json.Marshal(map[string]any{"namespace": "prod", "kind": "pod", "name": "api-1"})

	res, err := tool.Execute(context.Background(), args, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CrashLoopBackOff", "restarts=7"} {
		if !strings.Contains(res.ModelView, want) {
			t.Fatalf("describe output missing %q:\n%s", want, res.ModelView)
		}
	}
}
