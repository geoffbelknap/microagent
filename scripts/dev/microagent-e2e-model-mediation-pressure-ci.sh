#!/usr/bin/env bash
#
# CI-safe pressure target for the experimental model mediator.
#
# This is a named E2E wrapper around the fake OpenAI-compatible runner pressure
# preset. It boots microVM probes, but it does not require llama.cpp, vLLM,
# HuggingFace access, a real model, or a GPU.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

export MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE=1
export MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_PRESSURE=1
export MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_PRESSURE_PRESET=ci

exec "$ROOT/scripts/dev/microagent-e2e-model-mediation-runner-fake.sh"
