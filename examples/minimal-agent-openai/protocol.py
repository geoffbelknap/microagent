"""Agent protocol — request/reply wire shapes for an agent in a microVM.

This module is the working draft of a protocol for agents that run
inside microagent microVMs. It's a "primitive" — built from experience,
small on purpose, and meant to mature here before it earns its way into a
stable contract somewhere else.

The protocol is shaped by the ASK framework (https://askframework.org/). The
wire shapes carry the structural properties — verified principals, atomic
constraint versions, audit references, fail-closed lifecycle signals — so
that operator enforcement has something concrete to grip on. Enforcement
itself lives outside the agent boundary; the protocol just makes it visible.

ASK's three-layer cognitive model:

* **Constraints**: operator-owned, external, atomic. The agent acknowledges a
  version, then operates under it. Models: ``Constraints``, ``ConstraintAck``.
* **Identity**: the agent's accumulated self. Versioned and hashed; mutations
  are write-mediated and auditable. Model: ``IdentityRevision``.
* **Session**: agent-internal, ephemeral. Not on the wire by design.

Transport is unspecified. JSON over a workspace file, JSON over the mediation
vsock, HTTP — pick one. Pydantic v2 serializes cleanly to JSON; that's the
likely common factor.
"""

from __future__ import annotations

from datetime import datetime
from enum import Enum
from typing import Literal, Optional

from pydantic import BaseModel, ConfigDict, Field

__all__ = [
    "Principal",
    "WorkBounds",
    "WorkRequest",
    "WorkStatus",
    "WorkResult",
    "LifecycleSignalKind",
    "LifecycleSignal",
    "Constraints",
    "ConstraintAck",
    "IdentityRevision",
]


class _Frozen(BaseModel):
    """Base for protocol models. Immutable after construction."""

    model_config = ConfigDict(frozen=True, extra="forbid")


class Principal(_Frozen):
    """Who is asking. Verified by upstream before reaching the agent.

    An agent that receives a request with ``verified=False`` must refuse it (T23).
    The agent never sets ``verified`` for itself — it consumes the value set by
    the verifier on the other side of mediation.

    ``authorization_scope`` is the upper bound on what the agent may do for
    this principal (T7). Narrower is fine; broader is a violation.
    """

    name: str
    kind: Literal["user", "agent", "service"]
    verified: bool
    verified_by: Optional[str] = None
    authorization_scope: tuple[str, ...] = ()


class WorkBounds(_Frozen):
    """Operation limits for a single request (T8).

    Bounds override any agent defaults. An agent must enforce them or refuse.
    Unbounded operations are not the default.
    """

    max_duration_seconds: Optional[int] = Field(default=None, ge=1)
    max_tokens: Optional[int] = Field(default=None, ge=1)
    max_tool_calls: Optional[int] = Field(default=None, ge=0)
    max_egress_bytes: Optional[int] = Field(default=None, ge=0)


class WorkRequest(_Frozen):
    """A unit of work for the agent.

    ``content`` is data, not instructions (T24). The agent's job is to process
    it under the operator's constraints. The only entity that gives the agent
    instructions is the operator, via constraints — never the prompt content.

    If ``constraints_version`` doesn't match the version the agent is currently
    operating under, the agent must emit
    ``LifecycleSignal(signal=constraints_outdated)`` and refuse the request (T9).
    """

    request_id: str
    principal: Principal
    content: str
    content_kind: Literal["text", "json", "structured"] = "text"
    constraints_version: int = Field(ge=0)
    bounds: Optional[WorkBounds] = None
    submitted_at: datetime
    audit_ref: str


class WorkStatus(str, Enum):
    """Outcome of a ``WorkRequest``.

    ``denied`` and ``yielded`` are first-class outcomes, not failures. An agent
    that always returns ``completed`` is suspicious.
    """

    completed = "completed"
    failed = "failed"
    denied = "denied"
    yielded = "yielded"


class WorkResult(_Frozen):
    """The agent's response to a ``WorkRequest``.

    ``audit_ref`` points at the actions log written by mediation (T2). The
    agent does not write logs; it consumes the reference so the operator can
    stitch request, actions, and result together.
    """

    request_id: str
    status: WorkStatus
    content: Optional[str] = None
    error: Optional[str] = None
    completed_at: datetime
    audit_ref: str


class LifecycleSignalKind(str, Enum):
    """Agent-emitted lifecycle signals.

    Five of these are agent-emitted; ``quarantined`` is emitted by the host on
    the agent's behalf as a record, since a quarantined agent has no way to send.
    """

    ready = "ready"
    accepting = "accepting"
    completed = "completed"
    mediation_broken = "mediation_broken"
    constraints_outdated = "constraints_outdated"
    quarantined = "quarantined"


class LifecycleSignal(_Frozen):
    """A lifecycle event the agent emits (or the host records on its behalf)."""

    signal: LifecycleSignalKind
    agent_id: str
    observed_at: datetime
    request_id: Optional[str] = None
    detail: Optional[str] = None
    error: Optional[str] = None


class Constraints(_Frozen):
    """Operator-owned external state (T1, T9).

    The protocol carries the envelope — version, hash, timestamp, and a
    pointer to the payload. What "a constraint" is — allowed tools, budget
    caps, behavioral rules — is operator-defined and opaque here.
    """

    version: int = Field(ge=0)
    hash: str
    effective_at: datetime
    payload_ref: str


class ConstraintAck(_Frozen):
    """The agent acknowledges a constraint version (T9).

    An agent must emit ``ConstraintAck`` after loading new constraints and
    before processing any ``WorkRequest`` whose ``constraints_version``
    matches. No ack, no work.
    """

    version: int = Field(ge=0)
    hash: str
    agent_id: str
    observed_at: datetime


class IdentityRevision(_Frozen):
    """The agent's accumulated identity state (T25).

    Identity is read by the agent from local workspace state. Mutations go
    through mediation, which writes the mutation record and bumps the
    version. The agent cannot self-elevate (T17).
    """

    agent_id: str
    version: int = Field(ge=0)
    hash: str
    mutated_at: datetime
    mutation_ref: str
