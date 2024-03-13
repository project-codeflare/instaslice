/*
Copyright 2024 IBM Corporation.

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

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

//+kubebuilder:webhook:path=/mutate-v1-pod,mutating=true,failurePolicy=fail,sideEffects=None,groups="",resources=pods,verbs=create;update,versions=v1,name=instaslice.codeflare.dev,admissionReviewVersions=v1

type PodAnnotator struct {
	Client  client.Client
	Decoder *admission.Decoder
}

const nvidiaPrefix = "nvidia.com/"
const migPrefix = nvidiaPrefix + "mig-"

func appendRC(claims []v1.ResourceClaim, name string) []v1.ResourceClaim {
	for _, c := range claims {
		if c.Name == name {
			return claims
		}
	}
	return append(claims, v1.ResourceClaim{Name: name})
}

func appendPRC(claims []v1.PodResourceClaim, name string, source v1.ClaimSource) []v1.PodResourceClaim {
	for _, c := range claims {
		if c.Name == name {
			return claims
		}
	}
	return append(claims, v1.PodResourceClaim{Name: name, Source: source})
}

func (a *PodAnnotator) Handle(ctx context.Context, req admission.Request) admission.Response {
	pod := &v1.Pod{}
	err := a.Decoder.Decode(req, pod)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]
		for resourceName, quantity := range container.Resources.Limits {
			name := string(resourceName)
			if strings.HasPrefix(name, migPrefix) {
				count, ok := quantity.AsInt64()
				if !ok || count < 1 || count > 7 {
					return admission.Errored(http.StatusBadRequest, fmt.Errorf("quantity for resource %v must an integer between 1 and 7", name))
				}
				templateName := name[len(nvidiaPrefix):]
				for j := 0; j < int(count); j++ {
					claimName := uuid.New().String()
					container.Resources.Claims = appendRC(container.Resources.Claims, claimName)
					pod.Spec.ResourceClaims = appendPRC(pod.Spec.ResourceClaims, claimName, v1.ClaimSource{ResourceClaimTemplateName: &templateName})
				}
				delete(container.Resources.Requests, resourceName)
				delete(container.Resources.Limits, resourceName)
			}
		}
	}
	marshaledPod, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledPod)
}
