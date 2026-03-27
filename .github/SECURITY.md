# Security Policy

## Reporting Security Vulnerabilities

**DO NOT** open a public GitHub issue for security vulnerabilities. Instead, please report security issues directly to:

📧 **[opensource@telekom.de](mailto:opensource@telekom.de)**

Or use GitHub's private security advisory feature:

🔒 **[Report a Vulnerability](../../security/advisories/new)**

Please include:

- A clear description of the vulnerability
- Steps to reproduce (if applicable)
- Affected component(s) and version(s)
- Potential impact and severity
- Any suggested fixes (if you have them)

We take all security reports seriously and will respond within 48 hours to acknowledge receipt. We will keep you updated on the investigation and remediation progress.

---

## Security Practices

### Our Commitment

The das-schiff-network-operator project is committed to security by design:

- **Network Configuration** - Secure management of network resources and routing
- **Least Privilege** - Access is restricted to explicitly configured resources only
- **Certificate Management** - Automatic certificate rotation for webhook TLS

### Security Scanning

This project uses multiple security tools to maintain code quality:

- **CodeQL** - Static analysis for security vulnerabilities
- **Dependabot** - Dependency vulnerability tracking
- **golangci-lint** - Go code quality and security linting
- **REUSE** - License compliance verification

### Dependencies

We actively monitor and update dependencies to address security issues:

- Go dependencies are kept up-to-date with security patches
- Container images are built on secure base images
- All dependencies are vendored or pinned to specific versions

---

## Incident Response

In case of a confirmed security vulnerability:

1. **Acknowledgment** - We will acknowledge receipt within 48 hours
2. **Assessment** - We will evaluate severity and affected versions
3. **Disclosure Coordination** - We will coordinate a timeline for public disclosure
4. **Patch Release** - A patch will be released as soon as possible
5. **Notification** - Users will be notified of available updates
