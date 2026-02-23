package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SecretKeyReference refers to a specific key inside a Secret in the same namespace.
// Follows the same pattern as Kubernetes core (envFrom.secretKeyRef).
type SecretKeyReference struct {
	// Name is the name of the Secret in the same namespace as the TodoEndpoint
	Name string `json:"name"`

	// Key is the key inside the Secret whose value will be used as the API key
	Key string `json:"key"`
}

// TodoEndpointSpec defines the desired state of TodoEndpoint
type TodoEndpointSpec struct {
	// URL is the base address of the Todo API instance, e.g. "http://my-todo.example.com"
	// +kubebuilder:validation:Pattern=`^https?://.+`
	URL string `json:"url"`

	// APISecretRef refers to a Secret in the same namespace that contains the API key
	APISecretRef SecretKeyReference `json:"apiSecretRef"`
}

// TodoEndpointStatus defines the observed state of TodoEndpoint
type TodoEndpointStatus struct {
	// Ready indicates whether the endpoint was successfully validated
	// (Secret exists and contains the expected key)
	// +optional
	Ready bool `json:"ready,omitempty"`

	// Message contains an error description when Ready is false
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="URL",type="string",JSONPath=".spec.url"
// +kubebuilder:printcolumn:name="Ready",type="boolean",JSONPath=".status.ready"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// TodoEndpoint is the Schema for the todoendpoints API
type TodoEndpoint struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TodoEndpointSpec   `json:"spec"`
	Status TodoEndpointStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TodoEndpointList contains a list of TodoEndpoint
type TodoEndpointList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TodoEndpoint `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TodoEndpoint{}, &TodoEndpointList{})
}
