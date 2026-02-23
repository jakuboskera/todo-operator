package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TaskSpec defines the desired state of Task
type TaskSpec struct {
	// Text is the task description
	// +kubebuilder:validation:MinLength=1
	Text string `json:"text"`

	// IsDone indicates whether the task is completed
	// +optional
	IsDone bool `json:"isDone,omitempty"`

	// EndpointRef refers to a TodoEndpoint in the same namespace.
	// The operator uses this endpoint to communicate with the Todo API instance.
	EndpointRef LocalObjectReference `json:"endpointRef"`
}

// LocalObjectReference refers to an object in the same namespace.
type LocalObjectReference struct {
	// Name is the name of the object in the same namespace
	Name string `json:"name"`
}

// ExternalRef identifies a task on a specific Todo API instance.
// The ID is only meaningful in the context of the named endpoint.
type ExternalRef struct {
	// EndpointRef is the name of the TodoEndpoint where the task was created.
	EndpointRef string `json:"endpointRef"`
	// ID is the task ID assigned by that endpoint's API.
	ID int `json:"id"`
}

// TaskStatus defines the observed state of Task
type TaskStatus struct {
	// ExternalRef is set once the task has been successfully created on an
	// external Todo API instance. It is nil until then, and is cleared
	// whenever the endpoint reference changes so the task can be recreated
	// on the new endpoint.
	// +optional
	ExternalRef *ExternalRef `json:"externalRef,omitempty"`

	// ObservedGeneration tracks the last reconciled generation
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Text",type=string,JSONPath=`.spec.text`
// +kubebuilder:printcolumn:name="Done",type=boolean,JSONPath=`.spec.isDone`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.spec.endpointRef.name`
// +kubebuilder:printcolumn:name="ExternalID",type=integer,JSONPath=`.status.externalRef.id`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Task is the Schema for the tasks API
type Task struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TaskSpec   `json:"spec"`
	Status TaskStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TaskList contains a list of Task
type TaskList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Task `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Task{}, &TaskList{})
}
