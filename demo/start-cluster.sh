#!/bin/sh

set -e

log () {
  echo "\n\033[0;31m$1\033[0m\n"
}

log "NODES=$NODES"

HOSTNAME=$(hostname)

log "Creating head node on $HOSTNAME"

sudo kubeadm init --skip-token-print --config clusterconfig.yaml
sudo cp /etc/kubernetes/admin.conf $HOME/.kube/config

sed -e "s/kube-apiserver/$HOSTNAME/" clusterconfig.yaml > /tmp/joinconfig.yaml

for NODE in $NODES; do
if [ "$NODE" != $HOSTNAME ]; then
log "Joining worker node on $NODE"
scp /tmp/joinconfig.yaml $NODE:
ssh $NODE sudo kubeadm join --config joinconfig.yaml
fi
done

log "Labeling and tainting nodes"

kubectl taint nodes -l node-role.kubernetes.io/control-plane node-role.kubernetes.io/control-plane:NoSchedule-
kubectl label node -l node-role.kubernetes.io/control-plane --overwrite nvidia.com/dra.controller=true
kubectl label node --all --overwrite nvidia.com/dra.kubelet-plugin=true

log "Installing calico"

kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.25.0/manifests/calico.yaml

log "Installing cert-manager"

kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.14.3/cert-manager.yaml

log "Enabling MIG"

for NODE in $NODES; do
echo "\033[0;35m$NODE:\033[0m"
ssh $NODE sudo nvidia-smi -mig 1
ssh $NODE sudo nvidia-smi mig -dci || true
ssh $NODE sudo nvidia-smi mig -dgi || true
done

log "Listing nodes"

kubectl get nodes

log "Listing GPUs"

./list-gpus.sh
