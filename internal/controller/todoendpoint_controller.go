package controller

import (
	"context"
	"fmt"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	todov1alpha1 "github.com/jakuboskera/todo-operator/api/v1alpha1"
)

const (
	// endpointRequeueInterval is how often a not-yet-ready endpoint is rechecked.
	endpointRequeueInterval = 15 * time.Second
)

type TodoEndpointReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=todo.jakuboskera.dev,resources=todoendpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=todo.jakuboskera.dev,resources=todoendpoints/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *TodoEndpointReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var endpoint todov1alpha1.TodoEndpoint
	if err := r.Get(ctx, req.NamespacedName, &endpoint); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Helper to update status without repeating the boilerplate below.
	setStatus := func(ready bool, message string) (ctrl.Result, error) {
		endpoint.Status.Ready = ready
		endpoint.Status.Message = message
		if err := r.Status().Update(ctx, &endpoint); err != nil {
			return ctrl.Result{}, err
		}
		if !ready {
			// Keep polling until the endpoint becomes ready.
			return ctrl.Result{RequeueAfter: endpointRequeueInterval}, nil
		}
		return ctrl.Result{}, nil
	}

	// Verify that the referenced Secret exists in the same namespace.
	var secret corev1.Secret
	secretKey := types.NamespacedName{
		Namespace: endpoint.Namespace,
		Name:      endpoint.Spec.APISecretRef.Name,
	}
	if err := r.Get(ctx, secretKey, &secret); err != nil {
		msg := fmt.Sprintf("secret %q not found: %v", endpoint.Spec.APISecretRef.Name, err)
		logger.Info(msg)
		return setStatus(false, msg)
	}

	// Verify that the Secret contains the expected key.
	keyBytes, ok := secret.Data[endpoint.Spec.APISecretRef.Key]
	if !ok {
		msg := fmt.Sprintf("secret %q does not contain key %q", endpoint.Spec.APISecretRef.Name, endpoint.Spec.APISecretRef.Key)
		logger.Info(msg)
		return setStatus(false, msg)
	}

	// Verify that the Todo API is actually reachable and accepts the API key.
	// This prevents Tasks from attempting to create/update records before the
	// API pod is fully started (e.g. when everything is deployed in one Helm release).
	if err := checkAPIReachable(endpoint.Spec.URL, string(keyBytes)); err != nil {
		msg := fmt.Sprintf("API at %q is not ready: %v", endpoint.Spec.URL, err)
		logger.Info(msg)
		return setStatus(false, msg)
	}

	logger.Info("TodoEndpoint is valid", "url", endpoint.Spec.URL)
	return setStatus(true, "")
}

// checkAPIReachable performs a GET /api/v1/tasks/ request to verify that the
// Todo API is up and accepts requests with the given key. It expects HTTP 200.
func checkAPIReachable(baseURL, apiKey string) error {
	httpClient := &http.Client{Timeout: 5 * time.Second}

	req, err := http.NewRequest("GET", baseURL+"/api/v1/tasks/", nil)
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("X-API-KEY", apiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

func (r *TodoEndpointReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&todov1alpha1.TodoEndpoint{}).
		Named("todoendpoint").
		Complete(r)
}
