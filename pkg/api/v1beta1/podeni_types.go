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

// PodENISpec defines the desired state of PodENI
type PodENISpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	PodIPInfo   PodIP  `json:"podIPInfo"`
	ENI         VPCENI `json:"eni"`
	NodeIP      string `json:"nodeIP,omitempty"`
	ServiceCidr string `json:"serviceCidr,omitempty"`
}
type PodIP struct {
	IPAddress string `json:"ipAddress"`
	SubnetID  string `json:"subnetID"`
}
type VPCENI struct {
	ID             string   `json:"id,omitempty"`
	Name           string   `json:"name,omitempty"`
	Description    string   `json:"description,omitempty"`
	IPAddress      string   `json:"ipAddress,omitempty"`
	MACAddress     string   `json:"macAddress,omitempty"`
	SecurityGroups []string `json:"securityGroups,omitempty"`
	PodCidr        string   `json:"podCidr,omitempty"`
	ServiceCidr    string   `json:"serviceCidr,omitempty"`
	GatewayIP      string   `json:"gatewayIP,omitempty"`
}

type PhaseStr string

// podeni exclusive mode     |    podeni multieniip mode
// pod create                |    pod create
//
//	                         |
//		|                    |        |
//		| podENI create      |        | podENI create
//		|                    |        |
//	                         |
//
// ENIPhaseInitial           |    ENIPhaseInitial
//
//	                         |
//		|                    |        |
//		| allocate eni       |        |  allocate eni secondary ip
//		|                    |        |
//	                         |
//
// ENIPhaseCreated           |    ENIPhaseIPAllocated
//
//		                     |
//		|   	             |        |
//		|       	         |        |  podeni delete
//		|   bind eni 	     |        |
//		                     |
//	                         |
//
// ENIPhaseBind              |    ENIPhaseDeleting
//
//	|                        |         |
//	|  podEni delete         |         |  release eni secondary ip
//	|                        |         |
//
// ENIPhaseDeleting          |    del podENI
//
//	| eni delete
//
// del podENI
const (
	// ENIPhaseInitial the status when pod first created, eni has been created
	ENIPhaseInitial = ""
	// ENIPhaseCreated the status ENI has been create
	ENIPhaseCreated = "Created"
	// ENIPhaseBind the status ENI is bind to ECS
	ENIPhaseBind = "Bind"
	// ENIPhaseDeleting the status when CR is removing
	ENIPhaseDeleting = "Deleting"
	// ENIPhaseIPAllocated when secondary ip allocated
	ENIPhaseIPAllocated = "Allocated"
)

// PodENIStatus defines the observed state of PodENI
type PodENIStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	Phase    PhaseStr `json:"phase,omitempty"`
	ECSID    string   `json:"ecsId,omitempty"`
	WorkLoad string   `json:"workLoad,omitempty"`
	NodeName string   `json:"nodeName,omitempty"`
	ENIID    string   `json:"eniID,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="ENIID",type="string",JSONPath=".status.eniID"
// +kubebuilder:printcolumn:name="Node",type="string",JSONPath=".status.nodeName"
// +kubebuilder:printcolumn:name="ENIIP",type="string",JSONPath=".spec.eni.ipAddress"
// +kubebuilder:printcolumn:name="PodIP",type="string",JSONPath=".spec.podIPInfo.ipAddress"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// PodENI is the Schema for the podenis API
type PodENI struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PodENISpec   `json:"spec,omitempty"`
	Status PodENIStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// PodENIList contains a list of PodENI
type PodENIList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodENI `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PodENI{}, &PodENIList{})
}
