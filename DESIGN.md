# Design: template-level, idempotent image switching

## Intended use

An `AlternateImageSource` (AIS) is deployed alongside an application. It lists
sets of `equivalentRepositories` that are expected to carry the same tags. When
a container cannot pull its image, saffire rewrites the image on the **pod
template of the top-level controller** so future pods use an equivalent
repository.

```yaml
spec:
  imageSourceReplacements:
    - equivalentRepositories:
        - quay.io/fairwinds/docker-demo
        - ehazlett/docker-demo
```

## Trigger and decision basis

- The watcher enqueues the AIS objects of a namespace when a pod there enters
  `ErrImagePull` or `ImagePullBackOff`. AIS create/update also reconciles.
- Pull errors only **identify candidate targets**. The decision to rewrite is
  always made from the owning controller's **pod template**, never from the
  images of individual pods. During a rollout old and new pods coexist; treating
  an old-image pod as "not rewritten yet" would cause endless re-patches.
- Failing pods are resolved to their top controller and de-duplicated per
  target, so many failing pods of one workload produce at most one patch.

## Target selection and owner chains

`resolveTop` walks the controller `ownerReference` (via
`metav1.GetControllerOf`) upward using the typed controller-runtime client:

- `Deployment -> ReplicaSet -> Pod` resolves to the Deployment.
- `CronJob -> Job -> Pod` resolves to the CronJob.
- A pod without a controller owner (or owned by a Node, i.e. a static pod) is a
  standalone target and is patched directly on its own `spec`.
- Unknown owner GVKs stop the walk at the highest recognized object; a top
  object of an unsupported kind is reported `Skipped/UnsupportedKind`, and one
  bad target never blocks the others.
- The walk is depth-capped as a cycle guard; a transiently missing owner
  returns an error and requeues.

## Supported workloads

| Kind | Pod template path | Mutable | Rollout semantics |
|---|---|---|---|
| Deployment | `spec.template` | yes | automatic; observed via `status.observedGeneration`, `updatedReplicas` |
| StatefulSet | `spec.template` | yes | automatic unless `updateStrategy: OnDelete`, reported as `rollout.manual=true` |
| DaemonSet | `spec.template` | yes | automatic; observed via `updatedNumberScheduled` |
| Job | `spec.template` | **no** | immutable after creation → `Skipped/ImmutableTemplate`, never written |
| CronJob | `spec.jobTemplate.spec.template` | yes | affects future Jobs only; no rollout block |
| standalone Pod | `spec` | yes | no rollout block |

The controller never changes an object's rollout knobs (max unavailable,
surge, update strategy); it only observes rollout progress and reports it
separately from template write success. Init and ephemeral containers are out
of scope and are left untouched.

## Idempotent writes

- Each template container is evaluated against the AIS rules. A container is
  only rewritten when its repository matches a rule, is among the currently
  failing repositories, and the desired image differs from the current one.
- Writes use a **strategic-merge patch** (`client.StrategicMergeFrom`) on a
  deep-copied base. Containers merge by name, so only the changed `image`
  fields are sent; labels, annotations, env, probes and every other field are
  preserved, even when another tool also manages the object. Full-object
  replacement is deliberately not used.
- When no container differs, no API call is made at all. An empty strategic
  patch would still write and bump `resourceVersion`, so the diff gate lives in
  the controller.
- Nothing time-varying is ever written to a workload template. Audit data lives
  only on the AIS `status`, so recording an audit cannot trigger a rollout.
- The AIS status is itself written only when it semantically changes
  (deep-equal after slice-shape normalization), so a repeated reconcile with
  identical inputs performs zero writes and leaves every `resourceVersion`
  untouched.

## Per-container decisions

Every template container gets one entry under `status.results[].containers`:

- `Rewritten` – image changed to an equivalent repository.
- `AlreadyTarget` – already runs an equivalent, non-failing repository.
- `Skipped` – no matching rule (`NoMatchingRule`) or no alternate repository.
- `Undecidable` – the image string cannot be parsed; the container is always
  left as-is while sibling containers are still processed.

In a multi-container template only the containers that need rewriting change;
the rest of the template is never rewritten wholesale.

## Thrashing detection

A higher-level declarative tool may revert saffire's patch, causing an endless
patch/restore tug-of-war. The controller tracks per-target patch timestamps in
a sliding window (constants in `controllers/thrash.go`):

- window **10 minutes**, threshold **3 patches**.
- At 3 in-window patches the target is marked `Thrashing` (the third patch
  itself still happens); further reconciles within the window skip writes and
  report `Thrashing/ThrashingThrottled`.
- Once the window slides out the target is allowed exactly one retry; if it is
  still being reverted the counter climbs back to the threshold.
- The in-memory tracker is rehydrated from each AIS status's `patchAttempts`,
  so a process restart does not reset detection. The persisted timestamp list
  is bounded to the window.

## Status shape

`status.observedGeneration` plus `status.results[]`, one entry per target keyed
by group/kind/name/namespace/UID. Each entry carries the phase, reason,
per-container decisions, last patch time, in-window patch attempts and, where
applicable, a rollout snapshot. Results are keyed and carried across reconciles:
a target that has converged keeps its historical audit record instead of being
overwritten by a fresh "skipped" entry.

## Testing

All tests run with the controller-runtime **fake client** (no envtest
dependency). A counting client wrapper separately counts object
Update/Patch and status Update/Patch, because a status write bumps
`resourceVersion` without triggering a rollout and the two must be asserted
independently. Coverage includes: template rewrite with field preservation,
zero-write idempotent re-reconcile with unchanged `resourceVersion`, immutable
Job skip, OnDelete manual reporting, owned vs standalone pods, thrashing with
an immediately-reverting fake syncer and window expiry retry, partial
multi-container rewrites, CronJob/owner-chain resolution, CRD YAML validity and
deep-copy isolation.
