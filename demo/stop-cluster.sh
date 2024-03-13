#!/bin/sh

for NODE in $NODES; do
ssh $NODE sudo kubeadm reset -f
ssh $NODE sudo nvidia-smi -mig 1
ssh $NODE sudo nvidia-smi mig -dci
ssh $NODE sudo nvidia-smi mig -dgi
ssh $NODE 'sudo ctr -n k8s.io c rm $(sudo ctr -n k8s.io c ls -q)'
done

