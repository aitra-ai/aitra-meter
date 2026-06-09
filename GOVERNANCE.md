# Governance

Aitra Meter is a [SODA Foundation](https://github.com/sodafoundation) project hosted under the Linux Foundation ecosystem.

## Decision making

- **Minor changes** (documentation fixes, small bug fixes, example additions): lazy consensus — a PR with no objections from a maintainer after 48 hours may be merged.
- **Major changes** (spec changes, architecture decisions, new core dependencies, storage or provider interface changes): require at least two maintainer approvals and an open comment period of 5 business days.
- **Breaking changes** (measurement methodology, metric label schema, CRD API, proto contract): require a written Architecture Decision Record in `docs/adr/`, a Phase milestone label, and a deprecation notice in the changelog.

When in doubt about which category a change falls into, open an issue and ask before writing code.

## Maintainers

Current maintainers are listed in [MAINTAINERS.md](MAINTAINERS.md).

**Adding a maintainer:** any existing maintainer may nominate a contributor who has made sustained, high-quality contributions across at least two release cycles. Approval requires a majority vote of existing maintainers, recorded in a PR to MAINTAINERS.md.

**Removing a maintainer:** a maintainer who has been inactive for 6 months may be moved to emeritus status by majority vote.

## Releases

Releases follow semantic versioning (`vMAJOR.MINOR.PATCH`). Release tags are created from `main`. Release notes are required for every public release.

Pre-release tags (`-alpha`, `-beta`) are used as follows:
- `-alpha` tags are internal development milestones, not announced publicly
- `-beta` tags are public releases with published images and Helm chart — interfaces may still change
- Release tags without a pre-release suffix are public stable releases

## SODA Foundation escalation

For unresolved disputes or governance questions, escalate to the SODA Foundation Technical Steering Committee at [github.com/sodafoundation](https://github.com/sodafoundation).
