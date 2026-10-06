# Infra

What ynr's own development needs outside the code: where Terraform keeps its state, and the
GitHub repository's settings.

| Path | What it is |
|---|---|
| [`terraform-state/`](terraform-state/README.md) | Terraform for the S3 bucket that holds the Terraform state for everything in this repository, and the IAM user that runs it. Applied once to bootstrap, then only to change the bucket or the user. |
| [`aws/`](aws/README.md) | Terraform for ynr's object store on S3 (ADR-005): the bucket with a lifecycle rule per signal, a role per collector limited to its own segments, and central's role. A module and an example root. |
| [`github/`](github/README.md) | Terraform for the `eyelock/ynr` repository: settings, branch protection, labels, Actions permissions. |
