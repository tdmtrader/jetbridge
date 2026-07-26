# Cluster observations — `theborg`, namespace `cicd`

Everything below was collected between 2026-04-21 and 2026-04-23 while chasing
the `nodes "100.68.228.107" not found` failures. It is what we looked at, not
what we concluded.

---

## 1. There is no node by that name, and there never was

```
$ kubectl get nodes -o wide
NAME      STATUS   ROLES           AGE    VERSION   INTERNAL-IP     EXTERNAL-IP
theborg   Ready    control-plane   214d   v1.32.3   192.168.1.42    <none>
```

Single node. Its name is `theborg`; its InternalIP is `192.168.1.42`. Neither
is `100.68.228.107`.

```
$ kubectl get node 100.68.228.107
Error from server (NotFound): nodes "100.68.228.107" not found
```

Same error the build prints, which at least confirms the message is a plain
Nodes-API 404 and not something being reformatted on the way out.

## 2. `100.68.228.107` is the artifact-daemon pod

```
$ kubectl -n cicd get pods -o wide
NAME                             READY   STATUS    IP                NODE
concourse-web-6d4c9f8b7d-2xk4v   1/1     Running   100.68.228.31     theborg
concourse-artifact-daemon-p9wzt  1/1     Running   100.68.228.107    theborg
concourse-db-0                   1/1     Running   100.68.228.14     theborg
```

`100.68.224.0/20` is the pod CIDR on this cluster. So the address in the error
is a **pod** IP — specifically the artifact-daemon DaemonSet pod's. It is not a
node address of any kind, and it is not the node's InternalIP either.

The daemon itself is healthy and it does have the cache. From inside the web
pod (`rc-3178` is the cache key for the version build #418 was reusing):

```
$ curl -sI http://100.68.228.107:7780/resource-caches/rc-3178
HTTP/1.1 200 OK
Content-Length: 0

$ curl -s http://100.68.228.107:7780/artifacts/steps/rc-3178 | tar tf - | head -3
./
./ci/
./ci/scripts/
```

So the cached data is there, on a daemon we can reach, at the address that
appears in the error. The bytes are fine. Something is just asking for them the
wrong way.

## 3. The web pod can read nodes

Ruled out as a cause — the error is a genuine 404 for a name that does not
exist, not a 403:

```
$ kubectl auth can-i get nodes --as=system:serviceaccount:cicd:concourse-web
yes
```

(We checked because this ClusterRole has been reverted by ArgoCD before. It is
intact right now.)

## 4. Timeline

| When | What |
|---|---|
| ~1 Apr | Resource-cache reuse started actually working on this cluster — before that we basically never got a hit. Builds were fine throughout. |
| 18 Apr | Deployed the "route artifact reads through the DaemonSet" work. |
| 19 Apr | First failures. Nobody connected them at the time — they looked like a flake. |
| 21 Apr | Noticed every failure has the same IP and that the IP is the daemon pod. |
| 22 Apr | Established the cache-hit correlation (below). |
| 23 Apr | Filed. |

## 5. The correlation, stated precisely

Over 34 builds across four pipelines, all before the daemon restart described
below:

- 11 builds printed `INFO: found existing resource cache`. **All 11 errored**,
  every one of them with `nodes "100.68.228.107" not found`.
- 23 builds did a real fetch. **All 23 passed.**

Two things we tried that did **not** change anything:

- Restarting `web` (`kubectl rollout restart deploy/concourse-web`). The next
  cache-hit build failed exactly the same way. Whatever is wrong is not
  something a fresh process clears.
- Restarting the artifact-daemon DaemonSet. The pod came back with a
  **different** IP (`100.68.228.119`) and the next cache-hit build failed with
  `nodes "100.68.228.119" not found` — the new address, immediately, with no
  restart of `web` in between.

One thing that did:

- `rm -rf /var/concourse/artifacts/steps/rc-*` on the node. The next build did a
  real fetch and passed; the build after it hit the (re-populated) cache and
  failed again.

## 6. What we have not been able to do

- Reproduce it in the K3s integration suite. Those runs are single-node too but
  we have not got a cache hit to occur inside a suite run.
- Get anything useful out of `--log-level=debug` on web. The failing request
  never reaches the daemon at all, so there is nothing on the daemon side, and
  the web side just shows the step erroring.
