# Security policy

## Supported versions

| Version | Supported |
|---|---|
| Latest release | Yes — security fixes backported |
| Previous minor | Yes — critical fixes only |
| Older versions | No |

## Reporting a vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Report vulnerabilities by emailing the maintainers listed in [MAINTAINERS.md](MAINTAINERS.md). Use the subject line `[aitra-meter] Security vulnerability report`.

Include:
- A description of the vulnerability and its potential impact
- Steps to reproduce or a proof of concept
- The version of Aitra Meter affected
- Any suggested fix if you have one

You will receive an acknowledgement within 48 hours and a resolution timeline within 5 business days.

## Disclosure policy

- We follow coordinated disclosure: vulnerabilities are fixed before public announcement.
- CVEs are requested from GitHub's advisory database when appropriate.
- Fixed vulnerabilities are disclosed in the release notes and in a GitHub security advisory.
- We credit reporters in the advisory unless they request anonymity.

## Security design notes

**Privileged pod access.** The measurement agent requires `hostPID: true` and `privileged: true` to read GPU energy counters via NVML. This is documented and expected. The attack surface is limited to the `aitra-system` namespace. Operators who cannot allow privileged pods should use the Zeus provider with a non-privileged sidecar configuration — see the configuration reference.

**No PII collected by default.** The `aitra-ai.github.io/user-id` annotation is opt-in and requires Aitra Gateway in the request path. No user identity is recorded without explicit operator configuration.

**External API credentials.** ElectricityMaps and WattTime API keys are stored in Kubernetes Secrets, never in ConfigMaps, values.yaml, or logs.

**Air-gapped mode.** Setting `airGapped: true` disables all external API calls. No data leaves the cluster boundary. All conversion factors are read from SiteConfig values.

**Kubernetes RBAC.** Aitra Meter requires no write permissions on any Kubernetes resource outside `aitra-system`. The full RBAC manifest is in `helm/aitra-meter/templates/rbac.yaml`.
