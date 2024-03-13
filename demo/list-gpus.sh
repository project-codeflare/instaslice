#!/bin/sh

for NODE in $NODES; do
echo "\033[0;35m$NODE:\033[0m"
ssh $NODE nvidia-smi -L
done
