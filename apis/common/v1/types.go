/*
Copyright 2025 The Crossplane Authors.

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

package v1

import (
	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Available returns a condition that indicates the resource is available.
func Available() xpv1.Condition {
	return xpv1.Condition{
		Type:               xpv1.TypeReady,
		Status:             "True",
		LastTransitionTime: metav1.Now(),
		Reason:             "Available",
	}
}

// Creating returns a condition that indicates the resource is being created.
func Creating() xpv1.Condition {
	return xpv1.Condition{
		Type:               xpv1.TypeReady,
		Status:             "Unknown",
		LastTransitionTime: metav1.Now(),
		Reason:             "Creating",
	}
}

// Deleting returns a condition that indicates the resource is being deleted.
func Deleting() xpv1.Condition {
	return xpv1.Condition{
		Type:               xpv1.TypeReady,
		Status:             "Unknown",
		LastTransitionTime: metav1.Now(),
		Reason:             "Deleting",
	}
}
