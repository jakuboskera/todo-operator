package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	todov1alpha1 "github.com/jakuboskera/todo-operator/api/v1alpha1"
)

const (
	taskFinalizer   = "todo.jakuboskera.dev/finalizer"
	requeueInterval = 60 * time.Second
)

type TaskReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=todo.jakuboskera.dev,resources=tasks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=todo.jakuboskera.dev,resources=tasks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=todo.jakuboskera.dev,resources=tasks/finalizers,verbs=update

func (r *TaskReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var task todov1alpha1.Task
	if err := r.Get(ctx, req.NamespacedName, &task); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// ========================
	// Handle deletion
	// ========================
	// Deletion is handled before resolving the current endpoint because the
	// endpoint may have been deleted already. The deletion branch resolves the
	// endpoint from ExternalRef (where the task was actually created) independently.
	if !task.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&task, taskFinalizer) {

			if task.Status.ExternalRef != nil {
				logger.Info("Deleting external task",
					"endpoint", task.Status.ExternalRef.EndpointRef,
					"id", task.Status.ExternalRef.ID)

				// Resolve the endpoint where the task actually lives (may differ from
				// the current spec if the endpoint ref was changed before deletion).
				delBase, delKey, resolveErr := r.resolveEndpointByName(ctx, task.Namespace, task.Status.ExternalRef.EndpointRef)
				if resolveErr != nil {
					logger.Info("Endpoint not resolvable during deletion, skipping delete",
						"endpoint", task.Status.ExternalRef.EndpointRef, "error", resolveErr)
				} else {
					if err := deleteTask(delBase, delKey, task.Status.ExternalRef.ID); err != nil {
						logger.Error(err, "failed to delete external task")
						return ctrl.Result{}, err
					}
					logger.Info("External task deleted successfully",
						"endpoint", task.Status.ExternalRef.EndpointRef,
						"id", task.Status.ExternalRef.ID)
				}
			}

			controllerutil.RemoveFinalizer(&task, taskFinalizer)
			if err := r.Update(ctx, &task); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// ========================
	// Ensure finalizer
	// ========================
	if !controllerutil.ContainsFinalizer(&task, taskFinalizer) {
		controllerutil.AddFinalizer(&task, taskFinalizer)
		if err := r.Update(ctx, &task); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Resolve the TodoEndpoint referenced by this Task (the current / desired endpoint).
	// If the endpoint is not yet available (e.g. deployed in the same Helm release),
	// requeue quietly instead of returning an error — returning an error would cause
	// controller-runtime to log it as a failure and apply exponential back-off.
	apiBase, apiKey, err := r.resolveEndpoint(ctx, &task)
	if err != nil {
		logger.Info("TodoEndpoint not yet available, requeueing",
			"endpoint", task.Spec.EndpointRef.Name, "reason", err.Error())
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	// ========================
	// ENDPOINT CHANGE
	// ========================
	// ExternalRef pairs the ID with the endpoint it belongs to, so we can
	// detect when the user points the task at a different instance.
	if task.Status.ExternalRef != nil &&
		task.Status.ExternalRef.EndpointRef != task.Spec.EndpointRef.Name {

		logger.Info("Endpoint reference changed, deleting task from old endpoint",
			"oldEndpoint", task.Status.ExternalRef.EndpointRef,
			"newEndpoint", task.Spec.EndpointRef.Name,
			"id", task.Status.ExternalRef.ID)

		oldAPIBase, oldAPIKey, resolveErr := r.resolveEndpointByName(ctx, task.Namespace, task.Status.ExternalRef.EndpointRef)
		if resolveErr != nil {
			// Old endpoint is gone; log and proceed — task is already unreachable there.
			logger.Info("Old endpoint not resolvable, skipping delete",
				"endpoint", task.Status.ExternalRef.EndpointRef, "error", resolveErr)
		} else {
			if err := deleteTask(oldAPIBase, oldAPIKey, task.Status.ExternalRef.ID); err != nil {
				logger.Error(err, "failed to delete task from old endpoint")
				return ctrl.Result{}, err
			}
			logger.Info("Task deleted from old endpoint",
				"endpoint", task.Status.ExternalRef.EndpointRef,
				"id", task.Status.ExternalRef.ID)
		}

		task.Status.ExternalRef = nil
		task.Status.ObservedGeneration = 0
		if err := r.Status().Update(ctx, &task); err != nil {
			return ctrl.Result{}, err
		}
		// Requeue immediately to fall into the CREATE branch.
		return ctrl.Result{Requeue: true}, nil
	}

	// ========================
	// CREATE
	// ========================
	if task.Status.ExternalRef == nil {
		id, err := createTask(apiBase, apiKey, task.Spec.Text, task.Spec.IsDone)
		if err != nil {
			logger.Error(err, "failed to create external task")
			return ctrl.Result{}, err
		}

		task.Status.ExternalRef = &todov1alpha1.ExternalRef{
			EndpointRef: task.Spec.EndpointRef.Name,
			ID:          id,
		}
		task.Status.ObservedGeneration = task.Generation

		if err := r.Status().Update(ctx, &task); err != nil {
			return ctrl.Result{}, err
		}

		logger.Info("External task created",
			"endpoint", task.Spec.EndpointRef.Name, "id", id)
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}

	// ========================
	// UPDATE
	// ========================
	// We always push the desired state (Spec) to the API on every reconcile loop.
	// This ensures Kubernetes stays the source of truth even if someone modified
	// the task directly in the Todo UI.
	if err := updateTask(apiBase, apiKey, task.Status.ExternalRef.ID, task.Spec.Text, task.Spec.IsDone); err != nil {
		logger.Error(err, "failed to update external task")
		return ctrl.Result{}, err
	}

	task.Status.ObservedGeneration = task.Generation
	if err := r.Status().Update(ctx, &task); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("External task synced",
		"endpoint", task.Status.ExternalRef.EndpointRef,
		"id", task.Status.ExternalRef.ID)

	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// resolveEndpoint looks up the TodoEndpoint referenced by the Task and reads
// the API key from the Secret referenced by the endpoint.
func (r *TaskReconciler) resolveEndpoint(ctx context.Context, task *todov1alpha1.Task) (apiBase, apiKey string, err error) {
	return r.resolveEndpointByName(ctx, task.Namespace, task.Spec.EndpointRef.Name)
}

// resolveEndpointByName resolves a TodoEndpoint by namespace and name.
func (r *TaskReconciler) resolveEndpointByName(ctx context.Context, namespace, name string) (apiBase, apiKey string, err error) {
	var endpoint todov1alpha1.TodoEndpoint
	endpointKey := types.NamespacedName{Namespace: namespace, Name: name}
	if err := r.Get(ctx, endpointKey, &endpoint); err != nil {
		return "", "", fmt.Errorf("todoendpoint %q not found: %w", name, err)
	}

	if !endpoint.Status.Ready {
		return "", "", fmt.Errorf("todoendpoint %q is not ready: %s", endpoint.Name, endpoint.Status.Message)
	}

	var secret corev1.Secret
	secretKey := types.NamespacedName{
		Namespace: endpoint.Namespace,
		Name:      endpoint.Spec.APISecretRef.Name,
	}
	if err := r.Get(ctx, secretKey, &secret); err != nil {
		return "", "", fmt.Errorf("secret %q not found: %w", endpoint.Spec.APISecretRef.Name, err)
	}

	keyBytes, ok := secret.Data[endpoint.Spec.APISecretRef.Key]
	if !ok {
		return "", "", fmt.Errorf("secret %q does not contain key %q", endpoint.Spec.APISecretRef.Name, endpoint.Spec.APISecretRef.Key)
	}

	return endpoint.Spec.URL, string(keyBytes), nil
}

// =====================================
// HTTP CLIENT FUNCTIONS
// =====================================

func createTask(baseURL, apiKey, text string, isDone bool) (int, error) {
	payload := map[string]any{
		"text":    text,
		"is_done": isDone,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequest("POST", baseURL+"/api/v1/tasks/", bytes.NewBuffer(body))
	if err != nil {
		return 0, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-KEY", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 201 {
		return 0, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}

	idFloat, ok := result["id"].(float64)
	if !ok {
		return 0, fmt.Errorf("invalid id returned from API")
	}

	return int(idFloat), nil
}

func updateTask(baseURL, apiKey string, id int, text string, isDone bool) error {
	payload := map[string]any{
		"text":    text,
		"is_done": isDone,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("PUT",
		fmt.Sprintf("%s/api/v1/tasks/%d", baseURL, id),
		bytes.NewBuffer(body),
	)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-KEY", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

// deleteTask removes a task from the API. A 404 response is treated as success
// since the task is already absent — this prevents an infinite retry loop when
// the task was deleted out-of-band before the controller could clean it up.
func deleteTask(baseURL, apiKey string, id int) error {
	req, err := http.NewRequest("DELETE",
		fmt.Sprintf("%s/api/v1/tasks/%d", baseURL, id),
		nil,
	)
	if err != nil {
		return err
	}

	req.Header.Set("X-API-KEY", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 && resp.StatusCode != 404 {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

// =====================================

// tasksForEndpoint maps a TodoEndpoint event to reconcile requests for all
// Tasks in the same namespace that reference it. This ensures tasks are
// immediately re-queued when their endpoint becomes ready.
func (r *TaskReconciler) tasksForEndpoint(ctx context.Context, obj client.Object) []reconcile.Request {
	endpoint, ok := obj.(*todov1alpha1.TodoEndpoint)
	if !ok {
		return nil
	}

	var taskList todov1alpha1.TaskList
	if err := r.List(ctx, &taskList, client.InNamespace(endpoint.Namespace)); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, task := range taskList.Items {
		if task.Spec.EndpointRef.Name == endpoint.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: task.Namespace,
					Name:      task.Name,
				},
			})
		}
	}
	return requests
}

func (r *TaskReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&todov1alpha1.Task{}).
		Watches(
			&todov1alpha1.TodoEndpoint{},
			handler.EnqueueRequestsFromMapFunc(r.tasksForEndpoint),
		).
		Named("task").
		Complete(r)
}
