package controller

import (
	"testing"

	"github.com/stretchr/testify/require"
	zv1 "github.com/zalando-incubator/stackset-controller/pkg/apis/zalando.org/v1"
	apps "k8s.io/api/apps/v1"
	autoscaling "k8s.io/api/autoscaling/v2beta1"
	v1 "k8s.io/api/core/v1"
	extensions "k8s.io/api/extensions/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var (
	testStackSet = zv1.StackSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
			UID:       "123",
		},
	}
	baseTestStack = zv1.Stack{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "foo-v1",
			Namespace:       testStackSet.Namespace,
			UID:             "456",
			Generation:      1,
			OwnerReferences: stacksetOwned(testStackSet).OwnerReferences,
		},
	}
	updatedTestStack = *baseTestStack.DeepCopy()

	baseTestStackOwned    = stackOwned(baseTestStack)
	updatedTestStackOwned = stackOwned(baseTestStack)
)

func init() {
	baseTestStackOwned.Annotations = map[string]string{"stackset-controller.zalando.org/stack-generation": "1"}

	updatedTestStack.Generation = 2
	updatedTestStackOwned.Annotations = map[string]string{"stackset-controller.zalando.org/stack-generation": "2"}
}

func TestReconcileStackDeployment(t *testing.T) {
	exampleReplicas := int32(3)
	updatedReplicas := int32(4)

	examplePodTemplateSpec := v1.PodTemplateSpec{
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  "foo",
					Image: "nginx",
				},
			},
		},
	}
	updatedPodTemplateSpec := v1.PodTemplateSpec{
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  "bar",
					Image: "nginx",
				},
			},
		},
	}

	for _, tc := range []struct {
		name     string
		stack    zv1.Stack
		existing *apps.Deployment
		updated  *apps.Deployment
		expected *apps.Deployment
	}{
		{
			name:  "deployment is created if it doesn't exist",
			stack: baseTestStack,
			updated: &apps.Deployment{
				ObjectMeta: baseTestStackOwned,
				Spec: apps.DeploymentSpec{
					Replicas: &exampleReplicas,
					Template: examplePodTemplateSpec,
				},
			},
			expected: &apps.Deployment{
				ObjectMeta: baseTestStackOwned,
				Spec: apps.DeploymentSpec{
					Replicas: &exampleReplicas,
					Template: examplePodTemplateSpec,
				},
			},
		},
		{
			name:  "deployment is updated if the stack version changes",
			stack: updatedTestStack,
			existing: &apps.Deployment{
				ObjectMeta: baseTestStackOwned,
				Spec: apps.DeploymentSpec{
					Replicas: &exampleReplicas,
					Template: examplePodTemplateSpec,
				},
			},
			updated: &apps.Deployment{
				ObjectMeta: updatedTestStackOwned,
				Spec: apps.DeploymentSpec{
					Replicas: &exampleReplicas,
					Template: updatedPodTemplateSpec,
				},
			},
			expected: &apps.Deployment{
				ObjectMeta: updatedTestStackOwned,
				Spec: apps.DeploymentSpec{
					Replicas: &exampleReplicas,
					Template: updatedPodTemplateSpec,
				},
			},
		},
		{
			name:  "deployment is updated if the replica count is set",
			stack: baseTestStack,
			existing: &apps.Deployment{
				ObjectMeta: baseTestStackOwned,
				Spec: apps.DeploymentSpec{
					Replicas: &exampleReplicas,
					Template: examplePodTemplateSpec,
				},
			},
			updated: &apps.Deployment{
				ObjectMeta: baseTestStackOwned,
				Spec: apps.DeploymentSpec{
					Replicas: &updatedReplicas,
					Template: examplePodTemplateSpec,
				},
			},
			expected: &apps.Deployment{
				ObjectMeta: baseTestStackOwned,
				Spec: apps.DeploymentSpec{
					Replicas: &updatedReplicas,
					Template: examplePodTemplateSpec,
				},
			},
		},
		{
			name:  "deployment is not updated if the stack version remains the same and replica count is unset",
			stack: baseTestStack,
			existing: &apps.Deployment{
				ObjectMeta: baseTestStackOwned,
				Spec: apps.DeploymentSpec{
					Replicas: &exampleReplicas,
					Template: examplePodTemplateSpec,
				},
			},
			updated: &apps.Deployment{
				ObjectMeta: baseTestStackOwned,
				Spec: apps.DeploymentSpec{
					Replicas: nil,
					Template: updatedPodTemplateSpec,
				},
			},
			expected: &apps.Deployment{
				ObjectMeta: baseTestStackOwned,
				Spec: apps.DeploymentSpec{
					Replicas: &exampleReplicas,
					Template: examplePodTemplateSpec,
				},
			},
		},
		{
			name:  "spec.selector is preserved",
			stack: baseTestStack,
			existing: &apps.Deployment{
				ObjectMeta: baseTestStackOwned,
				Spec: apps.DeploymentSpec{
					Replicas: &exampleReplicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"foo": "bar"},
					},
					Template: examplePodTemplateSpec,
				},
			},
			updated: &apps.Deployment{
				ObjectMeta: updatedTestStackOwned,
				Spec: apps.DeploymentSpec{
					Replicas: &exampleReplicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"updated": "selector"},
					},
					Template: updatedPodTemplateSpec,
				},
			},
			expected: &apps.Deployment{
				ObjectMeta: updatedTestStackOwned,
				Spec: apps.DeploymentSpec{
					Replicas: &exampleReplicas,
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"foo": "bar"},
					},
					Template: updatedPodTemplateSpec,
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := NewTestEnvironment()

			err := env.CreateStacksets([]zv1.StackSet{testStackSet})
			require.NoError(t, err)

			err = env.CreateStacks([]zv1.Stack{tc.stack})
			require.NoError(t, err)

			if tc.existing != nil {
				err = env.CreateDeployments([]apps.Deployment{*tc.existing})
				require.NoError(t, err)
			}

			err = env.controller.ReconcileStackDeployment(&tc.stack, tc.existing, func() *apps.Deployment {
				return tc.updated
			})
			require.NoError(t, err)

			updated, err := env.client.AppsV1().Deployments(tc.stack.Namespace).Get(tc.stack.Name, metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, tc.expected, updated)
		})
	}
}

func TestReconcileStackService(t *testing.T) {
	examplePorts := []v1.ServicePort{
		{
			Name:       "foo",
			Protocol:   v1.ProtocolTCP,
			Port:       8080,
			TargetPort: intstr.FromInt(80),
		},
	}
	exampleUpdatedPorts := []v1.ServicePort{
		{
			Name:       "bar",
			Protocol:   v1.ProtocolTCP,
			Port:       9090,
			TargetPort: intstr.FromInt(90),
		},
	}
	exampleClusterIP := "10.3.0.1"

	for _, tc := range []struct {
		name     string
		stack    zv1.Stack
		existing *v1.Service
		updated  *v1.Service
		expected *v1.Service
	}{
		{
			name:  "service is created if it doesn't exist",
			stack: baseTestStack,
			updated: &v1.Service{
				ObjectMeta: baseTestStackOwned,
				Spec: v1.ServiceSpec{
					Ports: examplePorts,
				},
			},
			expected: &v1.Service{
				ObjectMeta: baseTestStackOwned,
				Spec: v1.ServiceSpec{
					Ports: examplePorts,
				},
			},
		},
		{
			name:  "service is updated if the stack changes, ClusterIP is preserved",
			stack: updatedTestStack,
			existing: &v1.Service{
				ObjectMeta: baseTestStackOwned,
				Spec: v1.ServiceSpec{
					Ports:     examplePorts,
					ClusterIP: exampleClusterIP,
				},
			},
			updated: &v1.Service{
				ObjectMeta: updatedTestStackOwned,
				Spec: v1.ServiceSpec{
					Ports: exampleUpdatedPorts,
				},
			},
			expected: &v1.Service{
				ObjectMeta: updatedTestStackOwned,
				Spec: v1.ServiceSpec{
					Ports:     exampleUpdatedPorts,
					ClusterIP: exampleClusterIP,
				},
			},
		},
		{
			name:  "service is not updated if the stack version remains the same",
			stack: baseTestStack,
			existing: &v1.Service{
				ObjectMeta: baseTestStackOwned,
				Spec: v1.ServiceSpec{
					Ports:     examplePorts,
					ClusterIP: exampleClusterIP,
				},
			},
			updated: &v1.Service{
				ObjectMeta: baseTestStackOwned,
				Spec: v1.ServiceSpec{
					Ports: exampleUpdatedPorts,
				},
			},
			expected: &v1.Service{
				ObjectMeta: baseTestStackOwned,
				Spec: v1.ServiceSpec{
					Ports:     examplePorts,
					ClusterIP: exampleClusterIP,
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := NewTestEnvironment()

			err := env.CreateStacksets([]zv1.StackSet{testStackSet})
			require.NoError(t, err)

			err = env.CreateStacks([]zv1.Stack{tc.stack})
			require.NoError(t, err)

			if tc.existing != nil {
				err = env.CreateServices([]v1.Service{*tc.existing})
				require.NoError(t, err)
			}

			err = env.controller.ReconcileStackService(&tc.stack, tc.existing, func() (*v1.Service, error) {
				return tc.updated, nil
			})
			require.NoError(t, err)

			updated, err := env.client.CoreV1().Services(tc.stack.Namespace).Get(tc.stack.Name, metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, tc.expected, updated)
		})
	}
}

func TestReconcileStackHPA(t *testing.T) {
	exampleResource := resource.MustParse("10m")
	exampleMetrics := []autoscaling.MetricSpec{
		{
			Type: "cpu",
			Resource: &autoscaling.ResourceMetricSource{
				Name:               "cpu",
				TargetAverageValue: &exampleResource,
			},
		},
	}
	exampleUpdatedResource := resource.MustParse("20m")
	exampleUpdatedMetrics := []autoscaling.MetricSpec{
		{
			Type: "cpu",
			Resource: &autoscaling.ResourceMetricSource{
				Name:               "cpu",
				TargetAverageValue: &exampleUpdatedResource,
			},
		},
	}

	exampleMinReplicas := int32(3)
	exampleUpdatedMinReplicas := int32(5)

	for _, tc := range []struct {
		name     string
		stack    zv1.Stack
		existing *autoscaling.HorizontalPodAutoscaler
		updated  *autoscaling.HorizontalPodAutoscaler
		expected *autoscaling.HorizontalPodAutoscaler
	}{
		{
			name:  "HPA is created if it doesn't exist",
			stack: baseTestStack,
			updated: &autoscaling.HorizontalPodAutoscaler{
				ObjectMeta: baseTestStackOwned,
				Spec: autoscaling.HorizontalPodAutoscalerSpec{
					MinReplicas: &exampleMinReplicas,
					MaxReplicas: 5,
					Metrics:     exampleMetrics,
				},
			},
			expected: &autoscaling.HorizontalPodAutoscaler{
				ObjectMeta: baseTestStackOwned,
				Spec: autoscaling.HorizontalPodAutoscalerSpec{
					MinReplicas: &exampleMinReplicas,
					MaxReplicas: 5,
					Metrics:     exampleMetrics,
				},
			},
		},
		{
			name:  "HPA is removed if it's no longer needed",
			stack: baseTestStack,
			existing: &autoscaling.HorizontalPodAutoscaler{
				ObjectMeta: baseTestStackOwned,
				Spec: autoscaling.HorizontalPodAutoscalerSpec{
					MinReplicas: &exampleMinReplicas,
					MaxReplicas: 5,
					Metrics:     exampleMetrics,
				},
			},
			updated:  nil,
			expected: nil,
		},
		{
			name:  "HPA is updated if stack version changes",
			stack: updatedTestStack,
			existing: &autoscaling.HorizontalPodAutoscaler{
				ObjectMeta: baseTestStackOwned,
				Spec: autoscaling.HorizontalPodAutoscalerSpec{
					MinReplicas: &exampleMinReplicas,
					MaxReplicas: 5,
					Metrics:     exampleMetrics,
				},
			},
			updated: &autoscaling.HorizontalPodAutoscaler{
				ObjectMeta: updatedTestStackOwned,
				Spec: autoscaling.HorizontalPodAutoscalerSpec{
					MinReplicas: &exampleMinReplicas,
					MaxReplicas: 7,
					Metrics:     exampleUpdatedMetrics,
				},
			},
			expected: &autoscaling.HorizontalPodAutoscaler{
				ObjectMeta: updatedTestStackOwned,
				Spec: autoscaling.HorizontalPodAutoscalerSpec{
					MinReplicas: &exampleMinReplicas,
					MaxReplicas: 7,
					Metrics:     exampleUpdatedMetrics,
				},
			},
		},
		{
			name:  "HPA is updated if min. replicas is changed",
			stack: updatedTestStack,
			existing: &autoscaling.HorizontalPodAutoscaler{
				ObjectMeta: baseTestStackOwned,
				Spec: autoscaling.HorizontalPodAutoscalerSpec{
					MinReplicas: &exampleMinReplicas,
					MaxReplicas: 5,
					Metrics:     exampleMetrics,
				},
			},
			updated: &autoscaling.HorizontalPodAutoscaler{
				ObjectMeta: baseTestStackOwned,
				Spec: autoscaling.HorizontalPodAutoscalerSpec{
					MinReplicas: &exampleUpdatedMinReplicas,
					MaxReplicas: 5,
					Metrics:     exampleMetrics,
				},
			},
			expected: &autoscaling.HorizontalPodAutoscaler{
				ObjectMeta: baseTestStackOwned,
				Spec: autoscaling.HorizontalPodAutoscalerSpec{
					MinReplicas: &exampleUpdatedMinReplicas,
					MaxReplicas: 5,
					Metrics:     exampleMetrics,
				},
			},
		},
		{
			name:  "HPA is not updated if the stack version remains the same and min. replicas are unchanged",
			stack: baseTestStack,
			existing: &autoscaling.HorizontalPodAutoscaler{
				ObjectMeta: baseTestStackOwned,
				Spec: autoscaling.HorizontalPodAutoscalerSpec{
					MinReplicas: &exampleMinReplicas,
					MaxReplicas: 5,
					Metrics:     exampleMetrics,
				},
			},
			updated: &autoscaling.HorizontalPodAutoscaler{
				ObjectMeta: baseTestStackOwned,
				Spec: autoscaling.HorizontalPodAutoscalerSpec{
					MinReplicas: &exampleMinReplicas,
					MaxReplicas: 5,
					Metrics:     exampleUpdatedMetrics,
				},
			},
			expected: &autoscaling.HorizontalPodAutoscaler{
				ObjectMeta: baseTestStackOwned,
				Spec: autoscaling.HorizontalPodAutoscalerSpec{
					MinReplicas: &exampleMinReplicas,
					MaxReplicas: 5,
					Metrics:     exampleMetrics,
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := NewTestEnvironment()

			err := env.CreateStacksets([]zv1.StackSet{testStackSet})
			require.NoError(t, err)

			err = env.CreateStacks([]zv1.Stack{tc.stack})
			require.NoError(t, err)

			if tc.existing != nil {
				err = env.CreateHPAs([]autoscaling.HorizontalPodAutoscaler{*tc.existing})
				require.NoError(t, err)
			}

			err = env.controller.ReconcileStackHPA(&tc.stack, tc.existing, func() (*autoscaling.HorizontalPodAutoscaler, error) {
				return tc.updated, nil
			})
			require.NoError(t, err)

			updated, err := env.client.AutoscalingV2beta1().HorizontalPodAutoscalers(tc.stack.Namespace).Get(tc.stack.Name, metav1.GetOptions{})
			if tc.expected != nil {
				require.NoError(t, err)
				require.Equal(t, tc.expected, updated)
			} else {
				require.True(t, errors.IsNotFound(err))
			}
		})
	}
}

func TestReconcileStackIngress(t *testing.T) {
	exampleRules := []extensions.IngressRule{
		{
			Host: "example.org",
			IngressRuleValue: extensions.IngressRuleValue{
				HTTP: &extensions.HTTPIngressRuleValue{
					Paths: []extensions.HTTPIngressPath{
						{
							Path: "/",
							Backend: extensions.IngressBackend{
								ServiceName: "foo",
								ServicePort: intstr.FromInt(80),
							},
						},
					},
				},
			},
		},
	}
	exampleUpdatedRules := []extensions.IngressRule{
		{
			Host: "example.com",
			IngressRuleValue: extensions.IngressRuleValue{
				HTTP: &extensions.HTTPIngressRuleValue{
					Paths: []extensions.HTTPIngressPath{
						{
							Path: "/",
							Backend: extensions.IngressBackend{
								ServiceName: "bar",
								ServicePort: intstr.FromInt(8181),
							},
						},
					},
				},
			},
		},
	}

	for _, tc := range []struct {
		name     string
		stack    zv1.Stack
		existing *extensions.Ingress
		updated  *extensions.Ingress
		expected *extensions.Ingress
	}{
		{
			name:  "ingress is created if it doesn't exist",
			stack: baseTestStack,
			updated: &extensions.Ingress{
				ObjectMeta: baseTestStackOwned,
				Spec: extensions.IngressSpec{
					Rules: exampleRules,
				},
			},
			expected: &extensions.Ingress{
				ObjectMeta: baseTestStackOwned,
				Spec: extensions.IngressSpec{
					Rules: exampleRules,
				},
			},
		},
		{
			name:  "ingress is removed if it is no longer needed",
			stack: baseTestStack,
			existing: &extensions.Ingress{
				ObjectMeta: baseTestStackOwned,
				Spec: extensions.IngressSpec{
					Rules: exampleRules,
				},
			},
			updated:  nil,
			expected: nil,
		},
		{
			name:  "ingress is updated if the stack changes",
			stack: updatedTestStack,
			existing: &extensions.Ingress{
				ObjectMeta: baseTestStackOwned,
				Spec: extensions.IngressSpec{
					Rules: exampleRules,
				},
			},
			updated: &extensions.Ingress{
				ObjectMeta: updatedTestStackOwned,
				Spec: extensions.IngressSpec{
					Rules: exampleUpdatedRules,
				},
			},
			expected: &extensions.Ingress{
				ObjectMeta: updatedTestStackOwned,
				Spec: extensions.IngressSpec{
					Rules: exampleUpdatedRules,
				},
			},
		},
		{
			name:  "ingress is not updated if the stack version remains the same",
			stack: baseTestStack,
			existing: &extensions.Ingress{
				ObjectMeta: baseTestStackOwned,
				Spec: extensions.IngressSpec{
					Rules: exampleRules,
				},
			},
			updated: &extensions.Ingress{
				ObjectMeta: baseTestStackOwned,
				Spec: extensions.IngressSpec{
					Rules: exampleUpdatedRules,
				},
			},
			expected: &extensions.Ingress{
				ObjectMeta: baseTestStackOwned,
				Spec: extensions.IngressSpec{
					Rules: exampleRules,
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := NewTestEnvironment()

			err := env.CreateStacksets([]zv1.StackSet{testStackSet})
			require.NoError(t, err)

			err = env.CreateStacks([]zv1.Stack{tc.stack})
			require.NoError(t, err)

			if tc.existing != nil {
				err = env.CreateIngresses([]extensions.Ingress{*tc.existing})
				require.NoError(t, err)
			}

			err = env.controller.ReconcileStackIngress(&tc.stack, tc.existing, func() (*extensions.Ingress, error) {
				return tc.updated, nil
			})
			require.NoError(t, err)

			updated, err := env.client.ExtensionsV1beta1().Ingresses(tc.stack.Namespace).Get(tc.stack.Name, metav1.GetOptions{})
			if tc.expected != nil {
				require.NoError(t, err)
				require.Equal(t, tc.expected, updated)
			} else {
				require.True(t, errors.IsNotFound(err))
			}
		})
	}
}
