# InstaSlice

InstaSlice facilitates the use of [Dynamic Resource
Allocation](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
(DRA) on Kubernetes clusters for GPU sharing.

For its initial release, InstaSlice facilitates the allocation of [MIG
slices](https://www.nvidia.com/en-us/technologies/multi-instance-gpu/) on
[NVIDIA A100 GPUs](https://www.nvidia.com/en-us/data-center/a100/). InstaSlice
makes it possible to deploy pods with MIG slice requirements expressed as
[extended
resources](https://kubernetes.io/docs/tasks/configure-pod-container/extended-resource/)
to a DRA-enabled cluster. In particular, it enables cluster administrators to
transparently replace [MIG
manager](https://catalog.ngc.nvidia.com/orgs/nvidia/teams/cloud-native/containers/k8s-mig-manager)
from [NVIDIA GPU
operator](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/index.html)
with [NVIDIA DRA driver](https://github.com/NVIDIA/k8s-dra-driver) without
requiring changes to pod specs.

See this [demonstration](demo) for a detailed comparison of MIG slicing using MIG manager
vs. DRA driver vs. InstaSlice.

## Description

InstaSlice implements a mutating webhook for pods that automatically rewrites
resource limits on containers into DRA [resource
claims](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/#api).
For instance, InstaSlice rewrites at creation time the following pod spec:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: sample
spec:
  restartPolicy: Never
  containers:
  - name: busybox
    image: quay.io/project-codeflare/busybox:1.36
    command: ["sh", "-c", "sleep 5"]
    resources:
      limits:
        nvidia.com/mig-1g.5gb: 1
```
into the following pod spec:
```yaml
apiVersion: v1
kind: Pod
metadata:
  name: sample
spec:
  containers:
  restartPolicy: Never
  containers:
  - name: busybox
    image: quay.io/project-codeflare/busybox:1.36
    command: ["sh", "-c", "sleep 5"]
    resources:
      claims:
      - name: ae9a7e7e-e955-4870-859c-12b83927b2bd
  resourceClaims:
  - name: ae9a7e7e-e955-4870-859c-12b83927b2bd
    source:
      resourceClaimTemplateName: mig-1g.5gb
```

The latter spec assumes the following resource claim templates and parameters
are already deployed to the pod namespace:
```yaml
apiVersion: gpu.resource.nvidia.com/v1alpha1
kind: MigDeviceClaimParameters
metadata:
  name: mig-1g.5gb
spec:
  profile: 1g.5gb
---
apiVersion: resource.k8s.io/v1alpha2
kind: ResourceClaimTemplate
metadata:
  name: mig-1g.5gb
spec:
  spec:
    resourceClassName: gpu.nvidia.com
    parametersRef:
      apiGroup: gpu.resource.nvidia.com
      kind: MigDeviceClaimParameters
      name: mig-1g.5gb
```
The deployment instructions below cover this prerequisite.

## Getting started

### Configuring a Kubernetes cluster

InstaSlice assumes a DRA-enabled Kubernetes cluster. It has been tested against
Kubernetes v1.27.

For development or testing purposes, InstaSlice can run on a cluster without
GPUs with a minimal configuration (option 1). In order run pods on MIG slices, a
GPU-enabled, DRA-enabled cluster running the NVIDIA DRA driver is necessary
(option 2).

#### Option 1: test cluster without GPUs

A cluster capable of running InstaSlice can be obtained using
[kind](https://kind.sigs.k8s.io) v0.19 with the provided cluster
[configuration](hack/kind-config.yaml).
```sh
kind create cluster --config hack/kind-config.yaml
```

InstaSlice assumes CRDs from the [NDIVIA DRA
driver](https://github.com/NVIDIA/k8s-dra-driver) are installed on the cluster:
```sh
kubectl apply -f https://raw.githubusercontent.com/NVIDIA/k8s-dra-driver/b6c7aae2b87d857668f417689462da752090406f/deployments/helm/k8s-dra-driver/crds/gpu.resource.nvidia.com_migdeviceclaimparameters.yaml
```

On such a cluster, InstaSlice will be able to rewrite pod specs, but of course
the cluster will be unable to satisfy GPU resource claims. Pods will remain
forever pending.

#### Option 2: GPU-enabled cluster

In order to dynamically create and destroy MIG slices on NVIDIA GPUs, a
GPU-enabled, DRA-enabled cluster running the NVIDIA DRA driver is necessary.
Please refer to
[https://github.com/NVIDIA/k8s-dra-driver](https://github.com/NVIDIA/k8s-dra-driver/tree/b6c7aae2b87d857668f417689462da752090406f)
for further instructions. Please note that InstaSlice has been developed and
tested against commit `b6c7aae` of this driver.

### Deploying cert-manager

InstaSlice assumes [cert-manager](https://github.com/cert-manager/cert-manager)
is deployed on the cluster:
```sh
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.14.3/cert-manager.yaml
```

### Building InstaSlice

A prebuilt InstaSlice image is available from
[quay.io/ibm/instaslice](https://quay.io/repository/ibm/instaslice).

To build and push an InstaSlice image run:
```sh
make docker-build docker-push IMG=<some-registry>/instaslice:<some-tag>
```

Alternatively, to build and push a multi-architecture InstaSlice image run:
```sh
make docker-buildx IMG=<some-registry>/instaslice:<some-tag>
```

## Running InstaSlice on the cluster

To deploy InstaSlice on the Kubernetes cluster, run the prebuilt image or your
own by replacing the image name below:
```sh
make deploy IMG=quay.io/ibm/instaslice:latest
```

InstaSlice relies on preconfigured [resource claim
templates](hack/mig-profiles.yaml). These templates must be deployed to each
namespace where pods using InstaSlice will be deployed.

To deploy the templates to a given namespace run:
```sh
kubectl apply -f hack/mig-profiles.yaml --namespace <some-namespace>
```

### Running an example pod

To deploy an [example pod](samples/sample.yaml) on the cluster run:
```sh
kubectl apply -f samples/sample.yaml
```

Check the resulting pod spec using:
```sh
kubectl get -o yaml pod sample
```

Delete the pod with:
```sh
kubectl delete -f samples/sample.yaml
```

### Uninstalling InstaSlice from the cluster

To uninstall InstaSlice from the cluster run:
```sh
make undeploy
```

## License

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

