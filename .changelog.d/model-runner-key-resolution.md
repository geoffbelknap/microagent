### Model pairings prefer the configured runner

When multiple live runners served the same model ref with different runner
configurations, a paired workspace could silently route to whichever runner
appeared first in the registry. Model forwards and mediators now resolve the
paired runner key first. They retain restart recovery by falling back to the
model ref when that exact configuration is gone, and record a warning when
that fallback substitutes another runner.
