output "release_name" {
  value = helm_release.opentams.name
}

output "namespace" {
  value = helm_release.opentams.namespace
}
