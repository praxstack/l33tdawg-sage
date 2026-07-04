# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 11.x    | Yes       |
| < 11    | Security fixes only when practical |

## Reporting a Vulnerability

If you discover a security vulnerability in SAGE, please report it responsibly:

1. **Do NOT open a public GitHub issue**
2. Email the author directly via GitHub: [@l33tdawg](https://github.com/l33tdawg)
3. Include a description of the vulnerability, steps to reproduce, and any relevant logs or screenshots

## Response Timeline

- **Acknowledgement:** Within 72 hours of report
- **Assessment:** Within 7 days
- **Fix or mitigation plan:** Within 30 days

## Credit

Reporters will be credited in the changelog unless anonymity is requested.

## Scope

- **SAGE Personal (sage-gui):** Single-user, localhost-only. Primary attack surface is the local REST API on port 8080.
- **SAGE Enterprise:** Multi-node BFT consensus. Broader attack surface including inter-validator communication, RBAC, and federation.

For a detailed security analysis, threat model, and known limitations, see [SECURITY_FAQ.md](SECURITY_FAQ.md).
