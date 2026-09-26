# Disposable single-node K3s for the Brine kubelet tiers, sourced by
# hack/test-run-kubelet and hack/test-review-kubelet. Only for a privileged CI
# task that owns its Docker daemon; never point it at a real cluster.
#
# The caller sets: work (a private temp dir), cluster and runner (container
# names), and calls dk3s_cleanup from its EXIT trap.

docker_pid=""

dk3s_cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then
    docker logs --tail 100 "$cluster" 2>&1 || true
    docker exec "$cluster" kubectl get nodes,pods -A -o wide 2>&1 || true
  fi
  docker rm -f "$runner" "$cluster" >/dev/null 2>&1 || true
  if [ -n "$docker_pid" ]; then
    kill "$docker_pid" 2>/dev/null || true
    wait "$docker_pid" 2>/dev/null || true
  fi
  rm -rf "$work"
}

# dk3s_start_dockerd: this task owns its Docker daemon and storage directory.
dk3s_start_dockerd() {
  mkdir -p "$work/docker"
  dockerd --host=unix:///var/run/docker.sock --data-root="$work/docker" --mtu=1450 --insecure-registry=registry.home --log-level=warn >"$work/docker.log" 2>&1 &
  docker_pid=$!
  for attempt in $(seq 1 60); do
    if docker info >/dev/null 2>&1; then break; fi
    sleep 1
  done
  docker info >/dev/null
}

# dk3s_start_cluster <k3s image> <host shared dir> <node mount path> <label>
# All networks are inside this task Pod. A distinct Pod/service CIDR avoids the
# outer cluster's routes. The shared directory is mounted at the same path in
# the node and the runner, so a node-local daemon's hostPaths agree.
dk3s_start_cluster() {
  docker run -d --name "$cluster" --privileged --cgroupns=host --network=host \
    --tmpfs /run --tmpfs /var/run \
    -v "$2:$3" \
    "$1" server --disable=traefik --disable=metrics-server \
    --snapshotter=native --tls-san=127.0.0.1 \
    --cluster-cidr=10.250.0.0/16 --service-cidr=10.251.0.0/16
  nodes=""
  for attempt in $(seq 1 120); do
    # kubectl succeeds on an empty list while the kubelet is still registering.
    nodes=$(docker exec "$cluster" kubectl get node -o name 2>/dev/null) || nodes=""
    if [ -n "$nodes" ]; then break; fi
    sleep 2
  done
  if [ -z "$nodes" ]; then
    echo 'The disposable kubelet did not register a node.' >&2
    exit 1
  fi
  docker exec "$cluster" kubectl wait --for=condition=Ready node --all --timeout=120s
  # The steps refuse any node that does not carry this owner marker.
  docker exec "$cluster" kubectl label node --all "$4"
  docker cp "$cluster:/etc/rancher/k3s/k3s.yaml" "$work/kubeconfig"
  chmod 600 "$work/kubeconfig"
}

# dk3s_import_image <image> <tar name>: make a local image available to the node.
dk3s_import_image() {
  docker save "$1" -o "$work/$2"
  docker cp "$work/$2" "$cluster:/tmp/$2"
  docker exec "$cluster" ctr -n=k8s.io images import --all-platforms "/tmp/$2"
}
