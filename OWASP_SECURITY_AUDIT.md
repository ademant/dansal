# OWASP Security Audit Report

**Date:** 2026-11-26
**Repository:** ademant/dansal
**Scope:** Repository-wide source review

## Executive Summary

No confirmed exploitable vulnerabilities were identified by the automated
security-review pass or by the supplemental targeted source checks.

The review found multiple defense-in-depth security controls, including
parameterized SQL use, Markdown HTML sanitization, explicit CSP headers,
nonce-based script CSP, route-aware cross-origin resource policy, authorization
helpers, token throttling, and activity-pub signature verification.

## Methodology

- Repository-wide security-review pass.
- Supplemental searches for dynamic SQL, HTML rendering, secret handling,
  file upload/download behavior, and security response headers.
- Manual inspection of representative authentication, templating, SQL, and
  web-header implementations.
- Source-based review only; no live instance or destructive testing was
  performed.

## OWASP Top 10 Review

| OWASP area | Result |
|---|---|
| A01 Broken Access Control | No confirmed issue identified |
| A02 Cryptographic Failures | No confirmed issue identified |
| A03 Injection | No confirmed issue identified |
| A04 Insecure Design | No confirmed issue identified |
| A05 Security Misconfiguration | No confirmed issue identified |
| A06 Vulnerable and Outdated Components | Not assessed by source-only review |
| A07 Authentication Failures | No confirmed issue identified |
| A08 Software and Data Integrity Failures | No confirmed issue identified |
| A09 Security Logging and Monitoring Failures | No confirmed issue identified |
| A10 SSRF | No confirmed issue identified |

## Findings

No confirmed findings met the threshold for reporting a vulnerability.

## Positive Controls Observed

- SQL inputs are predominantly supplied through bound parameters.
- Rendered Markdown is sanitized before it is converted to `template.HTML`.
- The web and webmin services configure CSP, nonce plumbing, and
  `Cross-Origin-Resource-Policy`.
- Public, authentication, search, geocode, form-token, and email flows include
  throttling controls.
- Event organization access-control helpers are shared across handlers.
- ActivityPub request verification supports both draft-cavage and RFC 9421
  signature schemes.
- API, web, and webmin header behavior has targeted tests.

## Limitations

This source-only review cannot establish whether deployed configuration,
dependency versions, infrastructure controls, or runtime behavior contain
additional vulnerabilities. A live OWASP ZAP/Burp assessment and dependency
audit are recommended before making a production security determination.
