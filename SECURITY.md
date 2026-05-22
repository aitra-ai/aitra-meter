# Security policy

## Supported versions

| Version | Supported |
|---|---|
| 0.x (pre-release) | Yes — current development branch |

Once v1.0 is released, the two most recent minor versions will receive security fixes.

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

- The measurement agent requires `hostPID: true` and `privileged: true` for NVML access. This is documented and expected. The attack surface is limited to the `aitra-system` namespace.
- No PII is collected by default. The `aitra-ai.github.io/user-id` annotation is opt-in.
- External API credentials (ElectricityMaps, WattTime) are stored in Kubernetes Secrets, never in ConfigMaps or logs.
- Air-gapped mode disables all external API calls. No data leaves the cluster boundary.
- Aitra Meter requires no write permissions on any Kubernetes resource outside `aitra-system`.
