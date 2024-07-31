#!/bin/bash

# Create the Kind cluster
kind create cluster --config - <<EOF
apiVersion: kind.x-k8s.io/v1alpha4
kind: Cluster
nodes:
- role: control-plane
  image: kindest/node:v1.27.3@sha256:3966ac761ae0136263ffdb6cfd4db23ef8a83cba8a463690e98317add2c9ba72
  # required for GPU workaround
  extraMounts:
    - hostPath: /dev/null
      containerPath: /var/run/nvidia-container-devices/all
EOF

# Check if Kind cluster creation was successful
if [ $? -ne 0 ]; then
    echo "Failed to create Kind cluster"
    exit 1
fi

# Create symlink in the control-plane container
docker exec -ti kind-control-plane ln -s /sbin/ldconfig /sbin/ldconfig.real

# Check if symlink creation was successful
if [ $? -ne 0 ]; then
    echo "Failed to create symlink"
    exit 1
fi

# Unmount the nvidia devices in the control-plane container
docker exec -ti kind-control-plane umount -R /proc/driver/nvidia

# Check if unmounting was successful
if [ $? -ne 0 ]; then
    echo "Failed to unmount nvidia devices"
    exit 1
fi

# Label all nodes with the specified label
kubectl label node --all --overwrite nvidia.com/mig.config=all-balanced

# Check if labeling was successful
if [ $? -ne 0 ]; then
    echo "Failed to label nodes"
    exit 1
fi

# Install the GPU Operator using Helm with the --wait flag
#--set devicePlugin.enabled=false
helm install --wait --generate-name -n gpu-operator --create-namespace nvidia/gpu-operator --set mig.strategy=mixed --set cdi.enabled=true

# Check if GPU Operator installation was successful
if [ $? -ne 0 ]; then
    echo "Failed to install GPU Operator"
    exit 1
fi

echo "GPU operator installation commands executed successfully"

kubectl apply -f ./deploy/custom-configmapwithprofiles.yaml

kubectl label node --all nvidia.com/device-plugin.config=update-capacity

#for already deployed GPU operator
#To avoid waiting for minutes, for now run the below command manually
#kubectl patch clusterpolicies.nvidia.com/cluster-policy     -n gpu-operator --type merge     -p '{"spec": {"devicePlugin": {"config": {"name": "test"}}}}'

#kubectl label node --all nvidia.com/device-plugin.config=a100-40gb
#for openshift replace gpu-operator with nvidia-gpu-operator
#TODO: configmap name and key should be changed.

exit 0
