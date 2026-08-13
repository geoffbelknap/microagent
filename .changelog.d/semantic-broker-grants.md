### Broker endpoints can enforce semantic grants

Broker endpoints now require an explicit assurance mode. The new `semantic`
mode validates each request against a typed operation grant before contacting
the upstream service, reauthorizes permitted redirects, and buffers each
response until its status, content type, size, JSON shape, and exact credential
non-disclosure checks pass. Requests and responses that fall outside the grant
fail closed, and audit events identify the approved operation and effect.

The existing generic relay remains available as the explicitly lower-assurance
`trusted-upstream` mode. Existing endpoint declarations must be updated with an
assurance mode; semantic endpoints also require a grant file.
