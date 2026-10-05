# Security alerts and automatic security fixes are off, as in ynm.
resource "github_repository_vulnerability_alerts" "ynr" {
  repository = github_repository.ynr.name
  enabled    = false
}

resource "github_repository_dependabot_security_updates" "ynr" {
  repository = github_repository.ynr.name
  enabled    = false
}
