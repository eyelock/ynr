# Dependabot alerts and security updates, on. Secret scanning and push protection are set on the
# repository itself (repository.tf) and follow its visibility.
resource "github_repository_vulnerability_alerts" "ynr" {
  repository = github_repository.ynr.name
  enabled    = true
}

resource "github_repository_dependabot_security_updates" "ynr" {
  repository = github_repository.ynr.name
  enabled    = true

  depends_on = [github_repository_vulnerability_alerts.ynr]
}

# Private vulnerability reporting (SECURITY.md points reporters at it). The GitHub provider has no
# resource for it, so it is switched on through the API, once, when the repository is public: GitHub
# offers it only on public repositories. Needs gh on PATH and GITHUB_TOKEN, as the provider does.
resource "terraform_data" "private_vulnerability_reporting" {
  count = local.public ? 1 : 0

  triggers_replace = [github_repository.ynr.name]

  provisioner "local-exec" {
    command = "gh api -X PUT repos/${var.owner}/${github_repository.ynr.name}/private-vulnerability-reporting"
  }
}
