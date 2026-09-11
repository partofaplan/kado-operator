# kado-operator

A Kubernetes operator that provisions isolated development environments on
demand. Declare what a team needs — a namespace, some storage, a database, a
cache — and the operator builds it and tears it down again.

```yaml
apiVersion: devenv.aviture.dev/v1alpha1
kind: DevEnvironment
metadata:
  name: team-alpha
  namespace: default
spec:
  owner: team-alpha
  storage:
    size: 1Gi
  services:
    - name: postgres
      image: postgres:16-alpine
      port: 5432
      mountPath: /var/lib/postgresql/data
    - name: redis
      image: redis:7-alpine
      port: 6379
```

```console
$ kubectl get devenvironments
NAME         PHASE   NAMESPACE    READY   SERVICES   AGE
team-alpha   Ready   team-alpha   2       2          30s
```

`kubectl delete devenvironment team-alpha` reclaims the whole namespace.

## What it does

Each `DevEnvironment` owns one namespace and everything in it:

- a namespace, labelled for ownership and cost attribution
- a ConfigMap built from `spec.config`, injected into every service
- copies of any Secrets the services reference
- an optional shared PersistentVolumeClaim
- a Deployment and ClusterIP Service per requested service
- teardown of all of it, via a finalizer, when the resource is deleted

Services reach each other by name: `postgres:5432`, `redis:6379`.

## Install

```bash
helm install kado-operator oci://ghcr.io/partofaplan/charts/kado-operator \
  --namespace kado-operator-system --create-namespace
```

Or from a checkout:

```bash
helm install kado-operator ./charts/kado-operator \
  --namespace kado-operator-system --create-namespace
```

Chart values are documented in
[`charts/kado-operator/values.yaml`](charts/kado-operator/values.yaml). The
DevEnvironment CRD is installed with the chart by default; set
`installCRDs=false` where a platform team owns CRDs separately.

## Try it locally

```bash
make k3d-up       # create the K3D cluster
make install      # install the CRD
make run          # run the operator against it

# in another terminal
kubectl apply -f config/samples/devenv_v1alpha1_devenvironment.yaml
kubectl get devenvironments -w
```

## Documentation

- [DevEnvironment reference](docs/devenvironment.md) — every field, what gets
  created, status and conditions, troubleshooting
- [Development workflow](docs/development.md) — branching, the local loop,
  testing layers, CI

## Testing

```bash
make test              # unit + envtest, no cluster required
make test-integration  # full lifecycle against K3D
make lint
```

## License

Apache 2.0 — see [LICENSE](LICENSE).
