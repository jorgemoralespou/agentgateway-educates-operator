package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

// The rate-limit service and its counter store.
//
// Rendered as plain objects rather than installed from a chart: they are static
// infrastructure with no per-workshop variation, and vendoring two more charts
// to express four objects would be more machinery than the problem needs.
//
// The counter store runs without persistence — no PVC, no volume. Counters
// reset on restart, which resets budgets. That is deliberate and acceptable for
// a disposable workshop cluster, and it keeps the "no long-term persistence"
// constraint ADR-0001 exists to protect (ADR-0003).

const (
	ratelimitImage = "envoyproxy/ratelimit:master"
	redisImage     = "redis:7-alpine"

	redisName = "agentgateway-ratelimit-redis"
	redisPort = 6379

	// ratelimitConfigMapName holds the rate-limit service's domain
	// configuration.
	ratelimitConfigMapName = "agentgateway-ratelimit-config"
)

// reconcileRateLimit converges the rate-limit service and its counter store.
func (r *AgentGatewayPlatformReconciler) reconcileRateLimit(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform, namespace string) (bool, ctrl.Result, error) {
	for _, step := range []func(context.Context, *agentgatewayv1alpha1.AgentGatewayPlatform, string) error{
		r.ensureRateLimitConfig,
		r.ensureRedis,
		r.ensureRedisService,
		r.ensureRateLimitDeployment,
		r.ensureRateLimitService,
	} {
		if err := step(ctx, platform, namespace); err != nil {
			setCondition(&platform.Status.Conditions, platform.Generation,
				agentgatewayv1alpha1.ConditionRateLimitReady, metav1.ConditionFalse,
				agentgatewayv1alpha1.ReasonFailed, err.Error())
			platform.Status.Phase = agentgatewayv1alpha1.PlatformFailed
			return false, ctrl.Result{}, r.updateStatus(ctx, platform)
		}
	}

	available, err := r.deploymentAvailable(ctx, namespace, RateLimitServiceName)
	if err != nil {
		return false, ctrl.Result{}, err
	}
	if !available {
		setCondition(&platform.Status.Conditions, platform.Generation,
			agentgatewayv1alpha1.ConditionRateLimitReady, metav1.ConditionFalse,
			agentgatewayv1alpha1.ReasonWaiting,
			"waiting for the rate-limit service Deployment to become available")
		platform.Status.Phase = agentgatewayv1alpha1.PlatformInstalling
		return false, ctrl.Result{RequeueAfter: requeueShort}, r.updateStatus(ctx, platform)
	}

	setCondition(&platform.Status.Conditions, platform.Generation,
		agentgatewayv1alpha1.ConditionRateLimitReady, metav1.ConditionTrue,
		agentgatewayv1alpha1.ReasonReady, "the rate-limit service is available")
	return true, ctrl.Result{}, nil
}

// ensureRateLimitConfig writes the rate-limit service's domain configuration.
//
// The descriptor here must match the one in the policy: the policy names the
// domain and the descriptor key, and the service decides what limit applies.
// The limit itself is per-session and comes from each key's own budget, so this
// config only has to establish the descriptor's existence.
func (r *AgentGatewayPlatformReconciler) ensureRateLimitConfig(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform, namespace string) error {
	config := fmt.Sprintf(`domain: %s
descriptors:
  - key: %s
    rate_limit:
      unit: hour
      requests_per_unit: 1000000
`, RateLimitDomain, metadataKeySession)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ratelimitConfigMapName,
			Namespace: namespace,
			Labels:    map[string]string{ManagedByLabel: ManagedByValue},
		},
		Data: map[string]string{RateLimitDomain + ".yaml": config},
	}
	return r.applyOwned(ctx, platform, cm, func(live client.Object) {
		live.(*corev1.ConfigMap).Data = cm.Data
	})
}

// ensureRedis renders the counter store, deliberately without persistence.
func (r *AgentGatewayPlatformReconciler) ensureRedis(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform, namespace string) error {
	labels := map[string]string{
		ManagedByLabel:           ManagedByValue,
		"app.kubernetes.io/name": redisName,
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      redisName,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptrTo(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: ptrTo(true),
					},
					Containers: []corev1.Container{{
						Name:  "redis",
						Image: redisImage,
						// No volume of any kind: counters are ephemeral by
						// design (ADR-0003).
						Args:  []string{"--save", "", "--appendonly", "no"},
						Ports: []corev1.ContainerPort{{ContainerPort: redisPort}},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptrTo(false),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
				},
			},
		},
	}

	return r.applyOwned(ctx, platform, deploy, func(live client.Object) {
		live.(*appsv1.Deployment).Spec = deploy.Spec
	})
}

func (r *AgentGatewayPlatformReconciler) ensureRedisService(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform, namespace string) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      redisName,
			Namespace: namespace,
			Labels:    map[string]string{ManagedByLabel: ManagedByValue},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app.kubernetes.io/name": redisName},
			Ports: []corev1.ServicePort{{
				Name:       "redis",
				Port:       redisPort,
				TargetPort: intstr.FromInt32(redisPort),
			}},
		},
	}
	return r.applyOwned(ctx, platform, svc, func(live client.Object) {
		l := live.(*corev1.Service)
		l.Spec.Selector = svc.Spec.Selector
		l.Spec.Ports = svc.Spec.Ports
	})
}

// ensureRateLimitDeployment renders the Envoy rate-limit service.
func (r *AgentGatewayPlatformReconciler) ensureRateLimitDeployment(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform, namespace string) error {
	labels := map[string]string{
		ManagedByLabel:           ManagedByValue,
		"app.kubernetes.io/name": RateLimitServiceName,
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      RateLimitServiceName,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptrTo(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: ptrTo(true),
					},
					Containers: []corev1.Container{{
						Name:    "ratelimit",
						Image:   ratelimitImage,
						Command: []string{"/bin/ratelimit"},
						Env: []corev1.EnvVar{
							{Name: "USE_STATSD", Value: "false"},
							{Name: "LOG_LEVEL", Value: "warn"},
							{Name: "REDIS_SOCKET_TYPE", Value: "tcp"},
							{Name: "REDIS_URL", Value: fmt.Sprintf("%s:%d", redisName, redisPort)},
							{Name: "RUNTIME_ROOT", Value: "/data"},
							{Name: "RUNTIME_SUBDIRECTORY", Value: "ratelimit"},
							{Name: "RUNTIME_WATCH_ROOT", Value: "false"},
							{Name: "RUNTIME_IGNOREDOTFILES", Value: "true"},
							// The gRPC port agentgateway sends checks to.
							{Name: "GRPC_PORT", Value: fmt.Sprintf("%d", RateLimitPort)},
						},
						Ports: []corev1.ContainerPort{{
							Name:          "grpc",
							ContainerPort: RateLimitPort,
						}},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "config",
							MountPath: "/data/ratelimit/config",
						}},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptrTo(false),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
					Volumes: []corev1.Volume{{
						Name: "config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: ratelimitConfigMapName,
								},
							},
						},
					}},
				},
			},
		},
	}

	return r.applyOwned(ctx, platform, deploy, func(live client.Object) {
		live.(*appsv1.Deployment).Spec = deploy.Spec
	})
}

func (r *AgentGatewayPlatformReconciler) ensureRateLimitService(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform, namespace string) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      RateLimitServiceName,
			Namespace: namespace,
			Labels:    map[string]string{ManagedByLabel: ManagedByValue},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app.kubernetes.io/name": RateLimitServiceName},
			Ports: []corev1.ServicePort{{
				Name:       "grpc",
				Port:       RateLimitPort,
				TargetPort: intstr.FromInt32(RateLimitPort),
			}},
		},
	}
	return r.applyOwned(ctx, platform, svc, func(live client.Object) {
		l := live.(*corev1.Service)
		l.Spec.Selector = svc.Spec.Selector
		l.Spec.Ports = svc.Spec.Ports
	})
}

// applyOwned creates an object owned by the platform, or updates the live one
// through the given mutation.
//
// Owned by the platform declaration so everything cascades when it is deleted,
// which is what keeps teardown from having to enumerate these by hand.
func (r *AgentGatewayPlatformReconciler) applyOwned(
	ctx context.Context,
	platform *agentgatewayv1alpha1.AgentGatewayPlatform,
	desired client.Object,
	mutate func(live client.Object),
) error {
	if err := controllerutil.SetControllerReference(platform, desired, r.Scheme); err != nil {
		return err
	}

	live, err := newEmptyLike(desired)
	if err != nil {
		return err
	}

	getErr := r.Get(ctx, types.NamespacedName{
		Namespace: desired.GetNamespace(), Name: desired.GetName(),
	}, live)
	if apierrors.IsNotFound(getErr) {
		if err := r.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create %T %s/%s: %w",
				desired, desired.GetNamespace(), desired.GetName(), err)
		}
		return nil
	}
	if getErr != nil {
		return getErr
	}

	mutate(live)
	if err := r.Update(ctx, live); err != nil {
		return fmt.Errorf("update %T %s/%s: %w",
			desired, desired.GetNamespace(), desired.GetName(), err)
	}
	return nil
}

// newEmptyLike returns an empty object of the same type, for a Get.
func newEmptyLike(obj client.Object) (client.Object, error) {
	switch obj.(type) {
	case *corev1.ConfigMap:
		return &corev1.ConfigMap{}, nil
	case *corev1.Service:
		return &corev1.Service{}, nil
	case *appsv1.Deployment:
		return &appsv1.Deployment{}, nil
	default:
		return nil, fmt.Errorf("no empty constructor for %T", obj)
	}
}

func ptrTo[T any](v T) *T { return &v }
