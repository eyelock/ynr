# Security Policy

## Reporting a vulnerability

Please do not open a public issue for a security problem. Report it privately through GitHub's
private vulnerability reporting: open the repository's **Security** tab, choose **Report a
vulnerability**, and describe what you found and how to reproduce it.

You can expect an acknowledgement within a few days. Fixes are released from `develop` through the
usual release process, and the report is credited unless you prefer otherwise.

## Supported versions

Only the latest release of ynr receives security fixes.

## Scope

ynr reads telemetry spooled by other tools and may hold run data, including people's identifiers
(see ADR-002). Reports about how that data is stored, exposed or erased are in scope.
