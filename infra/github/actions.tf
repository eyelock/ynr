resource "github_actions_repository_permissions" "ynr" {
  repository      = github_repository.ynr.name
  enabled         = true
  allowed_actions = "all"
}

# Workflows get a read-only GITHUB_TOKEN unless a job asks for more, and cannot approve PRs.
resource "github_workflow_repository_permissions" "ynr" {
  repository                       = github_repository.ynr.name
  default_workflow_permissions     = "read"
  can_approve_pull_request_reviews = false
}

# The RELEASE_TOKEN secret, for publishing releases and the Homebrew formula, is added with the
# release workflow in the walking skeleton's fourth slice, following ynf's actions.tf.
