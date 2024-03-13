#!/bin/sh

for NODE in $NODES; do
echo "\033[0;35m$NODE:\033[0m"
kubectl describe node $NODE | awk '/Allocatable:/{flag=1}/System Info:/{flag=0}flag'
done
