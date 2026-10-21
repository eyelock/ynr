# Deploy the AWS infrastructure

Create the S3 bucket collectors write to and central reads, with one IAM role for each collector and
one for central, using the Terraform in `infra/aws`. Terraform runs never take place in ynr's own
CI, and no credentials are kept in the repository.

## What you need

- Terraform 1.10 or later.
- AWS credentials for the account the store goes in.
- A state bucket for Terraform's state. [`infra/terraform-state`](https://github.com/eyelock/ynr/tree/develop/infra/terraform-state)
  makes one; its README says how.
- The collector ids, as `^[a-z0-9][a-z0-9._-]{0,62}$`, and for each the IAM roles, users or
  accounts that may assume its role. A collector is a runner pool or a host, not a single job. With
  the default `name`, an id is at most 45 characters, because the role name `ynr-<name>-collector-<id>`
  must fit IAM's 64.
- The principals that may assume central's role.

## Configure

```bash
cd infra/aws/example
cp example.tfvars.example main.tfvars                 # account, owner, collectors, central
cp example.s3.tfbackend.example example.s3.tfbackend  # the state key for this deployment
```

Both copies are gitignored. Edit `main.tfvars`:

```hcl
account_id = "123456789012"
owner      = "eyelock"

collectors = {
  "ci-pool-a" = { trusted_principals = ["arn:aws:iam::123456789012:role/ynh-runner-pool-a"] }
  "build-01"  = { trusted_principals = ["arn:aws:iam::123456789012:role/build-01"] }
}

central_trusted_principals = ["arn:aws:iam::123456789012:role/ynr-central-host"]
```

and `example.s3.tfbackend` with your state bucket, a key for this deployment, and its region.

Other variables you may set: `region` (default `us-east-1`), `name` (default `main`; it names the
resources `ynr-store-<name>`), `versioning` (default on), and `retention_days`, an object with a
number of days for each of `traces`, `logs`, `metrics`, `compacted_traces`, `compacted_logs`,
`compacted_metrics`, `index`, `rollups` and `leases`. To use the module from another root, call
`source = "<path>/infra/aws/store"` with the same variables and add the AWS provider yourself.

## Plan and apply

```bash
terraform init -backend-config=example.s3.tfbackend
terraform plan -var-file=main.tfvars
terraform apply -var-file=main.tfvars
```

Read the plan before you apply it. The bucket has `prevent_destroy`.

## Use the store

```bash
terraform output -raw store_url
```

prints the store URL, `s3://<bucket>?region=<region>`. The other outputs are `bucket`, `bucket_arn`,
`collector_role_arns` by collector id, and `central_role_arn`. Point the tools at it:

```bash
ynr serve --store "$(terraform output -raw store_url)" --collector-id ci-pool-a
ynr central --store "$(terraform output -raw store_url)"
```

Each runs with its own role's credentials. A collector's host or pool assumes its role with
`aws sts assume-role`, or a profile with `role_arn`; the principal that does so must be in that
collector's `trusted_principals`. Then continue with [Run ynr central on S3](run-central-on-s3.md).

## Add or remove a collector

Add a key to `collectors` and apply. Removing a key deletes its role and leaves its data in the
bucket until the lifecycle rules expire it.

## Delete a store

`terraform destroy` stops at `prevent_destroy`. To delete a store on purpose, remove the guard in
`infra/aws/store/main.tf` and set `force_destroy` if the bucket holds objects.

The full variable list is in `infra/aws/store/variables.tf`, and
[`infra/aws/README.md`](https://github.com/eyelock/ynr/blob/develop/infra/aws/README.md) describes
each resource.
