package core

import (
	"encoding/json"
	"errors"
	"sort"

	zv1 "github.com/zalando-incubator/stackset-controller/pkg/apis/zalando.org/v1"
	corev1 "k8s.io/api/core/v1"
	extensions "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	StacksetHeritageLabelKey = "stackset"
	StackVersionLabelKey     = "stack-version"
)

var (
	errNoPaths  = errors.New("invalid ingress, no paths defined")
	errNoStacks = errors.New("no stacks to assign traffic to")
)

func currentStackVersion(stackset *zv1.StackSet) string {
	version := stackset.Spec.StackTemplate.Spec.Version
	if version == "" {
		version = defaultVersion
	}
	return version
}

func generateStackName(stackset *zv1.StackSet, version string) string {
	return stackset.Name + "-" + version
}

// sanitizeServicePorts makes sure the ports has the default fields set if not
// specified.
func sanitizeServicePorts(service *zv1.StackServiceSpec) *zv1.StackServiceSpec {
	for i, port := range service.Ports {
		// set default protocol if not specified
		if port.Protocol == "" {
			port.Protocol = corev1.ProtocolTCP
		}
		service.Ports[i] = port
	}
	return service
}

// NewStack returns an (optional) stack that should be created
func (ssc *StackSetContainer) NewStack() (*StackContainer, string) {
	stackset := ssc.StackSet

	observedStackVersion := stackset.Status.ObservedStackVersion
	stackVersion := currentStackVersion(stackset)
	stackName := generateStackName(stackset, stackVersion)

	stack := ssc.stackByName(stackName)

	// If the current stack doesn't exist, check that we haven't created it before. We shouldn't recreate
	// it if it was removed for any reason.
	if stack == nil && observedStackVersion != stackVersion {
		var service *zv1.StackServiceSpec
		if stackset.Spec.StackTemplate.Spec.Service != nil {
			service = sanitizeServicePorts(stackset.Spec.StackTemplate.Spec.Service)
		}

		return &StackContainer{
			Stack: &zv1.Stack{
				ObjectMeta: metav1.ObjectMeta{
					Name:      stackName,
					Namespace: ssc.StackSet.Namespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: stackset.APIVersion,
							Kind:       stackset.Kind,
							Name:       stackset.Name,
							UID:        stackset.UID,
						},
					},
					Labels: mergeLabels(
						map[string]string{StacksetHeritageLabelKey: stackset.Name},
						stackset.Labels,
						map[string]string{StackVersionLabelKey: stackVersion}),
					Annotations: stackset.Spec.StackTemplate.Annotations,
				},
				Spec: zv1.StackSpec{
					Replicas:                stackset.Spec.StackTemplate.Spec.Replicas,
					HorizontalPodAutoscaler: stackset.Spec.StackTemplate.Spec.HorizontalPodAutoscaler,
					Service:                 service,
					PodTemplate:             stackset.Spec.StackTemplate.Spec.PodTemplate,
					Autoscaler:              stackset.Spec.StackTemplate.Spec.Autoscaler,
				},
			},
		}, stackVersion
	}

	return nil, ""
}

// MarkExpiredStacks marks stacks that should be deleted
func (ssc *StackSetContainer) MarkExpiredStacks() {
	historyLimit := defaultStackLifecycleLimit
	if ssc.StackSet.Spec.StackLifecycle.Limit != nil {
		historyLimit = int(*ssc.StackSet.Spec.StackLifecycle.Limit)
	}

	gcCandidates := make([]*StackContainer, 0, len(ssc.StackContainers))

	for _, sc := range ssc.StackContainers {
		// Stacks are considered for cleanup if we don't have an ingress or if the stack is scaled down because of inactivity
		if sc.ingressSpec == nil || sc.ScaledDown() {
			gcCandidates = append(gcCandidates, sc)
		}
	}

	// only garbage collect if history limit is reached
	if len(gcCandidates) <= historyLimit {
		return
	}

	// sort candidates by oldest
	sort.Slice(gcCandidates, func(i, j int) bool {
		// TODO: maybe we use noTrafficSince instead of CreationTimeStamp to decide oldest
		return gcCandidates[i].Stack.CreationTimestamp.Time.Before(gcCandidates[j].Stack.CreationTimestamp.Time)
	})

	excessStacks := len(gcCandidates) - historyLimit
	for _, sc := range gcCandidates[:excessStacks] {
		sc.PendingRemoval = true
	}
}

func (ssc *StackSetContainer) GenerateIngress() (*extensions.Ingress, error) {
	stackset := ssc.StackSet
	if stackset.Spec.Ingress == nil {
		return nil, nil
	}

	labels := mergeLabels(
		map[string]string{StacksetHeritageLabelKey: stackset.Name},
		stackset.Labels,
	)

	result := &extensions.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        stackset.Name,
			Namespace:   stackset.Namespace,
			Labels:      labels,
			Annotations: mergeLabels(stackset.Spec.Ingress.Annotations),
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: stackset.APIVersion,
					Kind:       stackset.Kind,
					Name:       stackset.Name,
					UID:        stackset.UID,
				},
			},
		},
		Spec: extensions.IngressSpec{
			Rules: make([]extensions.IngressRule, 0),
		},
	}

	rule := extensions.IngressRule{
		IngressRuleValue: extensions.IngressRuleValue{
			HTTP: &extensions.HTTPIngressRuleValue{
				Paths: make([]extensions.HTTPIngressPath, 0),
			},
		},
	}

	actualWeights := make(map[string]float64)
	desiredWeights := make(map[string]float64)

	for _, sc := range ssc.StackContainers {
		if sc.actualTrafficWeight > 0 {
			actualWeights[sc.Name()] = sc.actualTrafficWeight

			rule.IngressRuleValue.HTTP.Paths = append(rule.IngressRuleValue.HTTP.Paths, extensions.HTTPIngressPath{
				Path: stackset.Spec.Ingress.Path,
				Backend: extensions.IngressBackend{
					ServiceName: sc.Name(),
					ServicePort: stackset.Spec.Ingress.BackendPort,
				},
			})
		}
		if sc.desiredTrafficWeight > 0 {
			desiredWeights[sc.Name()] = sc.desiredTrafficWeight
		}
	}

	if len(rule.IngressRuleValue.HTTP.Paths) == 0 {
		return nil, errNoPaths
	}

	// sort backends by name to have a consistent generated ingress resource.
	sort.Slice(rule.IngressRuleValue.HTTP.Paths, func(i, j int) bool {
		return rule.IngressRuleValue.HTTP.Paths[i].Backend.ServiceName < rule.IngressRuleValue.HTTP.Paths[j].Backend.ServiceName
	})

	// create rule per hostname
	for _, host := range stackset.Spec.Ingress.Hosts {
		r := rule
		r.Host = host
		result.Spec.Rules = append(result.Spec.Rules, r)
	}

	actualWeightsData, err := json.Marshal(&actualWeights)
	if err != nil {
		return nil, err
	}

	desiredWeightData, err := json.Marshal(&desiredWeights)
	if err != nil {
		return nil, err
	}

	result.Annotations[backendWeightsAnnotationKey] = string(actualWeightsData)
	result.Annotations[stackTrafficWeightsAnnotationKey] = string(desiredWeightData)

	return result, nil
}

func (ssc *StackSetContainer) GenerateStackSetStatus() *zv1.StackSetStatus {
	result := &zv1.StackSetStatus{
		Stacks:               0,
		ReadyStacks:          0,
		StacksWithTraffic:    0,
		ObservedStackVersion: ssc.StackSet.Status.ObservedStackVersion,
	}

	for _, sc := range ssc.StackContainers {
		if sc.PendingRemoval {
			continue
		}

		result.Stacks += 1
		if sc.HasTraffic() {
			result.StacksWithTraffic += 1
		}
		if sc.IsReady() {
			result.ReadyStacks += 1
		}
	}
	return result
}
