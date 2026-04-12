# Security Policy

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| main    | :white_check_mark: |
| < main  | :x:                |

## Reporting a Vulnerability

We take security vulnerabilities seriously. If you discover a security issue, please report it responsibly.

### How to Report

1. **Do NOT** open a public GitHub issue for security vulnerabilities.
2. Send a detailed report to the maintainers via the project's private security channel.
3. Include in your report:
   - Type of vulnerability
   - Full paths of source file(s) related to the vulnerability
   - Location of the affected source code (tag/commit/direct URL)
   - Step-by-step instructions to reproduce the issue
   - Proof-of-concept or exploit code (if possible)
   - Impact assessment of the vulnerability

### What to Expect

- **Acknowledgment:** We aim to acknowledge your report within 48 hours.
- **Initial Assessment:** We will perform an initial assessment within 7 days.
- **Status Updates:** We will provide updates on the progress of the fix.
- **Disclosure:** We will coordinate disclosure with you before any public announcement.

### Scope

This security policy applies to:
- The Hirdforge platform source code
- Infrastructure configurations
- Authentication and authorization mechanisms
- Data handling and storage
- API endpoints and integrations

### Out of Scope

- Social engineering attacks
- Physical security issues
- Denial of service attacks against third-party services
- Vulnerabilities in third-party dependencies (report to upstream maintainers)

## Security Best Practices

When contributing to Hirdforge:

- Never commit secrets, API keys, or credentials to the repository
- Use environment variables or Sealed Secrets for sensitive configuration
- Follow the principle of least privilege for service accounts and permissions
- Validate all user inputs and sanitize outputs
- Keep dependencies up to date

## Security Updates

Security updates will be released as patch versions and announced through the project's standard release channels.
