# Example environments

Ready-to-apply `DevEnvironment` manifests for common development setups. Copy
one, change the names and credentials, and apply it:

```bash
kubectl apply -f examples/postgres-web-stack.yaml
kubectl get devenvironment -w            # wait for Ready
kubectl delete -f examples/postgres-web-stack.yaml
```

| Example | Services | For |
| --- | --- | --- |
| [postgres-web-stack.yaml](postgres-web-stack.yaml) | Postgres, Redis, Adminer | the backing services a typical web app needs |
| [mysql-web-stack.yaml](mysql-web-stack.yaml) | MySQL, Adminer | the same, for a MySQL project |
| [document-store.yaml](document-store.yaml) | MongoDB, Redis | a service built on a document store |
| [message-queue.yaml](message-queue.yaml) | RabbitMQ, Redis | developing producers and workers |
| [observability.yaml](observability.yaml) | Prometheus, Grafana | building a dashboard or an alert |
| [mail-sandbox.yaml](mail-sandbox.yaml) | Mailpit, Memcached | catching outbound mail instead of sending it |

These are recipes to copy. The single canonical sample that ships with a
release lives in [`config/samples/`](../config/samples/).

## Reaching the services

Each service gets a ClusterIP Service named after it, in the environment's
namespace:

```
postgres.<namespace>.svc.cluster.local:5432
```

From outside the cluster, port-forward:

```bash
kubectl -n web-stack port-forward svc/adminer 8080:8080
```

## Things that shaped these files

Worth knowing before you write your own, because each one is a mistake that
looks like a bug later:

- **A service exposes one port.** `message-queue` publishes RabbitMQ's AMQP
  port and not its management UI; `mail-sandbox` publishes Mailpit's web inbox
  and not its SMTP port. The other port still answers on the pod, so
  `port-forward deploy/<name>` reaches it.
- **Set `readinessProbe: {}`.** The empty handler means a TCP check against the
  service's own port. Without a probe, a container counts as ready the instant
  it starts — so `readyServices` claims a database is up before it accepts
  connections. Every service here sets it.
- **Only one service should mount the shared volume.** `storage` is a single
  ReadWriteOnce claim for the whole environment. Two databases mounting it
  would write to the same directory.
- **A non-root image usually cannot write the shared volume.** The operator
  sets no pod `securityContext` or `fsGroup`, so on any provisioner that
  presents the volume root as `root:root 0755` — most CSI drivers, EBS, GCE PD
  — an image running as a non-root user fails to write to it. Postgres, MySQL
  and MongoDB are fine because they start as root and `chown` their data
  directory; Prometheus (`nobody`) and Grafana (uid 472) are not, which is why
  `observability` declares no storage at all. A cluster with a 0777
  StorageClass, such as k3s `local-path`, hides this.
- **A volume nothing mounts stays `Pending` forever** on a cluster whose
  StorageClass binds on first consumer, while the environment still reports
  Ready. If you declare `storage`, give something a `mountPath` —
  `message-queue` and `mail-sandbox` declare none at all rather than leave one
  dangling.
- **There is no `command` or `args`.** A service runs its image's default
  entrypoint, so an image that needs a subcommand to start cannot be used as-is
  — bake the arguments into an image of your own.
- **Credentials here are `dev`/`dev`** and are meant to be. These are throwaway
  environments on a development cluster; for anything real, put credentials in
  a Secret and reference it with `secretRefs`.

## Verification

Every example in this directory was applied to a live cluster (k3s/k3d),
reached `Ready` with zero container restarts, and was checked to actually serve — `pg_isready`,
`mysqladmin ping`, `mongosh` ping, `rabbitmq-diagnostics ping`, `redis-cli
ping`, a memcached `version`, and HTTP 200 from Adminer, Grafana, Prometheus
and Mailpit. `make test` re-checks that each file still satisfies the CRD
schema, so a field renamed in the API cannot leave these silently invalid.
