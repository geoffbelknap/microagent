package vmkit

// PersistenceTier names the storage guarantee for a persisted artifact. It is
// separate from DurabilityTier, which describes lifetime across VM operations.
type PersistenceTier string

const (
	PersistenceRecoverable PersistenceTier = "recoverable"
	PersistenceOperational PersistenceTier = "operational"
	PersistenceAudit       PersistenceTier = "audit"
	PersistenceEvidence    PersistenceTier = "evidence"
)

// ContractPersistence is the machine-readable storage contract exposed by
// microagent contract and MCP contract.get.
type ContractPersistence struct {
	Tiers     []ContractPersistenceTier `json:"tiers"`
	Artifacts []PersistedArtifact       `json:"artifacts"`
}

type ContractPersistenceTier struct {
	Name        PersistenceTier `json:"name"`
	Description string          `json:"description"`
	Integrity   string          `json:"integrity"`
	Retention   string          `json:"retention"`
	Recovery    string          `json:"recovery"`
}

// PersistedArtifact assigns one owned family of files to a storage tier. Paths
// are relative to state-dir unless Scope says otherwise.
type PersistedArtifact struct {
	ID             string          `json:"id"`
	Path           string          `json:"path"`
	Scope          string          `json:"scope"`
	Tier           PersistenceTier `json:"tier"`
	Writer         string          `json:"writer"`
	Mode           string          `json:"mode"`
	Atomicity      string          `json:"atomicity"`
	Integrity      string          `json:"integrity"`
	Ordering       string          `json:"ordering"`
	Retention      string          `json:"retention"`
	CleanupOwner   string          `json:"cleanupOwner"`
	Recovery       string          `json:"recovery"`
	ContainsSecret bool            `json:"containsSecret,omitempty"`
}

// PersistenceContract inventories microagent-owned persistent files. A family
// belongs here when microagent creates, replaces, appends, or deletes it.
func PersistenceContract() ContractPersistence {
	return ContractPersistence{
		Tiers: []ContractPersistenceTier{
			{
				Name:        PersistenceRecoverable,
				Description: "Derived caches, logs, and transient host bookkeeping that may be discarded and rebuilt or reacquired.",
				Integrity:   "corruption is reported or the artifact is discarded; it is never an authority for durable workspace state",
				Retention:   "bounded by explicit cache pruning, log rotation, or stale-runtime cleanup",
				Recovery:    "rebuild, reacquire, or recreate from operational state",
			},
			{
				Name:        PersistenceOperational,
				Description: "Authoritative workspace, resource, and configuration state required for correct operation.",
				Integrity:   "structured metadata is written atomically and malformed state fails closed",
				Retention:   "retained until the owning resource is explicitly deleted",
				Recovery:    "recover from the last complete atomic version; payload loss requires restore or recreation",
			},
			{
				Name:        PersistenceAudit,
				Description: "Ordered, append-oriented records of lifecycle, access, and mediation decisions.",
				Integrity:   "malformed or interrupted records are reported; bounded retention is explicit and never presented as complete history",
				Retention:   "retained with the workspace, subject only to the declared per-stream bound",
				Recovery:    "preserve readable records and report gaps or malformed tails; never replace a damaged stream silently",
			},
			{
				Name:        PersistenceEvidence,
				Description: "Secret-bearing forensic captures held for investigation and never eligible for workspace restore.",
				Integrity:   "published as a complete directory with a manifest and artifact digests; incomplete captures stay outside the visible catalog",
				Retention:   "retained until explicit snapshot or workspace deletion; never removed by cache or stale-runtime cleanup",
				Recovery:    "preserve the prior complete capture on failed replacement; a damaged capture is unusable evidence and must be reported",
			},
		},
		Artifacts: []PersistedArtifact{
			artifact("build.working-set", "build/tmp/**", "state-dir", PersistenceRecoverable, "pkg/rootfs", "0700/0600",
				"staged temporary output", "content verification belongs to the published image/rootfs record", "not ordered",
				"removed after completion or by stale temporary cleanup", "rootfs builder", "rebuild from the source image"),
			artifact("build.base-cache", "build/base-cache/**", "state-dir", PersistenceRecoverable, "pkg/rootfs", "0755/0644",
				"entries stage beside their final location and publish by atomic rename", "entries are keyed by resolved manifest digest and validated against their metadata on restore; unusable entries are treated as a miss and rebuilt", "not ordered",
				"bounded to the newest entries by last use; cleared by image delete/prune with file deletion", "rootfs builder and image cache purge", "re-fetch the image from its source"),
			artifact("images.cache", "images/**", "state-dir", PersistenceRecoverable, "pkg/imagecache", "0700/0600",
				"index replacement is atomic; blobs publish after download", "digest and provenance are verified before use", "not ordered",
				"retained until image delete/prune", "image cache", "pull or rebuild the image again"),
			artifact("models.cache", "models/**", "state-dir", PersistenceRecoverable, "pkg/model", "0700/0600",
				"downloads stage before becoming usable", "downloaded model identity is recorded in the index", "not ordered",
				"retained until model remove/prune", "model cache", "pull the model again"),
			artifact("kernels.cache", "kernels/**", "state-dir", PersistenceRecoverable, "pkg/kernel", "0700/0600",
				"downloads stage before installation", "published kernel hashes are verified before use", "not ordered",
				"retained until operator cleanup", "kernel manager", "install the verified kernel again"),
			artifact("runtime.config-disk", "<workspace>/config.disk", "state-dir", PersistenceOperational, "pkg/workspace", "0700/0600",
				"written atomically before each boot's supervisor request", "hashed into the workspace verification record each boot", "one config per boot",
				"regenerated every start; retained until workspace deletion; snapshots capture their own copy", "workspace lifecycle", "regenerate from the workspace manifest on the next start"),
			artifact("runtime.state", "<workspace>/runtime.json", "state-dir", PersistenceOperational, "pkg/workspace and backend supervisor", "0700/0600",
				"atomic file replacement", "malformed state fails closed", "latest observation wins",
				"retained until workspace deletion; stale process fields may be reconciled", "workspace lifecycle", "reconcile against the live backend and event state"),
			artifact("runtime.latest-event", "<workspace>/event.json", "state-dir", PersistenceOperational, "pkg/workspace and backend supervisor", "0700/0600",
				"atomic file replacement", "malformed state fails closed", "latest committed event",
				"retained until workspace deletion", "workspace lifecycle", "recover the latest valid state from events.json or backend inspection"),
			artifact("runtime.result", "<workspace>/result.json", "state-dir", PersistenceOperational, "guest result listener", "0700/0600",
				"one structured result replaces the prior value", "schema and identity are validated on read", "one terminal result per run",
				"retained until the next run or workspace deletion", "workspace lifecycle", "rerun the workload when no valid result exists"),
			artifact("workspace.manifest", "workspaces/<workspace>/workspace.json", "state-dir", PersistenceOperational, "pkg/workspace", "0700/0600",
				"atomic file replacement", "malformed manifests fail closed", "latest declared configuration",
				"retained until workspace deletion", "workspace lifecycle", "restore from operator configuration or recreate the workspace"),
			artifact("workspace.rootfs", "workspaces/<workspace>/rootfs.ext4", "state-dir", PersistenceOperational, "pkg/workspace", "0700/0600",
				"created before manifest publication; snapshot restore stages before replacement", "verification metadata records source hashes and divergence", "filesystem ordering",
				"retained until workspace deletion", "workspace lifecycle", "restore a snapshot or recreate from the source image"),
			artifact("workspace.disks", "workspaces/<workspace>/disks/**", "state-dir", PersistenceOperational, "pkg/workspace", "0700/0600",
				"created before manifest publication", "declared disk paths and modes are validated before attachment", "filesystem ordering",
				"retained until workspace deletion", "workspace lifecycle", "restore from the declared source or an external backup"),
			artifactWithSecret("workspace.egress-ca", "<workspace>/egress-ca{,-key}.pem", "state-dir", PersistenceOperational, "backend supervisor", "0700; certificate 0644; private key 0600",
				"certificate and key are published before mediated networking starts", "certificate hash is recorded in snapshots and checked on restore", "one active CA pair",
				"retained until workspace deletion and copied with a snapshot fork", "workspace lifecycle", "mint a new pair only for a fresh runtime; snapshot restore requires the recorded pair"),
			artifact("volumes.registry", "volumes/index.json", "state-dir", PersistenceOperational, "pkg/volume", "0700/0600",
				"locked atomic file replacement", "malformed registries fail closed", "serialized registry mutations",
				"retained independently of workspaces", "volume manager", "repair from operator records and existing managed disks"),
			artifact("volumes.disks", "volumes/<volume>.ext4", "state-dir", PersistenceOperational, "pkg/volume", "0700/0600",
				"formatted before registry publication", "ext4 validity is required for attachment", "filesystem ordering",
				"retained until explicit volume deletion", "volume manager", "restore from an external backup; workspace deletion never removes it"),
			artifact("snapshots.standard", "<workspace>/snapshots/<tag>/**", "state-dir", PersistenceOperational, "pkg/vmkit and backend supervisor", "0700/0600",
				"complete staging directory is atomically renamed into the catalog", "manifest records artifact names, hashes, source identity, and secret purge state", "immutable capture",
				"retained until snapshot or workspace deletion", "snapshot manager", "failed replacement restores the prior complete tag"),
			artifact("events.lifecycle", "<workspace>/events.json", "state-dir", PersistenceAudit, "internal/eventhistory", "0700/0600",
				"cross-process lock plus atomic replacement", "malformed history is rejected instead of overwritten", "array order is serialized commit order",
				"bounded to the latest 1024 events", "workspace lifecycle", "preserve the readable history and report malformed content"),
			artifact("audit.egress", "<workspace>/egress-access.jsonl", "state-dir", PersistenceAudit, "internal/egress", "0700/0600",
				"append-only active file with optional rotation", "malformed records are reported by readers", "file order is writer commit order",
				"operator-configured byte cap and backup count; zero cap is unbounded", "egress mediator", "retain readable records and report malformed lines"),
			artifact("audit.broker", "<workspace>/broker-access.jsonl", "state-dir", PersistenceAudit, "pkg/broker and backend supervisor", "0700/0600",
				"append-only JSON lines", "malformed records are reported by readers", "file order is writer commit order",
				"retained until workspace deletion", "broker mediation", "retain readable records and report malformed lines"),
			artifactWithSecret("audit.broker-capture", "<workspace>/broker-capture.jsonl", "state-dir", PersistenceAudit, "pkg/broker and backend supervisor", "0700/0600",
				"append-only JSON lines", "malformed records must be treated as an incomplete capture", "file order is writer commit order",
				"opt-in and retained until workspace deletion", "broker mediation", "retain readable records; raw request capture cannot be reconstructed"),
			artifact("audit.secret-access", "<workspace>/secrets-access.jsonl", "state-dir", PersistenceAudit, "pkg/secretxfer", "0700/0600",
				"append-only JSON lines", "malformed records are reported by readers", "file order is writer commit order",
				"unbounded until workspace deletion", "secret transfer service", "retain readable records and report malformed lines"),
			artifact("logs.serial", "<workspace>/serial.log", "state-dir", PersistenceRecoverable, "backend supervisor", "0700/0600",
				"append/truncate according to runtime start", "not an authoritative state record", "byte stream order",
				"retained until workspace deletion", "workspace lifecycle", "no recovery; use structured events and results for authority"),
			artifact("runtime.plumbing", "<workspace>/{serial.in,*.pid,*.log}", "state-dir", PersistenceRecoverable, "backend supervisor", "0700/0600",
				"best-effort runtime bookkeeping", "validated against live process identity before use", "not ordered",
				"removed during teardown, gc, or workspace deletion", "backend supervisor", "recreate when the runtime starts"),
			artifact("runtime.activity", "<workspace>/activity", "state-dir", PersistenceRecoverable, "pkg/workspace and backend supervisor", "0700/0600",
				"mtime update", "never used without a configured runtime lease", "latest activity timestamp",
				"retained until workspace deletion", "workspace lifecycle", "recreate on the next eligible activity"),
			artifact("host-workers.runtime", "host-workers/**", "state-dir", PersistenceRecoverable, "internal/hostworker", "0700/0600",
				"recoverable process index plus append-only logs", "process identity is checked before reuse", "latest process record per workspace capability",
				"removed when holders release or stale processes are reconciled", "host worker manager", "restart the host worker from workspace configuration"),
			artifact("model-runners.runtime", "runners/**", "state-dir", PersistenceRecoverable, "pkg/modelrunner", "0700/0600",
				"recoverable process index plus append-only logs", "process liveness and configuration digest are checked before reuse", "latest process record per runner key",
				"removed when unpinned holders release or stale processes are reconciled", "model runner manager", "restart the runner from model and workspace configuration"),
			artifactWithSecret("snapshots.forensic", "<workspace>/snapshots/forensic-*/**", "state-dir", PersistenceEvidence, "pkg/vmkit and backend supervisor", "0700/0600",
				"complete staging directory is atomically renamed into the catalog", "manifest and artifact hashes identify the capture; restore validation always rejects retained secrets", "immutable capture",
				"retained until explicit snapshot or workspace deletion", "snapshot manager", "failed replacement restores the prior complete capture"),
			artifactWithSecret("registry.credentials", "~/.microagent/auth.json or $REGISTRY_AUTH_FILE", "operator config", PersistenceOperational, "pkg/registryauth", "0700/0600",
				"credential file replacement is operator-controlled", "malformed entries are rejected and public pulls remain anonymous", "latest operator configuration",
				"retained until registry logout or operator deletion", "registry auth manager/operator", "log in again; credentials are never reconstructed from workspace state"),
		},
	}
}

func artifact(id, path, scope string, tier PersistenceTier, writer, mode, atomicity, integrity, ordering, retention, cleanupOwner, recovery string) PersistedArtifact {
	return PersistedArtifact{
		ID: id, Path: path, Scope: scope, Tier: tier, Writer: writer, Mode: mode,
		Atomicity: atomicity, Integrity: integrity, Ordering: ordering,
		Retention: retention, CleanupOwner: cleanupOwner, Recovery: recovery,
	}
}

func artifactWithSecret(id, path, scope string, tier PersistenceTier, writer, mode, atomicity, integrity, ordering, retention, cleanupOwner, recovery string) PersistedArtifact {
	value := artifact(id, path, scope, tier, writer, mode, atomicity, integrity, ordering, retention, cleanupOwner, recovery)
	value.ContainsSecret = true
	return value
}
