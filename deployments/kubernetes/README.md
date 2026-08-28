# Kubernetes Manifests

Plain YAML, no Helm. Apply in order:

```sh
kubectl apply -f namespace.yaml
kubectl apply -f configmap.yaml -f secret.yaml
kubectl apply -f migrate-job.yaml
kubectl wait --for=condition=complete job/opentams-migrate -n opentams --timeout=300s
kubectl apply -f deployment.yaml -f service.yaml
```

## Before Applying

1. Copy `secret.yaml` and replace all `REPLACE_ME` values with real credentials.
   Do **not** commit the populated file.
2. Set the correct image tag in `migrate-job.yaml` and `deployment.yaml`.

## Teardown

```sh
kubectl delete namespace opentams
```

This removes all resources including the Deployment, Service, Job, ConfigMap, and Secret.
