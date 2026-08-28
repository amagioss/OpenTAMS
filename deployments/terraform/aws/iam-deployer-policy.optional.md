# Deployer IAM policy — optional statements

`iam-deployer-policy.example.json` is the **minimal** deployer policy for the
default config (`create_oidc_provider = false`, S3 SSE-S3/AES256,
`bucket_kms_key_arn = ""`). Strict JSON forbids comments, so the optional
blocks live here — add the ones your config needs.

Placeholders: `<ACCOUNT_ID>`, `<REGION>`, `<KMS_KEY_ID>`.

## Add when `create_oidc_provider = true`

Terraform creates the cluster's IAM OIDC provider (first-time IRSA setup).

```json
{
    "Sid": "IAMOIDCProvider",
    "Effect": "Allow",
    "Action": [
        "iam:CreateOpenIDConnectProvider",
        "iam:GetOpenIDConnectProvider",
        "iam:DeleteOpenIDConnectProvider",
        "iam:TagOpenIDConnectProvider"
    ],
    "Resource": "arn:aws:iam::<ACCOUNT_ID>:oidc-provider/oidc.eks.<REGION>.amazonaws.com/*"
}
```

## Add when `bucket_kms_key_arn` is set (customer-managed KMS on S3)

Deployer must read the CMK metadata during S3 SSE configuration.

```json
{
    "Sid": "KMSDescribeKey",
    "Effect": "Allow",
    "Action": ["kms:DescribeKey"],
    "Resource": "arn:aws:kms:<REGION>:<ACCOUNT_ID>:key/<KMS_KEY_ID>"
}
```

## Add when using remote state (S3 backend, `backend.tf`)

The deployer reads and writes `terraform.tfstate` and the native lock object in
the state bucket. Scope to your state bucket ARN (not the media bucket).

```json
{
    "Sid": "TerraformStateBackend",
    "Effect": "Allow",
    "Action": [
        "s3:ListBucket",
        "s3:GetObject",
        "s3:PutObject",
        "s3:DeleteObject"
    ],
    "Resource": [
        "arn:aws:s3:::<STATE_BUCKET>",
        "arn:aws:s3:::<STATE_BUCKET>/*"
    ]
}
```

No DynamoDB permission needed — `use_lockfile = true` keeps the lock in the same
S3 bucket. `GetObject`/`PutObject` read/write the state; `PutObject`/`DeleteObject`
on `/*` cover the `.tflock` lock object.

## Notes

- **ARN scoping needs the `opentams-` name prefix.** Resources are named
  `${resource_name_prefix}-${eks_cluster_name}-*` (default prefix `opentams`),
  and the bucket name must start `opentams-`. If you change
  `resource_name_prefix`, update every `opentams-*` ARN in the policy to match.
- `ec2:Describe*` and `eks:DescribeCluster` stay `Resource: "*"` — AWS has no
  resource-level support for those. Read-only, low risk.
- `s3:DeleteObject`/`DeleteObjectVersion`/`ListBucketVersions` are kept in the
  minimal policy for `bucket_force_destroy = true` teardown. Drop them if you
  never force-destroy the bucket.
