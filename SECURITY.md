# Security policy

## Supported versions

Security fixes are intended for the latest version on the default branch. This
project does not currently publish a formal list of supported release branches.

## Reporting a vulnerability

Please do not disclose suspected vulnerabilities in public issues or pull
requests. Use the private vulnerability reporting feature of the hosting
platform, if available. Otherwise, contact the repository maintainers privately
through the contact details on the hosting platform.

Include a description of the affected component, potential impact, and
reproduction details that can be shared safely. Please allow maintainers
reasonable time to investigate and prepare a fix before public disclosure.

## Deployment notes

OAuth2-proxy is disabled by default in the sample configuration. Do not expose
the dashboard to untrusted networks while authentication is disabled. Review
the Kubernetes RBAC permissions and keep OAuth client credentials and cookie
secrets in a Kubernetes Secret or an external secret manager.
