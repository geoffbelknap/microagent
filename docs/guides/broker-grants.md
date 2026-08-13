---
title: Semantic broker grants
description: Constrain credentialed broker calls by operation and validate responses before they reach a workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-13_

# Semantic broker grants

A broker endpoint always resolves and injects its credential on the host. Its
assurance setting says what happens around that injection:

- `semantic` grants a finite set of terminating HTTP operations. microagent
  checks the request, every redirect, and the complete response.
- `trusted-upstream` preserves the broad response relay. It is an explicit
  lower-assurance choice for an upstream you trust not to return or transform
  the injected credential.

Semantic mode requires an HTTPS upstream URL without embedded credentials.
The compatibility relay can target HTTP, but `trusted-upstream` then means the
operator trusts both the service and the entire cleartext network path.

Use `semantic` for a high-assurance endpoint. It requires HTTPS, cannot enable
the opaque CONNECT proxy, and requires a grant file:

```bash
microagent run --network isolated \
  --broker-upstream https://api.example.com \
  --broker-secret api=env:API_TOKEN \
  --broker-assurance semantic \
  --broker-grant ./api-grant.yaml \
  --broker-env API_BASE_URL \
  alpine:3.20 ./agent
```

The same fields are available as `assurance=semantic;grant=./api-grant.yaml`
inside each `--broker-endpoint` spec, as `broker_assurance` and `broker_grant`
in MCP, and as `assurance` and `grant` in an Agentfile broker block. An
Agentfile resolves a relative grant path beside the Agentfile.

## Grant file

This grant allows one read operation and one write operation in a single
repository namespace:

```yaml
operations:
  - name: list-open-issues
    effect: read
    method: GET
    route: /repos/{owner}/{repo}/issues
    pathParameters:
      owner: [acme]
      repo: [widgets]
    query:
      - name: state
        required: true
        values: [open]
    headers:
      - name: Authorization
        required: true
        pattern: 'Bearer @secret:[A-Za-z0-9._/-]+'
        maxBytes: 128
    response:
      statuses: [200]
      contentTypes: [application/json]
      maxBytes: 65536
      credentialDisclosure: deny-exact
      json:
        type: object
        properties:
          total_count: integer
          items: array
        required: [items]
        additionalProperties: false

  - name: create-issue
    effect: write
    method: POST
    route: /repos/{owner}/{repo}/issues
    pathParameters:
      owner: [acme]
      repo: [widgets]
    headers:
      - name: Authorization
        required: true
        pattern: 'Bearer @secret:[A-Za-z0-9._/-]+'
        maxBytes: 128
      - name: Content-Type
        required: true
        values: [application/json]
    body:
      maxBytes: 8192
      contentTypes: [application/json]
      json:
        type: object
        properties:
          title: string
          body: string
        required: [title]
        additionalProperties: false
    response:
      statuses: [201]
      contentTypes: [application/json]
      maxBytes: 65536
      credentialDisclosure: deny-exact
      json:
        type: object
        properties:
          id: integer
          url: string
        required: [id, url]
        additionalProperties: false
redirects:
  allow: false
```

The route matcher compares complete path segments. Every `{parameter}` needs
an exact value allowlist, which is the remote account, repository, bucket, or
object namespace the credential may affect. Undeclared query keys and request
headers are denied. A query rule can use exact `values`, an RE2 `pattern`, or
both; a pattern also requires a positive `maxBytes`. URL-shaped values are
denied unless that rule sets `allowURL: true`. Set `required: true` when
omitting a query parameter or header would widen the operation. Unknown grant
fields and multiple YAML documents are rejected rather than ignored. A
declared query parameter or header may appear at most once per request.

Omitting `body` means the operation accepts no request body. A declared body
and every response use `application/json` and require a positive byte limit
plus a JSON schema. The deliberately small schema validates a top-level object, required and
additional properties, and the property types `string`, `number`, `integer`,
`boolean`, `object`, `array`, and `null`.

Every response declares allowed status codes, content types, a positive byte
limit, and `credentialDisclosure: deny-exact`. The broker buffers the complete
bounded response before writing status, headers, or body to the guest. A size,
status, content-type, schema, or exact-credential mismatch returns a broker
error without streaming the rejected upstream response.

## Redirects

Redirects are denied unless the grant opts in with a positive `maxHops`.
Allowed redirects must remain on the HTTPS upstream origin or an exact HTTPS
origin in `allowedOrigins`. Every hop is reauthorized against a declared
method, route, namespace, query, and header contract before it is sent.
Semantic redirects are limited to bodyless `GET` and `HEAD` operations.
Allowed decisions report the final host and operation when a redirect occurs.

The host, content length, and other HTTP framing fields come from the approved
upstream URL and bounded body, not from guest headers. The semantic transport
also disables environment proxy routing, cookie jars, implicit response
compression, and an undeclared default user agent.

## Credential guarantee

`deny-exact` scans all response headers and the complete bounded response body
for each exact credential value injected into the request. Buffering makes the
check independent of network chunk boundaries.

This is not a claim that a malicious upstream cannot transform a static
credential. Base64, encryption, hashing, character substitution, or a
service-specific derived value cannot be recognized generically without also
blocking ordinary data. Give a semantic broker a narrowly scoped,
short-lived credential and a constrained response schema. If you select
`trusted-upstream`, the manifest and broker decision `assurance` field report that lower
assurance and the response is relayed without the semantic checks.
