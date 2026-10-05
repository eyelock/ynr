resource "github_repository" "ynr" {
  name        = var.repository
  description = "Your named reporting: the factory's observation plane on OpenTelemetry. Spool, collector, storage and dashboards."
  visibility  = var.visibility
  topics = [
    "ai-agents",
    "duckdb",
    "factory",
    "golang",
    "observability",
    "opentelemetry",
  ]

  has_issues      = true
  has_discussions = false
  has_projects    = false
  has_wiki        = false
  is_template     = false

  # Gitflow (branches.tf): feature PRs are squash-merged into develop, titled and described by the
  # PR, so its "Closes #n" lines close issues; release and hotfix PRs into main are true merges.
  allow_squash_merge          = true
  allow_merge_commit          = true
  allow_rebase_merge          = false
  allow_auto_merge            = false
  allow_update_branch         = true
  delete_branch_on_merge      = true
  squash_merge_commit_title   = "PR_TITLE"
  squash_merge_commit_message = "PR_BODY"
  merge_commit_title          = "MERGE_MESSAGE"
  merge_commit_message        = "PR_TITLE"
  web_commit_signoff_required = false

  # A destroy archives the repository instead of deleting it.
  archive_on_destroy = true

  lifecycle {
    prevent_destroy = true
  }
}

# A docs site (GitHub Pages from /docs on main, as ynf and ynm have) is added once ynr has docs
# beyond its ADRs.
