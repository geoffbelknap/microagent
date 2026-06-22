#ifndef CMICROAGENT_SANDBOX_H
#define CMICROAGENT_SANDBOX_H

/// Applies a Seatbelt (SBPL) sandbox profile string to the current process.
///
/// This is a thin wrapper over the (deprecated-but-stable) libSystem
/// `sandbox_init`. The wrapper exists so the Swift supervisor can apply a
/// Seatbelt profile without taking a dependency on the deprecated `<sandbox.h>`
/// header (which would emit deprecation warnings under `-warnings-as-errors`).
///
/// Returns 0 on success. On failure returns non-zero and, when `errbuf` is
/// non-NULL, sets `*errbuf` to a newly allocated NUL-terminated error string the
/// caller must release with `microagent_sandbox_free_error`.
int microagent_sandbox_apply(const char *profile, char **errbuf);

/// Releases an error buffer produced by `microagent_sandbox_apply`.
void microagent_sandbox_free_error(char *errbuf);

#endif /* CMICROAGENT_SANDBOX_H */
