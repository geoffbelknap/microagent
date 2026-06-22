#include "cmicroagent_sandbox.h"

#include <stdint.h>

// sandbox_init / sandbox_free_error live in libSystem but their public
// declarations in <sandbox.h> are marked deprecated. Declare the symbols here
// so we link against them directly without importing the deprecated header.
// These signatures are ABI-stable and have been unchanged for many macOS
// releases.
extern int sandbox_init(const char *profile, uint64_t flags, char **errorbuf);
extern void sandbox_free_error(char *errorbuf);

// A flags value of 0 tells sandbox_init that `profile` is a literal SBPL
// profile string (as opposed to SANDBOX_NAMED == 1, a builtin profile name).
int microagent_sandbox_apply(const char *profile, char **errbuf) {
    return sandbox_init(profile, 0, errbuf);
}

void microagent_sandbox_free_error(char *errbuf) {
    sandbox_free_error(errbuf);
}
