export NODES="netsres61 netsres62"

### GPU operator demo

./start-cluster.sh

./list-allocatable.sh

# label nodes with desired MIG profile

kubectl label node --all --overwrite nvidia.com/mig.config=all-1g.5gb

# install GPU operator

helm install gpu-operator --wait -n gpu-operator --create-namespace \
  nvidia/gpu-operator --set toolkit.enabled=false \
  --set driver.enabled=false --set mig.strategy=mixed

kubectl get pods -n gpu-operator -w

# wait until GPU operator is ready

./list-gpus.sh

./list-allocatable.sh

# deploy example pods

kubectl apply -f vectoradd-limits.yaml

kubectl get pods

kubectl logs small

kubectl get pods large -o yaml

kubectl delete pods --all

# switch MIG profile

kubectl label node --all --overwrite nvidia.com/mig.config=all-balanced

kubectl get pods -n gpu-operator -w

# wait until GPU operator is ready

./list-gpus.sh

./list-allocatable.sh

# deploy example pods

kubectl apply -f vectoradd-limits.yaml

kubectl get pods

./stop-cluster.sh


### DRA demo

./start-cluster.sh

# install DRA

helm install dra-driver --wait -n dra-driver --create-namespace \
  $HOME/k8s-dra-driver/deployments/helm/k8s-dra-driver

./list-gpus.sh

# deploy example pods using resource claims

kubectl apply -f vectoradd-claims.yaml

kubectl get pods,resourceclaims

kubectl logs small

kubectl delete pods --all

# deploy example pods using resource limits

kubectl apply -f vectoradd-limits.yaml

kubectl get pods

kubectl get pods large -o yaml

kubectl delete pods --all

# install InstaSlice

kubectl apply -f instaslice.yaml

# wait until InstaSlice is ready

# deploy example pods using resource limits

kubectl apply -f vectoradd-limits.yaml

kubectl get pods,resourceclaims

kubectl logs small
