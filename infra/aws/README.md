# ynr on AWS

Terraform for ynr's object store on S3 (ADR-005, "Infrastructure"): the bucket collectors write to
and central reads, and the IAM roles that keep each collector to its own segments.

| Path | What it is |
|---|---|
| [`store/`](store) | The module: the bucket, its lifecycle rules, one role per collector and one for central |
| [`example/`](example) | A root that uses it, with variables, a backend file and a `.tfvars` to copy |

## What it makes

- **The bucket**, `ynr-store-<name>-<account id>`: public access blocked, SSE-S3 on, owner-enforced
  ownership, plain HTTP refused. Versioning is a variable (on by default); with it on, noncurrent
  versions expire after a day (`noncurrent_version_days`), so an erased object does not linger as an
  old version. The keys are at the bucket's root, so the store URL is
  `s3://<bucket>?region=<region>` (output `store_url`).
- **Lifecycle rules**, one per prefix, because retention is the bucket's job on S3 (central does not
  prune an S3 store). Defaults are NFR-15's central numbers, set with `retention_days`:

  | Prefix | Default |
  |---|---|
  | `traces/`, `logs/`, `metrics/` (batches, a backstop: central deletes a batch once its hour is compacted) | 30 days |
  | `compacted/traces/`, `compacted/logs/`, `compacted/metrics/` | 30 days |
  | `index/` | 30 days |
  | `rollups/` | 396 days (13 months) |
  | `leases/` (central's compaction leases) | 7 days |
  | `registries/` | never expired |

  Events are log records, so they live under `logs/`. NFR-15 keeps events 90 days and logs 30, which
  one prefix cannot do. The default is the narrower reading, 30 days. To keep events 90 days, set
  `retention_days = { logs = 90, compacted_logs = 90 }`, which keeps every log that long.
- **One role per collector id**, `ynr-<name>-collector-<id>`, assumable by the principals listed for
  it. A collector is a runner pool or a host (ADR-003). Its role may write only
  `traces/*/<id>/*`, `logs/*/<id>/*`, `metrics/*/<id>/*` and `registries/<id>/*`, and is explicitly
  denied writes and deletes under `compacted/`, `rollups/`, `index/` and `leases/`. It cannot read or
  list: a collector only writes. The id must match `^[a-z0-9][a-z0-9._-]{0,62}$`, and the role name
  must fit IAM's 64 characters, so an id is at most 45 characters with the default `name`.
- **One central role**, `ynr-<name>-central`: reads and lists everything; writes and deletes under
  `compacted/`, `rollups/`, `index/` and `leases/`; deletes batches under `traces/`, `logs/` and
  `metrics/` (compaction prunes them, and erasure removes them). It cannot write batches or
  registries.

An IAM `*` matches across `/`, so a grant cannot fix a key's depth: `traces/*/<id>/*` also matches
`traces/a/b/<id>/x`. The grants are anchored per prefix so a collector can never reach another
prefix, and the reader accepts only keys of the exact shape and ignores the rest (ADR-005).

## Use

Needs Terraform 1.10 or later and AWS credentials for the account (the state bucket comes from
[`../terraform-state`](../terraform-state/README.md)). Terraform runs never take place in this
repository's CI; no credentials are kept here.

```bash
cd infra/aws/example
cp example.tfvars.example main.tfvars                 # account, owner, collectors, central
cp example.s3.tfbackend.example example.s3.tfbackend  # the state key for this deployment
terraform init -backend-config=example.s3.tfbackend
terraform plan -var-file=main.tfvars
terraform apply -var-file=main.tfvars
```

Both copies are gitignored. To use the module from another root, call `source = "<path>/infra/aws/store"`
with the same variables and add the AWS provider yourself.

Then point the tools at the store:

```bash
ynr serve --store "$(terraform output -raw store_url)"       # a collector, with its role's credentials
ynr central --store "$(terraform output -raw store_url)"     # central, with its role's credentials
```

A collector's host or pool assumes its role with `aws sts assume-role` (or a profile with
`role_arn`); the principal that does so must be in that collector's `trusted_principals`. Adding a
collector is adding a key to `collectors`; removing one deletes its role and leaves its data until
the lifecycle rules expire it.

The bucket has `prevent_destroy`, so `terraform destroy` stops; remove the guard in
`store/main.tf` (and set `force_destroy` if it holds objects) to delete a store.

## Checks

`terraform fmt -check -recursive infra` runs in `make check`. To validate without credentials:

```bash
cd infra/aws/example && terraform init -backend=false && terraform validate
```
