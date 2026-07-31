# Changelog

All notable changes to Andbo are documented here.

## Unreleased

### Added
- **Kubernetes runner: `metadata:` was the one rendered mapping no contract in
  the repository reached, and it is now closed at render time.** A new guard
  (`carriesIdentityOnlyMetadata`) refuses any `objectMeta` field outside `name`,
  `namespace` and `labels`, any label key outside the three `jobLabels` builds,
  any `yaml:",inline"` field, and any value that is neither a bounded scalar nor
  that label map — recursing one level so a `labels` retyped to
  `map[string]any` cannot slip past the key check instead of being caught by it.
  `TestSecurity_TheManifestsCarryIdentityOnlyMetadata` pins the property on both
  documents across every shape `Render` emits, and
  `TestSecurity_NoOtherFieldCanWidenTheMetadata` closes `objectMeta` by field the
  way the Job's four contracts and the deny-all already were.

  The gap was measured, not argued. Every closure in the package names
  `"metadata"` in its allowed key set and none descends into it, so adding
  `Annotations map[string]string` and `OwnerReferences []ownerReference` to
  `objectMeta` — both `omitempty`, both left unset — took the **whole repository
  green**: no golden moved, no closure objected, `go vet` was clean. Wiring the
  second to a `JobSpec` field then rendered an `ownerReferences:` block on the
  deny-all `NetworkPolicy`, still green.

  `ownerReferences` is the field worth naming, because it contradicts a bound the
  enforcement notes already state — the policy "carries no ownerReference, so it
  is neither garbage-collected with the Job nor protected from removal". An owned
  policy is collected when the Job is, and the two deletions do not take the same
  time: a `NetworkPolicy` has no grace period while its pod has one, defaulted to
  30 seconds because this renderer sets none, and cascading deletion gives no
  ordering guarantee among an owner's dependents. So the deny-all can be gone for
  the last seconds of a run that is still committing and pushing, and nothing
  about the manifest looks wrong, because the field that did it is metadata
  rather than spec.

  Only one of the four refusals is reachable from a value, which the guard's own
  doc states rather than glosses: the label key set is data and is table-tested,
  while the other three are properties of the `objectMeta` *type* that no test
  value can vary. Each was proven by mutating the struct and watching every
  render in the package fail — including the one that shows the closure is not
  redundant with the guard, where a `generateName` field plus a deleted key check
  left `TestSecurity_NoOtherFieldCanWidenTheMetadata` as the only failure.
  **Bound:** the guard reads `objectMeta`. `templateMeta` — the pod template's
  own metadata, whose labels are what the `NetworkPolicy` binds to — is a
  different type and is not reached.

- **Kubernetes runner: the rendered `NetworkPolicy` is enforced to deny both
  directions.** A new render-time guard (`deniesEveryDirection`) refuses any
  `policyTypes` that is not exactly `[Ingress, Egress]`, and the NetworkPolicy
  document's field set is now closed by
  `TestSecurity_NoOtherFieldCanPunchAHoleInTheDenyAll` the way the Job's four
  contracts already were.

  This was the one contract in the package with **neither a guard nor a
  closure**, and the gap was demonstrated rather than argued.
  `bindsPolicyToPod` — the only NetworkPolicy guard `Render` called — checks
  *where* the policy lands and *what* it selects, never what it **denies**: given
  a policy with a perfect namespace and a perfect selector and a `policyTypes` of
  `nil`, `[]`, `[Ingress]` or `[Egress]`, it returned `nil` in all four cases. And
  adding an egress rule field to `networkPolicySpec` with `omitempty`, left unset,
  rendered no byte and took the entire repository green — one line away from being
  set, which is exactly the latent authorisation `assertClosed`'s third direction
  exists for, arriving through a struct field instead of an allowed-set entry.

  The rendered *value* was already pinned and that half is not overstated here:
  dropping `Egress` from the literal in `Render` fails three goldens and
  `TestRender_NetworkPolicyDeniesBothDirections`. What was unpinned was the
  document's **shape**, and the shape of the assertions — the denial was checked
  on `goldenSpec` alone, which is `DefaultJobSpec` plus a name, an image and a
  command, so a rule rendered only for a caller who names a service account was
  invisible. It is now asserted across every shape `Render` emits.

  Why an empty `policyTypes` is the case worth naming, verified against
  `SetDefaults_NetworkPolicy` rather than assumed: it is **not** a policy that
  denies nothing by omission, and it is not rejected either. The API server fills
  `len(obj.Spec.PolicyTypes) == 0` with `["Ingress"]` and appends `"Egress"` only
  `if len(obj.Spec.Egress) != 0` — so on a rule-less deny-all, the field rendered
  precisely to close egress is completed by the cluster into the one value that
  opens it. Nothing in the manifest looks wrong, because the wrong value is not in
  the manifest. This is the same defaulting hazard the `imagePullPolicy` guard
  already refuses.

  **The apply-time half does not hold at all, and that is now stated.** The
  existing lifetime note covered *deletion*; deletion is the loud route and not
  the only one. `ValidateNetworkPolicyUpdate` re-runs the same spec validation a
  create does and pins nothing in it — **a NetworkPolicy has no immutable *spec*
  fields** — where the Job at least freezes its pod template. Its *metadata* is a
  different matter, and review caught the first draft of this entry saying a
  NetworkPolicy had no immutable fields at all: `ValidateObjectMetaUpdate` calls
  `ValidateImmutableField` on `name`, `namespace`, `uid`, `creationTimestamp`,
  `deletionTimestamp` and `deletionGracePeriodSeconds`. Dropping "spec" was not
  merely imprecise — it contradicted the argument it was supporting, since the
  edited policy keeps the identity it was reviewed under *because* those six are
  pinned. So anyone who can update it rewrites the live
  object instead: add an egress rule (an empty one is the widest hole available,
  since upstream reads an empty `to` as every destination and an empty `ports` as
  every port), drop `Egress` from `policyTypes`, or repoint `podSelector` at
  labels no pod carries. None of the three deletes anything — the object is still
  there, still named for the run, still carrying the run's labels — so it reads as
  present and bound in `kubectl get networkpolicy` while the agent has egress,
  which makes it *quieter* than deletion rather than merely equivalent. Read the
  deny-all as a property of the manifest at apply time, not of the run. RBAC on
  `networkpolicies` (the `update` verb as much as `delete`) is what defends the
  live object.

  The `networkPolicy` type's own comment claimed "adding rule fields here is the
  only way to punch a hole". That was an overclaim and is corrected: the other way
  needs no field at all, since `policyTypes` is a plain slice literal in `Render`
  and `bindsPolicyToPod` never read it.

- **Kubernetes runner: the rendered Job is enforced to start exactly one agent
  container.** The pod declares one agent container, plus — with
  `--workspace image:/path` only — one init container that copies the workspace
  in. A new render-time guard (`runsOnlyTheAgent`) refuses anything else, and it
  derives the permitted set from the **workspace transport** rather than from a
  constant, because the transport is the only opt-in input that legitimately adds
  a container. Both containers are checked by name **and** by argv: review found
  the first draft authorising the init slot and then reading only its nameplate,
  which let a shell-form init container through (`sh -c "cp …; curl … | sh"`)
  with no golden regeneration needed, since no golden fixture pairs the image
  transport with an opt-in field. For the agent, argv means `Command` **and**
  `Args` — the rendered argv is the two concatenated, so comparing only `Command`
  left an appended flag costing exactly one golden regeneration.

  The count was pinned only *incidentally* before this: three assertions in the
  repository fataled on it, and every one did so as a precondition to reaching
  `containers[0]` rather than as a property. That is weak in the direction that
  matters, and it was demonstrated rather than argued — a sidecar gated behind
  `ServiceAccountName` renders in no fixture that counts, so `make lint` and the
  whole test suite went green with a second container sitting in
  `full.golden.yaml` once the goldens were regenerated. The first draft of the
  new test had the same hole for the same reason, so its fixtures now cross each
  transport with **every optional JobSpec field set**: an opt-in field left at its
  zero value cannot be seen to add anything.

  Why a second container is a security problem and not a tidiness one, verified
  against the kubelet's `getPhase` rather than assumed: `case running > 0 &&
  unknown == 0: return v1.PodRunning` is evaluated *before every terminal case in
  that switch* — the qualifier was added later, because `getPhase` has one
  terminal return ahead of the switch (`failedInitialization > 0 &&
  spec.RestartPolicy == RestartPolicyNever`) that this renderer reaches, though
  not in the case described here, since an agent that exited 0 had its init
  containers succeed —
  so a container that does not exit holds the pod `Running` after the agent has
  exited 0 — the Job never completes and burns its whole
  `activeDeadlineSeconds`, ending `Failed/DeadlineExceeded` with the pod and its
  logs deleted. The other direction is the terminal branch: the pod is
  `PodSucceeded` only `if stopped == succeeded`, so a second container exiting
  non-zero fails a run the agent completed. **Neither outcome names the extra
  container anywhere in the Job's status**, so both read as the agent having
  failed. An extra *init* container is not concurrent but runs before the agent on
  the same workspace volume, so it can rewrite the tree the agent then commits.

  The apply-time half of this contract holds, unlike the no-retry and one-pod
  contracts beside it — but not because the pod template is simply frozen, and
  the imprecise version of that claim was caught in review. While a Job is
  suspended the update path exempts container `resources` and the scheduling
  directives, and the exempt set includes the template's **labels**, which is
  what this manifest's `NetworkPolicy` binds to. What refuses an added container
  is narrower: `validatePodResourceUpdatesOnly` copies the new resources across
  only when `len(newPod.Containers) == len(oldPodCopy.Containers)` and the names
  line up index-for-index, then compares the whole pod spec for equality — so a
  container list that grew, shrank, or was renamed fails in all three branches.
  Live pods allow only `spec.containers[*].image` and
  `spec.initContainers[*].image` to change. Container-list **membership** is
  therefore genuinely held, and nobody edits a container into a live Job the way
  they can raise `backoffLimit` or drop `parallelism` to 0.

  What no manifest can hold is the **pod**: an ephemeral
  container is added through the pod's own `ephemeralcontainers` subresource
  rather than through the template (`kubectl debug` is the usual route), so it
  never meets that immutability at all, it shares the pod's network namespace,
  and it **cannot be removed once added**. Namespace RBAC on
  `pods/ephemeralcontainers` is where that is refused, not the manifest — stated
  in `EnforcementNotes` and pinned by a test, like every other bound in this
  package.
- **Kubernetes runner, slice 1: `andbo k8s render` CLI surface.** The renderer
  from slice 0 is now reachable: `andbo k8s render "<task>" --name <job>
  --namespace <ns> --workspace <empty|image:PATH>` loads your policy, builds the
  agent command through the normal adapter, crosses the `FromRuntimeSpec`
  boundary, and writes the two-document manifest to **stdout** so it composes
  (`| kubectl apply -f -`). Everything else — the summary and the full
  "not enforced" list — goes to stderr. `--json` emits the manifest plus every
  note as one object.

  It **renders only**: no kubeconfig is read, no cluster client exists in
  `go.mod` (asserted by a test), no agent runs, no session is recorded, and
  nothing is applied. `budget.max_runtime_minutes` becomes
  `activeDeadlineSeconds` (and `0` keeps the renderer's bounded 1800s default
  rather than meaning "no deadline" as it does for `andbo run` — a pod nobody
  supervises always gets one). `--workspace` has no default, because an emptyDir
  makes "workspace lost" and "workspace never declared" render identically.

  Fail-closed, never downgraded — all exit `2`: `network.mode` `allowlist`/`open`,
  `runtime.isolation: local`, `budget.max_runtime_minutes` above the cap (bounded
  in **minutes**, so the check never depends on the duration conversion), and an
  agent that needs environment variables of its own (`goose` sets `GOOSE_MODE`;
  nothing but `HOME` crosses into a Job) are each rejected with an error naming
  where the workload *can* run. A `--policy` path that does not exist is an error
  rather than a silent fall-back to built-in defaults, which would have swapped
  the floating-tag default image for a pinned digest under a summary claiming the
  named policy had been applied. `HOME` is set to the writable volume for both
  workspace transports, since the pod root filesystem is read-only. A
  `secrets.allow` name that is actually set in the host environment **stops the
  render** rather than being dropped or inlined into a plain-text manifest — the
  exception being `PATH`, `LANG`, `LC_ALL` and `TERM`, which are always dropped
  because the image supplies them. An invalid manifest exits `7`, with manifest
  field names mapped back to the flag or policy field that produced them.

  Four CLI-layer enforcement notes were added alongside the renderer's own,
  covering what this command does not do — in particular that `filesystem.deny`
  cannot sanitize a workspace baked into an image, since Andbo never copies one.
  `SECURITY.md` now states the Kubernetes boundary: everything in the rendered
  manifests is enforced by your cluster, not by Andbo.
- **Kubernetes runner, slice 0: hardened manifest rendering contract**
  (`internal/runtime/k8s`). Renders a batch/v1 Job plus a default-deny
  `NetworkPolicy` that selects that Job's pod, for an external scheduler (or a
  future Andbo Kubernetes backend) to apply. Secure by construction: non-root,
  `readOnlyRootFilesystem`, `allowPrivilegeEscalation: false`, capabilities
  dropped to `ALL`, seccomp `RuntimeDefault`,
  `automountServiceAccountToken: false`, `enableServiceLinks: false`,
  `dnsPolicy: None` with a single loopback nameserver (so the pod is handed
  neither the kube-dns resolver nor the `svc.cluster.local` search domains —
  applied by the kubelet, so it holds where the CNI does not implement
  `NetworkPolicy`; it stops accidental resolution and is not a boundary), no
  privileged mode, no host namespaces, and size-limited `emptyDir` as the only
  volume source. Resources, `activeDeadlineSeconds`, and
  `ttlSecondsAfterFinished` are all required and bounded. `RuntimeClassName`
  and `ServiceAccountName` are rendered only when explicitly requested.
  `FromRuntimeSpec` maps the existing container `RuntimeSpec`/`CommandSpec`,
  and its main job is refusal: only `Image`, `NetworkMode`, `User`,
  `Executable`, `Args`, and `Timeout` cross the boundary. Everything
  host-derived fails closed with an actionable error — bind mounts, the host
  working directory, and the resolved host environment (which carries secret
  values, and which this renderer could only inline as plain text).

  There is **no CLI surface and no cluster interaction** in this slice: the
  package renders and validates only. Domain allowlisting is **not** implemented
  for Kubernetes — `network.mode=allowlist` and `open` are rejected rather than
  silently downgraded, since NetworkPolicy selects by IP/namespace/pod, not by
  domain. `JobSpec.EnforcementNotes()` states what the manifests do **not**
  guarantee, including that NetworkPolicies are additive (another policy in the
  namespace, or a cluster-scoped `AdminNetworkPolicy`, can grant egress this one
  cannot remove), that the policy must outlive the pod and is not
  garbage-collected with the Job, that `backoffLimit: 0` is not at-most-once
  execution, and that `$HOME` is not writable under the read-only root
  filesystem.

### Fixed
- **Kubernetes runner: the README presented a partial immutability list as the
  list.** It said "the update validation's immutability checks name `selector`,
  `completionMode`, `podFailurePolicy`, `backoffLimitPerIndex`, `managedBy` and
  `successPolicy`, and not this one" and stopped, while the enforcement note
  beside it already carried the qualifier. That is not a smaller version of the
  same claim: verified against upstream, `ValidateJobSpecUpdate` calls
  `ValidateImmutableField` on exactly those six *and separately* calls
  `validatePodTemplateUpdate`, `validateCompletions` and
  `validateJobSchedulingUpdate` — so a README-only reader concluded the pod
  template was editable on a live Job, contradicting what the same page says
  three paragraphs earlier about `restartPolicy` and `imagePullPolicy`.
  `TestSecurity_ReadmeSaysTheImmutableListIsPartial` now fails if the qualifier
  is dropped again, and asserts the helper NAMES rather than the sentence, since
  a name is the smallest thing a rewrite cannot keep while dropping the point.
- **Kubernetes runner: the one-container test's `getPhase` claim outlived its own
  correction.** `runsOnlyTheAgent`'s doc was corrected to say `case running > 0
  && unknown == 0` is reached before the terminal cases *in that switch*, and
  noted the one terminal return ahead of it; the test comment and the changelog
  entry that made the same claim were not. Verified against upstream: `getPhase`
  returns `PodFailed` before the switch when `failedInitialization > 0 &&
  spec.RestartPolicy == RestartPolicyNever`, and this renderer reaches both
  halves of that condition (`Never` always, an init container under
  `--workspace image:`). The conclusion is unaffected — an agent that exited 0
  had its init containers succeed — but "before every terminal case" was wrong as
  written in two of the three places it appeared.
- **Kubernetes renderer: the no-retry and one-pod contracts were stated as
  properties of the run, and only some of them survive being applied.** The
  wall-clock note already said `activeDeadlineSeconds` is mutable on a live Job;
  the two notes beside it said nothing, so a reviewer reading a manifest was
  entitled to assume all of it held for the life of the run. Verified verbatim
  against upstream master rather than assumed, and the answer differs per field.
  The pod template is immutable while the Job is not suspended, so
  `restartPolicy: Never` and `imagePullPolicy: Always` are held. `completions: 1`
  is held by a three-step chain — `validateCompletions` allows a change only for
  an `Indexed` Job, this Job is `NonIndexed` because the renderer emits no
  `completionMode` and `SetDefaults_Job` stores that default, and `completionMode`
  is itself immutable. **`backoffLimit: 0` and `parallelism: 1` are held by
  nothing**: raising `backoffLimit` re-arms the replacement pod on the
  controller's next sync for as long as the run is alive — bounded only by the
  run's end, since a Job already `Complete` or `Failed` is skipped before the
  retry budget is read.
- **Kubernetes renderer: `parallelism` is a restart switch, and two notes said it
  was harmless.** Adversarial re-review falsified a claim made in the first draft
  of the change above — that `parallelism` is "freely mutable and merely inert"
  because the controller caps wanted pods at `completions` minus successes. That
  argument is sound in the RAISING direction only. `parallelism: 0` is an equally
  legal update, and it exposes a mechanism this package had never modelled: **the
  Job controller strips a pod's tracking finalizer before issuing its own deletes,
  and every failure-counting path skips a finalizer-less pod.** So scaling to 0
  deletes the running agent while counting no failure against `backoffLimit: 0`;
  the Job survives with no pod; scaling back to 1 starts the agent over on a
  half-written workspace. Done quickly, the replacement is created while the
  original is still terminating — concurrent agents on one repository — because
  terminating pods are subtracted from the creation diff only under a
  `podReplacementPolicy` this renderer does not emit.
- **Kubernetes renderer: "suspending a running Job ends it" was wrong, and it was
  wrong as a *correction*.** The same finalizer mechanism falsifies it. The note
  originally said a suspend/resume cycle hands the run a fresh budget; that was
  "corrected" to say a suspend kills the run, reasoning that the deleted pod
  counts as a failure against `backoffLimit: 0` — which is true of a pod deleted
  by anyone *else*, and false for the controller's own deletions. Upstream's real
  distinction is who deleted the pod, not which `podReplacementPolicy` is set. The
  original claim was closer to true: a suspend neither pauses nor kills, the Job
  stays unfinished, and resume resets `status.startTime` — so suspend/resume is an
  unbounded wall-clock extension *and* a way to re-run an agent that has already
  pushed. Two successive notes described the controller accurately and drew
  opposite wrong conclusions from it; the test now pins that lesson.
- **Kubernetes renderer: the apply-time assertions were topic-shaped, so a note
  could be inverted and stay green.** Review rewrote all three notes to say
  `backoffLimit` is the pinned half, `restartPolicy` the loose one, and
  `parallelism` immutable — keeping every asserted substring verbatim — and took
  the **whole repository green**. Anchors like `"refuses to change the template at
  all"` pinned a rule without pinning which field it lands on, and `"raises it on
  a running job"` survived being negated (`"nobody raises it on a running job"`).
  Every clause now fuses polarity to subject.
- **Kubernetes renderer: an allowed key could be authorised before the field
  existed.** `assertClosed` checked declared⊆allowed and rendered⊆allowed, never
  allowed⊆declared, so a test-only diff adding `"completionMode": true` to the
  three allowed sets changed no manifest byte, touched no production code, went
  green, and silently disarmed all three closures for a field landing in a later
  commit. The reverse direction is now checked.
- **Kubernetes renderer: the one-pod contract had no closed key set of its own,
  and the two closures covering that struct ask other contracts' questions.** A
  closure is a question put to whoever adds a field, and it defends only the
  contract it names. Measured on the parent commit: adding `CompletionMode` to the
  Job spec struct failed exactly the no-retry and wall-clock closures — and
  `completionMode` reinstates no retry and moves no deadline, so a maintainer
  answering both messages **honestly** adds it to both allowed sets, which took
  the **whole repository green with no golden regeneration**. What that ships is
  the single switch that unpins `completions` on a live Job, and under `Indexed`
  it is mutable *only in tandem with `parallelism`* — concurrent agents on one
  repository with one set of credentials, while the manifest still renders
  `completions: 1` and `parallelism: 1` and the render-time guard still passes.
  `TestSecurity_NoOtherFieldCanStartASecondPod` now asks the pod-count question at
  Job level; the pod and container levels are deliberately not re-closed, for
  reasons stated on the test.
- **Kubernetes renderer: both manifest key-set closures were blind to any field
  added with `omitempty`.** The closures walked the keys the FIXTURE rendered, so
  a field left at its zero value by `validSpec()` was invisible to them — and
  rendered normally for any caller who set it. Measured on the merged contract:
  adding `ManagedBy` to the Job spec struct with `omitempty`, wired from a new
  `JobSpec` field, took the **whole repository green with no golden
  regeneration**, while a caller setting it rendered `managedBy:` beside a
  correct `activeDeadlineSeconds:` — switching the Job to an external controller
  and so disabling the deadline and `backoffLimit` together. That is the exact
  field both closures name in their own messages as the thing they exist to
  catch; the same hole was reproduced at pod level (`terminationGracePeriodSeconds`)
  and container level (`lifecycle`). The crack was already written down and not
  recognised — the pod-spec closure noted that three of its keys "render only when
  set" without noticing that tolerance also admits fields nobody has thought of.
  Both closures now read the manifest STRUCTS as well as the rendered keys, so a
  field that exists is seen whatever any fixture does with it.
- **Kubernetes renderer: the wall-clock note claimed a suspend/resume cycle hands
  the run a fresh budget "each time", which is false for the manifest this package
  emits.** The mechanism is real for a generic Job, but `backoffLimit: 0` makes
  the first suspend of a RUNNING Job terminal: suspending deletes the pod,
  `isPodFailed` counts a deleted pod as failed (the exemption needs
  `podReplacementPolicy: Failed`, defaulted only alongside a `podFailurePolicy`
  this renderer never emits), and `1 > 0` finishes the Job as
  `BackoffLimitExceeded`. So a suspend does not pause an Andbo run, it kills it —
  and lands it in the same state the no-retry note teaches operators to read as an
  agent failure, which is the more useful fact and was stated nowhere. Two smaller
  corrections alongside it: the grace period is a **ceiling**, not a duration (an
  agent handling SIGTERM exits at once; a push that overruns is SIGKILLed
  mid-flight, leaving a half-written push or an `index.lock`), and the list of
  immutable fields is the immutable *spec fields* rather than everything the
  update path checks — it also constrains the pod template.
- **Kubernetes renderer: the wall-clock guard's two refusals could swap messages
  with the suite still green.** The guard's doc comment says the two ends of the
  range "fail differently and the messages must not be interchangeable", and
  nothing checked that: both messages interpolate the value with `%d`, so
  anchoring the tests on `activeDeadlineSeconds=0` matched whichever message was
  returned. Review measured it — giving the zero branch the over-cap message
  verbatim kept the whole repository green, leaving the guard to tell a
  maintainer that a budget of `0` "exceeds the 86400-second cap" and to advise
  *lowering* it. Each case now pins a clause unique to its branch as well as the
  value. Two more claim-precision defects from the same review: the README's
  stated **default** (`1800s`, what a run actually gets when
  `budget.max_runtime_minutes` is `0`) was tied to `DefaultActiveDeadlineSeconds`
  by nothing — the same hole the cap check had just closed, and arguably the more
  load-bearing number — and the rendered-manifest test presented RANGE as an
  independent property when it is dominated by the provenance check and
  unreachable out of range, which the comment now states.
- **Kubernetes renderer: the wall-clock note named the elaborate way to defeat the
  budget and not the obvious one.** The note said a suspend/resume cycle hands the
  run a fresh budget, which is true, and left the impression that extending a run
  takes a trick. It does not: `activeDeadlineSeconds` is absent from the immutable
  list in `ValidateJobSpecUpdate` — whose `ValidateImmutableField` calls name
  `selector`, `completionMode`, `podFailurePolicy`, `backoffLimitPerIndex`,
  `managedBy` and `successPolicy`, which is the immutable *spec fields* and not
  everything that path holds (it also calls `validatePodTemplateUpdate`,
  `validateCompletions` and `validateJobSchedulingUpdate`) — so
  the same permission that could suspend and resume the Job can instead raise the
  number directly. The budget in a reviewed manifest is the budget at apply time,
  not for the life of the run, and the note and README now say so.
- **Kubernetes renderer: `activeDeadlineSeconds` was a value nothing checked once
  it had been rendered, and the level below it was closed by nothing.** The
  rendered manifest is unchanged and always carried a bounded deadline; this
  hardens what holds it there. The budget is now refused at render time outside
  `1..86400`: `0` is not "no deadline" but a deadline already spent — the API
  server admits it for a Job, since batch validation asks only that the value be
  nonnegative, while the pod-level field of the same name must be positive — and
  `pastActiveDeadline` compares with `>=`, so the Job is failed the first time
  the controller evaluates it. The rendered value is asserted at the renderer,
  across both workspace transports, for **presence** (absent is not a longer run
  but an unbounded one, because the API server defaults no deadline), **form** (a
  quoted string is rejected by the API server so the run never starts; a `null`
  means it never stops), **range**, and **provenance** — a rendered constant
  would state a budget the caller never chose, and the manifest is the only place
  a run's bound is written down. Those four were previously checked only from the
  `andbo k8s render` side, on CLI-produced manifests, against a literal `86400`
  rather than the package's own cap.

  The hole underneath them was `Job.spec.template.spec`, which no test closed:
  `terminationGracePeriodSeconds` is *added* to every budget this renderer emits,
  since the Job controller deletes pods through a call that carries no delete
  options at all. Measured on the parent commit, rendering a 3600-second grace
  period failed exactly three tests, all of them golden — and
  `go test ./internal/runtime/k8s -update` then took the whole repository green
  on a manifest where every run overruns its stated budget by an hour.

  The enforcement note now states that the deadline is when the cluster *begins*
  ending the run and not when the agent stops, that the agent goes on running for
  up to the pod's grace period afterwards, that a `DeadlineExceeded` Job with its
  pod and logs deleted is **not** evidence the agent did nothing, and that a Job
  reconciled by another controller through `managedBy` has no deadline applied at
  all. (Two claims this entry originally made about that note did not survive its
  own re-review and are corrected above: the grace period is a ceiling rather
  than a duration, and a suspend/resume cycle does **not** hand the run a fresh
  budget — for this manifest it ends the run.)
- **Kubernetes renderer: nothing enforced the no-retry contract at render time.**
  The rendered manifest is unchanged and always carried `backoffLimit: 0` with
  `restartPolicy: Never`; this hardens what holds them there. Both are now
  refused at render time and asserted by name across every workspace transport,
  and the rendered `Job.spec` and container key sets are closed — because a
  guard reading named struct fields cannot see a field that does not exist yet,
  and `podFailurePolicy` (`action: Ignore`) would make `backoffLimit: 0` inert
  while it still rendered as `0`. The enforcement note now states that the two
  fields are one control, what `OnFailure` would actually cost, and that a
  Failed Job with its logs deleted does not tell you whether the agent pushed.
- **Kubernetes renderer: the reserved-namespace guard only knew the prefix
  Kubernetes reserves, so every namespace a privileged add-on owns rendered
  clean.** `--namespace cert-manager`, `kyverno`, `gatekeeper-system`,
  `ingress-nginx`, `velero`, `cattle-system`, `istio-system`,
  `openshift-monitoring` and the rest of the residual the previous fix
  documented all produced an applyable manifest that put the agent in a
  namespace owned by a project whose default install binds cluster-wide RBAC to
  a service account living there — for several of them, cluster-wide access to
  `Secrets`. The renderer's own enforcement notes name
  another NetworkPolicy in that same namespace as the way this Job's
  default-deny is silently defeated: policies are additive and cannot subtract
  from one another, so a namespace belonging to such a component is the one
  place the deny is least able to hold.

  The guard now has two halves, and which half a name lands in is decided by
  Kubernetes namespace semantics rather than by how the name looks:

  - **Reserved prefixes**, for platforms that actually reserve a family:
    `kube-` (documented as reserved) and `openshift-` (OpenShift refuses to
    create a project under it). The tail does not matter, so
    `openshift-anything` is refused the same way `kube-flannel` already was.
  - **Exact names**, for everything else: `kubeflow`, `kubernetes-dashboard`,
    `calico-system`, `tigera-operator`, `istio-system`, `metallb-system`,
    `openshift`, `cattle-system`, `gatekeeper-system`, `kyverno`,
    `cert-manager`, `ingress-nginx`, `velero`. These are **not** prefixes on
    purpose. Namespace names are flat — `cert-manager-runs` has no relationship
    to `cert-manager` — so a prefix test there would refuse namespaces an
    operator may legitimately dedicate to agent runs, and a renderer that
    refuses the safe case teaches operators to work around it. `cert-manager-runs`,
    `velero-agent-runs`, `kyverno-agent`, `kube`, `andbo-kube-runs`,
    `andbo-runs` and `default` all still render (tests pin each).

  The error names the owning project and says why the name is refused — the
  additive-policy reason, plus the reason that is true of *that* namespace —
  rather than only asserting the namespace is taken. For most of the list that
  is cluster-wide privilege. For `kubernetes-dashboard` and `openshift` it is
  ownership alone: the Dashboard ships a namespaced `Role` and a metrics-only
  `ClusterRole` (operators binding `cluster-admin` to it is a thing operators
  do, not a thing it ships), and `openshift` is a content namespace of shared
  imagestreams and templates with nothing of OpenShift's running in it. Both are
  still refused, on the ground that holds. A test pins the split in both
  directions, so the stronger reason cannot be asserted where it is not true.
  Exit code is unchanged (`7`, invalid manifest).

  **What this still does not cover:** any privileged project outside that list,
  and the rest of a family only one member of which is named — `argocd`,
  `flux-system`, `linkerd`, `vault`, `crossplane-system`, `rook-ceph`, and
  Rancher's `cattle-*` beyond `cattle-system`, of which `cattle-fleet-system`
  is the one to watch. Rancher does not reserve `cattle-` the way Kubernetes and
  OpenShift reserve theirs, so it gets no prefix test. That residual is pinned
  in both directions exactly as before: the guard's stated bound has to name
  each one, and each one has to still render. Enforcement note 3 remains the
  standing answer: run agents in a dedicated namespace, and audit the namespaced
  and cluster-scoped policy objects before applying.
- **Kubernetes renderer: the reserved-namespace guard listed three names, so the
  rest of the reserved prefix walked past it.** `kube-system`, `kube-public`, and
  `kube-node-lease` were refused by exact match, so `--namespace kube-flannel`
  — or any other name under the `kube-` prefix Kubernetes reserves for its own
  system namespaces — rendered a clean manifest placing the agent in a namespace
  owned by cluster components and their service accounts.

  That is not only co-tenancy. The renderer's own enforcement notes name another
  NetworkPolicy in the same namespace as the way this Job's default-deny is
  silently defeated: NetworkPolicies are additive and cannot subtract from one
  another, so whatever policy the cluster's components already have in their
  namespace, this Job's deny-all could not take it away.

  The check is now on the **prefix**, so it cannot stop covering whatever the
  next distribution calls its system namespace, and the error states the
  additive-policy reason rather than only asserting the namespace is reserved
  (both the prefix and that reason are pinned by tests). It stays a prefix test:
  `kube` and `andbo-kube-runs` still render, and so does `default` — `andbo k8s
  render` warns about that one rather than refusing it. Exit code is unchanged
  (`7`, invalid manifest).

  **What this does not cover:** only the prefix Kubernetes itself reserves. The
  names other projects and distributions pick for themselves — `kubeflow`,
  `kubernetes-dashboard`, `calico-system`, `tigera-operator`, `istio-system`,
  `metallb-system`, `openshift-monitoring` and the rest of `openshift-*` — still
  render, because the renderer has no way to know a cluster put anything
  privileged in them. `kubeflow` and `kubernetes-dashboard` are the two to watch:
  each is `kube` followed by something that is not the hyphen, so each misses the
  prefix by a single character and renders looking like a name the guard weighed
  and cleared. Neither is, and rendering is not a verdict on any of the others
  either. Those names are now pinned by a test in both directions: the bound has
  to enumerate each one, and each one has to still render. The list is
  illustrative, not exhaustive — no list could be, which is what makes it a
  bound. Enforcement note 3 remains the standing answer: run agents in a
  dedicated namespace, and audit the namespaced and cluster-scoped policy objects
  before applying.

  **Superseded within this same unreleased cycle** by the entry above, but only
  as to the "what this does not cover" list: those names are now refused,
  `openshift-*` by prefix and the rest by exact name. Everything this entry
  records as *rendering* — `kube`, `andbo-kube-runs`, `default` (still a warning,
  not a refusal) and `andbo-runs` — still renders. What is left uncovered is
  restated there.
- **An `agent.default` naming an adapter that does not exist was called valid by
  both gates and then killed the run.** `andbo policy check` printed
  `✓ Policy valid`, exit `0`, and `andbo doctor` reported `config: ✓ andbo.yaml
  valid`, for a policy `andbo run` and `andbo k8s render` refuse at exit `4` —
  a typo (`clade`), a case slip (`Custom`), or an explicit `default: ""` all got
  a clean bill of health from the two commands whose whole job is to catch that
  before anything executes.

  Both now resolve the adapter through `adapters.Get` — the *same* resolution
  `run` and `k8s render` perform, not a second list of names kept beside the
  registry — so the set of adapter **names** the gates accept cannot drift from
  the set those two resolve. `policy check` reports it through `check.Errors`, so
  it reaches `--json` and the human report alike under that command's existing
  invalid-policy exit `7`; doctor reports it on its `config` line and still
  exits `0`.

  Nothing else changed. `run` and `k8s render` keep their own exit `4` from
  `adapters.Get`, the rendered Kubernetes Job and NetworkPolicy bytes are
  identical, and every policy with a supported `agent.default` — including all
  four shipped `examples/*.yaml` — produces byte-identical output. `--agent NAME`
  still overrides a broken `agent.default` for a single run, because the check
  lives in the two gates and not in the validation `run` performs *before* it
  applies flag overrides.

  `andbo exec` is the one surface left out, and it now disagrees with the gates
  on purpose: it resolves no adapter — the caller supplies the command, so the
  caller is the agent — and runs one of these policies to completion at exit `0`
  while `policy check` calls the file invalid. Making `exec` refuse an agent it
  never consults would be a false alarm on the surface that needs none.

  The gate answers for the **name**, and only the name: `agent.default: custom`
  with an empty `agent.custom.command` still resolves — the failure lives in the
  adapter's `BuildCommand` — so it is still reported valid here and refused by
  `run` *and* `k8s render` at exit `4`, exactly like the name cases above. That
  is a second gap in a different field, left for its own
  milestone rather than folded into this one. It is unchanged from before.
- **`andbo doctor` reported `config: ✓ andbo.yaml valid` for policies every
  other command refuses.** It only asked whether the YAML decoded, so a
  `network.mode: bogus`, a `secrets.mode: env`, an empty `runtime.image`, or a
  negative budget each passed doctor and were then rejected as an invalid policy
  (exit `7`) by `policy check`, `run`, `exec`, and `k8s render` — as was a budget
  above what a run deadline can hold, which `policy check` reports under that
  same exit `7` while `run`, `exec`, and `k8s render` refuse it as a policy
  violation (exit `2`). Doctor is the command a user reaches for *after* a run
  fails, and its verdict sent them looking at Docker, at their agent, at anything
  but the file that was broken.

  Doctor now runs the validation `andbo policy check` runs — `config.Check` for
  malformed values, plus the upper bound on `budget.max_runtime_minutes` — and
  reports `config` as failing, naming every field to fix on one line.

  It still **diagnoses rather than gates**: `andbo doctor` exits `0` exactly as
  before, including on an invalid policy, so a setup script that runs it before
  `andbo init` does not start failing. No other command's validation changed, and
  the Kubernetes manifest contract is untouched. What is asserted is *agreement*:
  a rule added to `config.Check` later cannot drift away from doctor again
  without turning the test red.

  Doctor's verdict is `andbo policy check`'s, no wider. `andbo k8s render`
  refuses policies that are perfectly valid for `andbo run` — a budget above the
  1440-minute `activeDeadlineSeconds` cap, `runtime.isolation: local`,
  `network.mode` allowlist/open, an agent needing environment variables — and
  doctor deliberately does not report those: it is host-local and target-agnostic,
  and flagging them would make it cry wolf on the path most people are on.

- **A negative `budget.max_runtime_minutes` meant something different on every
  surface, and none of them was the bound it asked for.** A sign typo on the
  default budget — `-30` where `30` was meant — validated clean and then took
  three separate paths. `andbo run` and `andbo exec` gate the deadline on
  `> 0`, so the run executed with **no deadline at all**: not the 30 minutes
  written, and not an error either. `andbo k8s render` gates the same way, left
  `activeDeadlineSeconds` at the renderer's own `1800`, and emitted a clean
  manifest carrying a 30-minute bound the policy never expressed. `andbo policy
  check` — the gate a pipeline runs *before* any of that — printed
  `minutes=-30` and `✓ Policy valid`, exit `0`.

  A negative wall-clock budget is not a duration, so it is now an invalid policy
  and is refused by `policy check`, `run`, `exec`, and `k8s render` alike, with
  exit `7` and an error naming the field, the value written, and both ways out
  (a positive number of minutes, or `0` for no deadline). `0` is unchanged and
  still means "no deadline" for `run`/`exec` and the renderer's bounded default
  for `k8s render`.

  The check lives in `config.Check` rather than in each command, because that is
  the one validation all four surfaces already funnel through: the same four
  cannot drift apart again, which is how they came to disagree here in the first
  place. Putting it beside the existing upper-bound guard would not have worked —
  `k8s render` never calls that one, so the surface with the worst symptom would
  have kept it.

  It bounds only the bottom of the range; the top stays where it is, for two
  reasons worth writing down. `Check` is a method on the policy and does not know
  which file the policy came from, so it cannot say "lower it in andbo.yaml" the
  way the upper-bound refusal does — which is also why the message below names no
  file. And moving that guard would change a shipped exit code from `2` to `7`.
- **`andbo run` / `andbo exec`: `budget.max_runtime_minutes` overflowed into a
  window the policy never asked for.** The conversion to a deadline multiplied
  minutes by `time.Minute` unguarded. A `time.Duration` counts nanoseconds in an
  int64, so above 153,722,867 minutes the product wrapped. The wrap is cyclic,
  not ordered — `153722868`, the very first value over the bound, already lands
  far negative, while `9007199254740992` (2^53) lands on exactly **zero** — and
  every landing was silent. `153722867281` became `5.224192s`: the run was killed
  almost immediately and told it had "hit budget.max_runtime_minutes
  (153722867281)", a bound it was never given.

  Zero and negative windows were the worst shape, though the run never went
  *unbounded*: both commands derive the outer run context from the **minutes**, so
  a wrapped-to-zero window still produced an already-expired context and the run
  died at once. What disappeared was the deadline one layer down — `local.go` and
  `docker.go` gate on `if command.Timeout > 0` — leaving the outer context as the
  only bound. No layer delivered what the policy asked for.
  `andbo k8s render` already refused these; `run` and `exec` did not.

  Both commands now refuse a budget above the representable maximum with exit `2`
  — the same policy-violation code `k8s render` uses for a budget it cannot bound
  — naming the value, the maximum, and the file to change. The refusal lands
  before the unsafe confirmation, so nobody is asked to accept risk for a run that
  cannot start. `andbo policy check` reports the same budget as an error (under
  its own exit `7`), so the gate a pipeline runs *before* a run no longer passes a
  policy the run will reject.

  The conversion itself was made total in both directions. The negative half
  wrapped too, and wrapped the wrong way: `-9007199254740991` minutes multiplied
  out to a plausible **one-minute** window, which passes the runners' `Timeout > 0`
  gate and so reads as enforced. `0` is how the policy spells "no deadline", so
  that is now exactly what the conversion returns for anything at or below it.
  Unreachable through the commands, which gate on `> 0` — and a negative no
  longer reaches it at all (see below) — but it is there so a call site added
  later cannot resurrect the defect, which is how `run` and `exec` came to differ
  from `k8s render` in the first place.
- **Kubernetes renderer: host-workspace leak check matched substrings, not
  paths.** `FromRuntimeSpec` refused any argv containing the workspace path, via
  `strings.Contains`. That was harmless while the only caller passed a long,
  unique session directory, but `andbo k8s render` takes the workspace from the
  operator's working directory: from `/tmp/w`, every mention of an unrelated
  `/tmp/workspace` was reported as a host-path leak with an explanation that was
  not true, and a CI checkout at `/src` or `/work` made the image transport
  unusable. Matching is now anchored to path-segment boundaries on both sides;
  every real reference is still caught.
- **Kubernetes renderer: validation errors gave Go API advice to CLI users.**
  The trailing line said "Start from DefaultJobSpec() for secure defaults", which
  nobody can act on from a terminal now that `andbo k8s render` surfaces these.
- **Kubernetes renderer: `workingDir` reserved-path bypass.** The reserved
  mount-path check compared raw strings, so a non-canonical spelling such as
  `/work/../etc` walked past it while the kernel still resolved the mount to
  `/etc` — hiding the image's CA trust store and `/etc/passwd` behind an empty
  volume. `workingDir` must now be a clean absolute path; non-canonical spellings
  are rejected rather than silently canonicalised, so the rendered `mountPath` is
  always the string the caller supplied.
- **Kubernetes renderer: quantity validation accepted forms Kubernetes rejects.**
  `strconv.ParseFloat` is a strict superset of the Kubernetes quantity grammar,
  so hex floats (`0x1p10`), underscore separators (`1_000`), and an exponent
  combined with a suffix (`1e3Ki`) passed validation and failed later at apply
  time. They are now rejected at the boundary with an actionable error.

## v0.6.0 — 2026-06-13 (renamed to Andbo)

### Renamed
- **The project is now Andbo (formerly AgentBox).** All commands, the module
  path (`github.com/qosikz/andbo`), the binary (`andbo`), the config file
  (`andbo.yaml`), the state directory (`.andbo/`), environment variables
  (`ANDBO_*`), the published image (`ghcr.io/qosikz/andbo/runtime`), and the
  agent skill (`andbo-sandbox`) were renamed, along with the README banner and
  tagline ("Disposable sandboxes for AI coding agents"). Earlier changelog
  entries below are written with the new name for consistency; they shipped
  under the old name. The GitHub repository is now `qosikz/andbo` (the old URL
  redirects) and the runtime image is republished under
  `ghcr.io/qosikz/andbo/runtime` with this release.

### Changed
- The fallback commit identity used when a repo has no configured git identity
  (e.g. fresh CI runners) is now `QOSI Andbo <andbo@qosi.kz>` instead of
  `Andbo <andbo@localhost>` — an intentional, branded identity for
  agent-made commits.

## v0.5.0 — 2026-06-13 (configurable egress ports + adoption polish)

### Added
- **Configurable egress ports** (`network.ports`) for allowlist mode. Permit
  ports beyond the default 80/443 (e.g. an internal metrics endpoint on 8428).
  Empty/nil keeps the secure default {80,443} — backward compatible. The egress
  boundary is preserved at any port: still domain-allowlisted, still denies
  IP-literals and private/reserved/metadata CIDRs (anti-SSRF). Because CONNECT
  tunnels are protocol-agnostic, permitting a non-80/443 port widens egress to
  arbitrary TCP toward your allowlisted host:port — README/SECURITY now say so.
  Ports validated 1–65535 at the config layer and in the proxy. Security-reviewed
  (verdict: ship). *Originally prototyped by a Hermes agent dogfooding Andbo.*
- **Recipe guides** in [`recipes/`](recipes/): safe Claude Code, containerized
  Codex, MCP server quarantine, CI dry-run for untrusted PRs.
- Project **banner** + trust **badges** (release, CI, Go version, license,
  signed-releases, GHCR image), and a **"How it works"** trust-boundary diagram.
- **Blocked-exfiltration demo** (`demo/exfil-demo.sh` + `demo/blocked-exfil.gif`), the
  README hero: a sandboxed agent holds a live (fake) API key, reaches its one
  allowed API, but the attacker host is refused at the egress proxy (fail closed)
  and the saved audit record redacts the key — all real, no mocks. The fake key
  never appears on screen or in the recording.
- CONTRIBUTING "Common contributions" guides: adding an adapter, writing a
  security test, and authoring/recording a demo.

### Changed
- README first-run story rewritten: the Quickstart now leads with a real
  sandboxed `andbo exec` run (non-root, network-deny, diff, audit) against the
  auto-pulled default image; a new "Two ways to start" separates ready-now
  sandbox mechanics from the optional real-agent step. "How it works" moved below
  Quickstart and the exfiltration GIF is the single hero — leaner top.
- Honest-limitations updated to reflect that egress is 80/443 by default and that
  `network.ports` widens it to arbitrary TCP toward allowlisted host:port.

### Removed
- Internal planning/build/marketing material (`docs/`, `launch/`, `backlog/`,
  `claude-code/`, `.claude/`) removed from the public repo and purged from
  history; kept local and gitignored. No credentials were present.

## v0.4.1 — 2026-06-12 (zero-friction first run + supply-chain trust)

Adoption and trust packaging: a real `andbo run` now works with no setup, and
every release is verifiable.

### Added
- **Published default runtime image** `ghcr.io/qosikz/andbo/runtime:latest`
  (multi-arch linux/amd64+arm64, non-root, minimal Debian + `git` +
  `ca-certificates`). The default policy points at it and Docker/Podman pull it
  automatically — no image to build before the first container run.
- **Signed releases (Sigstore / keyless).** `checksums.txt` is signed with
  `cosign sign-blob`; verify with the published `.sig` + `.pem` and the workflow
  identity. The runtime image is signed by digest.
- **SBOM** (SPDX): a dependency SBOM for the release plus an image SBOM scanned
  from the published runtime image.
- **SLSA build provenance** attestations for the release binaries and the image
  (verifiable with `gh attestation verify`).
- `publish-image.yml` workflow; a "Verifying releases" guide in the README.

### Changed
- Default `runtime.image` repointed from the unpublished `andbo/default:latest`
  to the published GHCR image, across `andbo init`, examples, and docs.
- All GitHub Actions are pinned to commit SHAs (release, CI, and the composite
  action) — supply-chain hardening against tag-mutation. Release/publish jobs run
  with least-privilege `permissions` and OIDC (`id-token`/`attestations: write`).

### Fixed
- README status line said `v0.3.1` while the latest release was `v0.4.0`.

## v0.4.0 — 2026-06-12 (enforced network allowlist)

The flagship safety milestone: `network.mode: allowlist` is now **enforced**,
not advisory. A real agent run no longer needs `network: open` — allowlist the
provider's API domains and the agent can reach those and nothing else.

### Added
- **Egress enforcement** for container runs. Two cooperating mechanisms:
  1. The agent container's only interface is a per-run `--internal` container
     network with no default route — direct egress and external DNS fail
     closed, so traffic that ignores the proxy cannot leave at all.
  2. An egress-proxy sidecar (dual-homed onto the external network) is the only
     path out and forwards only HTTP(S) whose target host matches
     `network.allow`. Each entry covers the domain and its subdomains; ports
     80/443 only; IP-literal targets always denied; targets resolving to
     private/loopback/link-local ranges refused (anti-SSRF backstop).
- `internal/netproxy` + `cmd/netproxy`: a stdlib-only filtering forward proxy
  (HTTP CONNECT + absolute-form HTTP) with structured ALLOW/DENY audit lines.
  Static linux builds (amd64/arm64) are **embedded into the andbo binary**
  by `make build`/`make release` and run in the sidecar from the user's own
  runtime image — no extra image or binary to install.
- Every proxy ALLOW/DENY is harvested into the session: denials become policy
  events (`egress DENY connect host:443: ...`), allows become audit log lines.
- The proxy sidecar carries the same hardening as the agent container:
  non-root 10001, `--cap-drop ALL`, no-new-privileges, `--rm`, and mounts ONLY
  the proxy binary read-only — never the workspace.
- `andbo doctor` reports `egress-proxy` embed status (allowlist
  enforceability) per architecture.
- `examples/andbo.codex.yaml` now uses `network: allowlist` with
  `api.openai.com` — **no `--yes-unsafe` required** for a real agent run.

### Changed
- `EnforcedNetwork()` no longer collapses allowlist to deny for container
  isolation; `policy check` shows `allowlist (enforced: allowlist)` with an
  honest enforcement note. Local isolation still collapses (no container
  network to enforce with) and says so.
- Allowlist setup failure fails the run — enforcement never falls open. If the
  internal-network swap is somehow skipped, the container stays on the
  isolated `none` network (fail closed by construction).
- The network=deny honesty note now points to allowlist instead of open.

### Hardening (from adversarial review)
- Anti-SSRF backstop refuses an explicit reserved-range set (loopback, RFC1918,
  CGNAT 100.64/10, link-local/metadata, benchmarking, TEST-NETs, 240/4, ULA,
  NAT64 64:ff9b::/96, 6to4, Teredo, and IPv4-in-IPv6 forms) — not just
  `IsPrivate()`.
- DNS-tunnel exfiltration is closed **structurally**: the agent's resolver is
  sunk (`--dns 0.0.0.0`); the proxy resolves allowlisted names. Independent of
  the daemon's internal-network DNS behavior.
- Proxy egress leg uses a **dedicated per-run external network**, not the
  shared default bridge, so unrelated containers can't use it as an open relay.
- CONNECT tunnels have an idle deadline + a concurrency cap; the HTTP server has
  read/idle timeouts and a header-size cap; the sidecar runs read-only and
  PID-capped — bounding self-inflicted DoS of the egress path.
- Egress audit lines are classified by their verb field (not substring), and
  the dry-run plan only claims enforcement when the proxy is actually embedded.

### Limitations (honest)
- Container isolation only; `--runtime local` runs have no network enforcement.
- HTTP(S) only: SSH and raw TCP cannot leave the sandbox at all (fail closed).
- An allowlisted domain is a permitted channel by definition — keep the list
  minimal (an allowlisted DoH/DNS resolver would re-open a DNS channel).
- Verified end-to-end on Docker; podman uses identical CLI arguments but is
  less tested.

## v0.3.2 — 2026-06-12 (public-home prep)

### Changed
- **Module path renamed** `github.com/qosi/andbo` → `github.com/qosikz/andbo`
  to match the public home (the QOSI organization at github.com/qosikz). This
  updates `go.mod`, every import, the `ghcr.io/.../andbo` test fixtures, and
  the README install/clone/`go install` URLs, so `go install
  github.com/qosikz/andbo/cmd/andbo@latest` resolves once the repo is
  public. No runtime behavior change.

### Documentation
- README: added an "Add Andbo to your agent harness" quickstart near the top
  (copy-paste `skill install` + `exec` + the MCP one-liner) so harness users can
  wire in the sandbox in ~30 seconds. Status bumped to v0.3.1.

## v0.3.1 — 2026-06-11 (harness-focused)

### Removed
- The `aider` adapter. Aider's upstream activity has stalled (last release
  2025-08); Andbo now focuses on actively-maintained harnesses and the
  `custom` adapter, which runs any CLI agent. `agent: aider` is no longer a
  valid adapter name — use `custom` with `agent.custom.command: aider` if you
  still need it.

### Changed
- Documentation and positioning lead with **harness integration** — driving
  Andbo from Claude Code, OpenClaw, Hermes Agent, or any MCP/skill-capable
  harness (via `exec` / `mcp serve` / `skill`) — and with the `custom` adapter
  as the bring-your-own-agent path. `andbo doctor` now probes
  claude/codex/gemini/goose/opencode instead of aider.

## v0.3.0 — 2026-06-11 (containerized agents)

Run a coding agent fully inside the sandbox: bake its CLI into a runtime image
and let Andbo run it under policy, with credentials injected at runtime and
redacted from logs.

### Added
- **Baked-in agent support.** The agent CLI can now live only in the runtime
  image. Andbo preflights the agent by **probing the image** (a hardened,
  no-network, self-removing throwaway container) instead of the host PATH, so a
  containerized agent you have never installed locally runs correctly. A missing
  agent yields an actionable, image-aware error before anything executes.
- `examples/agents/` — worked runtime images and a guide:
  - `stub.Dockerfile` + `stub-agent.sh` — a zero-cost proof fixture that
    confirms a policy-injected key reaches the container and is redacted, with
    no API key and no network.
  - `codex.Dockerfile` — an illustrative image baking the OpenAI Codex CLI in
    (auth/sandbox setup is provider- and version-specific; the stub is the
    verified zero-cost path). Uses `CODEX_API_KEY`, matching the codex adapter.
  - `README.md` — the baked-in-agent model, runtime secret injection, and the
    honest network limitation.
- Example policies `examples/andbo.stub.yaml` and `examples/andbo.codex.yaml`.
- `runtime.Runner` gains `ProbeBinary(ctx, image, bin)` (implemented for
  docker/podman, local, and dry-run runners).

### Changed
- The agent preflight is now **isolation-aware**: local isolation checks the
  host PATH (as before); container isolation probes the image. Container
  dry-runs skip the probe entirely, so planning never needs a daemon — and the
  previous spurious "agent not found on PATH" warning for container dry-runs is
  gone.
- A real container agent run under `network=deny` with allowlisted secrets now
  records a note that the agent cannot reach a remote API (allowlist egress is
  still unenforced).

### Security
- Credentials for a baked-in agent are injected **only at runtime** under
  `secrets.allow` (deny still overrides allow; the secure defaults keep
  `OPENAI_API_KEY`/`ANTHROPIC_API_KEY`/`AWS_SECRET_ACCESS_KEY` out unless you
  remove them from `deny` deliberately) and are redacted from all recorded
  artifacts. No example image contains a key, an `ENV`, or an `ARG` for one.
- The image probe is unprivileged: `--cap-drop ALL`, `--security-opt
  no-new-privileges`, `--network none`, `--rm`, no mounts.

## v0.2.0 — 2026-06-11 (harness integration)

Andbo now works in BOTH directions: it sandboxes agents, and agent
harnesses use it as their safety sandbox.

### Added
- `andbo exec "<command>"` — run any command in an isolated, policy-
  controlled workspace with no agent adapter: the calling harness IS the
  agent. The sandboxed command's exit code passes through; `--json` returns
  exit_code, redacted output, changed files, and the session path.
- `andbo mcp serve` — a stdio MCP server (protocol 2025-11-25, with
  fallbacks) exposing `sandbox_exec`, `sandbox_run`, `scan_mcp`,
  `session_list`, and `session_show` to any MCP-capable harness (Claude Code,
  OpenClaw, Codex CLI, Gemini CLI, Goose, OpenCode). Unsafe modes are not
  reachable through MCP tools.
- `andbo skill install` — installs a cross-harness SKILL.md
  (agentskills.io-style) teaching the harness when to use the sandbox.
  Targets: claude-project, claude-user, openclaw, hermes, agents
  (~/.agents/skills), or `--dir`.
- Built-in adapters for popular coding agents: `claude` (Claude Code,
  `-p --permission-mode acceptEdits`), `codex` (`codex exec --sandbox
  workspace-write`), `gemini` (`--approval-mode auto_edit -p`), `goose`
  (`goose run --no-session -t` with `GOOSE_MODE=auto`), `opencode`
  (`opencode run`). `aider` remains as a community adapter (upstream activity
  has slowed; last release 2025-08).

### Fixed
- Local (unsafe) runs now forward `USER`/`LOGNAME` to the agent. OS keychains
  (e.g. Claude Code's macOS auth) and git's identity fallback require them;
  without them real agents failed to authenticate ("Not logged in").
  Containers still never receive the host `USER`.

### Verified
- End-to-end run with a real coding agent: Claude Code fixed a failing Go
  test under Andbo — tests re-run green, diff captured, branch committed
  and propagated, session recorded, workspace cleaned.

## v0.1.0 — 2026-06-11 (production preview)

### Added
- CLI: `init`, `run`, `policy check`, `mcp scan`/`mcp list`, `session list`/`show`/`replay`, `doctor`, `version`.
- YAML policy engine with secure defaults, strict loading, validation, and an
  `EffectivePolicy` (deny-overrides-allow, mandatory sensitive denies, unsafe-mode gating).
- Runtime abstraction: dry-run runner, Docker runner (non-root, never privileged,
  never mounts the Docker socket, `deny` → `--network none`), and an unsafe local runner.
- Agent adapters: `custom` (templated `{{ task }}`) and `aider`, with a registry
  and availability checks.
- Session recorder: full `session.json` schema plus `report.md`, redacted `logs.txt`,
  `diff.patch`, `policy-events.json`, `test-results.txt`, and `metadata.json`.
- Secret redaction: named env values plus built-in/extra regex patterns, applied to
  every persisted artifact.
- MCP Guard static scanner with stable rule IDs (shell, env/secrets, filesystem,
  network, database, git, prompt-injection), human and JSON output, exit 2 on unsafe.
- Git integration: repo detection, diff (including agent-created files), changed-file
  attribution, branch/commit, remote normalization/clone, and `gh`-based PR creation.
- Disposable, sanitized workspace copy (denied files excluded; `.git` preserved).
- Unsafe-mode confirmation flow (interactive prompt / `--yes-unsafe` for CI; exit 8).
- GitHub composite action and a safe, dry-run-by-default example workflow.
- Security acceptance tests mapping to the threat model (§8).

### Production hardening
- Podman support: `runtime.engine: podman` in the policy or the new
  `andbo run --engine docker|podman` flag.
- Container hardening: containers run as a non-root user with `--cap-drop ALL`
  and `--security-opt no-new-privileges`; never privileged, never the Docker socket.
- Environment hygiene: host `PATH`/`HOME` are never forwarded into containers;
  containers get a standard `PATH`, `HOME` set to the workspace, `LANG`/`TERM`,
  and explicitly allowlisted secrets only.
- `budget.max_runtime_minutes` is enforced as a hard deadline on real runs
  (dry-run is unaffected).
- `runtime.cleanup` is honored: the disposable workspace copy is removed after
  the run; `cleanup: false` keeps it for debugging. Session artifacts are always kept.
- Versioned builds: `make build` embeds version/commit/date via `-ldflags`;
  `make release` cross-compiles darwin/linux × amd64/arm64 with SHA-256 checksums.
- Release workflow: pushing a `v*` tag publishes prebuilt binaries to GitHub Releases.

### Known limitations
- `network: allowlist` is not enforced yet (falls back to `deny`, advisory list).
- `commands.deny` is best-effort; `budget` USD/token caps depend on adapter support.
- Real container execution requires a runtime image you build (none published yet).
- Secret redaction is best-effort and may miss unknown formats.
