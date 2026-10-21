/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// PodNodeSpec defines the desired state of PodENI
type PodNodeSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	NodeName string `json:"nodeName,omitempty"`
	EniNums  int    `json:"eniNums,omitempty"`
}

type PodNodePhase string

// node add
//
//	|
//	|
//	| create podnode
//	|
//
// PodNodePhaseInitial
//
//	|
//	| create enis
//	|
//	|
//
// PodNodePhaseCreated
//
//	|
//	| delete podnode and enis
//	|
//
// PodNodePhaseDeleting
const (
	// the status when node first created, enis needs to create
	PodNodePhaseInitial = ""
	// the status ENI has been create and bind to node
	PodNodePhaseFinished = "Finished"
	// the status when CR is removing
	PodNodePhaseDeleting = "Deleting"
)

// PodNodeStatus defines the observed state of PodENI
type PodNodeStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	NetworkInterfaces []*NetworkInterface `json:"networkInterfaces,omitempty"`
	Phase             PodNodePhase        `json:"phase,omitempty"`
}

// +kubebuilder:resource:scope=Cluster
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// PodNode is the Schema for the podenis API
type PodNode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PodNodeSpec   `json:"spec,omitempty"`
	Status PodNodeStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// PodNodeList contains a list of PodNode
type PodNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodNode `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PodNode{}, &PodNodeList{})
}

type ENIType string

// status for ENIType
const (
	ENITypePrimary   ENIType = "Primary"
	ENITypeSecondary ENIType = "Secondary"
)

type NetworkInterface struct {
	// +kubebuilder:validation:Required
	ID string `json:"id"`
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	Status string `json:"status"`
	// +kubebuilder:validation:Required
	MacAddress string `json:"macAddress"`
	// +kubebuilder:validation:Required
	SecurityGroups []string `json:"securityGroups"`
	// +kubebuilder:validation:Required
	IPAddress string `json:"ipAddress"`
	// +kubebuilder:validation:Optional
	NetworkInterfaceType ENIType `json:"networkInterfaceType"`
	// +kubebuilder:validation:Optional
	Subnet *SubnetBrief `json:"subnet"`
	// +kubebuilder:validation:Optional
	IsBind bool `json:"isBind"`
	// +kubebuilder:validation:Optional
	Allocated int `json:"allocated"`
}
type SubnetBrief struct {
	// +kubebuilder:validation:Optional
	ID string `json:"id"`
	// +kubebuilder:validation:Optional
	OSID string `json:"osID"`
	// +kubebuilder:validation:Optional
	Name string `json:"name"`
	// +kubebuilder:validation:Optional
	CIDR string `json:"cidr"`
	// +kubebuilder:validation:Optional
	GatewayIP string `json:"gatewayIP"`
}
