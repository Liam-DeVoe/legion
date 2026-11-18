package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/ghconfig"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
)

// gitHubRefreshInterval is how often refreshGitHubCredentials walks the pod store. The daemon's
// function (Options.GitHubCredential) holds each App's lease and re-mints it as the lease nears its
// expiry, so most ticks render the hosts.yml the Secret already holds and write nothing; a minute
// keeps the time a role's gh runs on a token the daemon has already replaced short against an
// installation token's hour, at one Secret read per live role. A runtime reads it once, as it starts
// (start); a test that must see a tick inside its run shortens it before starting one.
var gitHubRefreshInterval = time.Minute

// refreshGitHubCredentialsEvery runs refreshGitHubCredentials, then refreshCacheTokens, then
// readCacheHealth, every interval until ctx ends: the informers' lifetime (start), so the walk never
// reads a store nothing feeds.
func (r *Runtime) refreshGitHubCredentialsEvery(ctx context.Context, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.refreshGitHubCredentials(ctx)
			r.refreshCacheTokens(ctx)
			r.readCacheHealth(ctx)
		}
	}
}

// refreshGitHubCredentials brings the role Secrets of every live pod to the current render of each
// role's GitHub credential, so the gh files its container projects (GHConfigDir) carry the tokens
// the daemon holds now: the kubelet rewrites the projection in place once the Secret changes. It
// walks the pod store for the pods of this project (every launcher role holds its App's gh
// bundle, the controller's included; a repository cache's pod names no kind and holds
// none, so it is passed over, its token refreshCacheTokens') that are Pending or Running and not
// being deleted —
// a pod waiting to be scheduled starts its launchers from the Secret as it is then — and, for each
// of the kind's roles, reads the role's Secret, skips one another Sandbox owns (the pod's Sandbox
// replaced since the pod was listed), renders the credential for the pod's own repository, and
// rewrites only the directories whose hosts.yml is not the render. A Secret that records no
// default (one an older runtime wrote, whose pod the next launch replaces),
// a render that fails, or one whose default is not the Secret's is logged and the role keeps its
// last tokens until the next tick, and a Secret whose default is empty (a controller whose project
// had no GitHub owner when its pod started, whose gh directory is an empty one) has nothing to
// refresh until its next pod; a write that conflicts is retried once on a fresh read. Nothing
// of the Secret but the rewritten directories' gh keys changes: the launcher token, whose hash
// binds the launcher (credentialFromSecret), the default, the annotations, and the keys of a
// directory the render no longer holds, which the pod's projection may still name, stay as they
// are. Every live issue pod's provisioning Secret is brought to the implement App's current
// lease for the pod's own repository too (refreshProvisioningToken), whatever its
// workspace-fetch has already done: the kubelet runs a pod's init containers afresh whenever it
// rebuilds the pod's sandbox, at any point in the pod's life (a relaunch that replaces the pod
// provisions it anew instead), and the fetch re-reads the token's file before every transfer, so
// a pod left on its boot token, aged past the hour GitHub gives an installation token, could not
// fetch its repository again. The lease is minted once per repository a tick, however many of
// that repository's pods the walk holds, and a repository whose mint fails is logged once and
// left to the next tick, where its pods are tried again.
func (r *Runtime) refreshGitHubCredentials(ctx context.Context) {
	// mints memoizes this tick's provisioning leases by lower-cased repository — nothing here
	// knows of a configured repository rename, which the daemon's adapter maps, so Acme/legion
	// and acme/legion are one call — and records a mint that failed as well as one that worked,
	// so a repository is asked at most once a tick whatever the answer. A lease names the one
	// repository its pod clones, so two repositories of one owner are two mints. The walk is one
	// goroutine, so the map needs no lock.
	type mint struct {
		token string
		ok    bool
	}
	mints := map[string]mint{}
	provisioning := func(repo ghrepo.Repository) (string, bool) {
		key := strings.ToLower(repo.String())
		if m, asked := mints[key]; asked {
			return m.token, m.ok
		}
		minting, cancel := call(ctx)
		token, err := r.tokens.Token(minting, repo)
		cancel()
		if err != nil {
			r.log.Warn("sandbox runtime: provisioning token mint failed", "repo", repo.String(), "err", err)
		}
		mints[key] = mint{token: token, ok: err == nil}
		return token, err == nil
	}
	// Each slot holds the App of the step its claim does now (slotAppRole), read from the stored
	// claims once a tick. A tick that cannot read them refreshes no slot of an issue pod, each of
	// which keeps its last tokens until the next tick, rather than mint a slot for its name.
	held, heldErr := r.slotHolders(ctx)
	if heldErr != nil {
		r.log.Warn("sandbox runtime: github credential refresh cannot read the stored claims; issue pods keep their tokens until the next tick", "err", heldErr)
	}
	for _, obj := range r.pods.GetStore().List() {
		pod := obj.(*corev1.Pod)
		kind, err := podKindOf(pod.Labels)
		if err != nil {
			continue
		}
		if phase := pod.Status.Phase; (phase != corev1.PodPending && phase != corev1.PodRunning) || pod.DeletionTimestamp != nil {
			continue
		}
		owner := metav1.GetControllerOf(pod)
		if owner == nil {
			continue
		}
		// The pod's own repository is read once here, for every role of it and for its
		// provisioning token: it is the pod's, not the role's, and a bundle is rendered for it
		// (refreshGitHubCredential).
		repo, named := fetchRepository(pod)
		for _, slot := range kind.roles() {
			if heldErr != nil && slot != claim.RoleController {
				continue
			}
			role, err := r.slotAppRole(ctx, held, pod.Name, slot)
			if err != nil {
				r.log.Warn("sandbox runtime: github credential refresh failed", "sandbox", pod.Name, "slot", slot, "err", err)
				continue
			}
			r.refreshGitHubCredential(ctx, pod, owner.UID, slot, role, repo)
		}
		if _, issue := kind.(issuePod); !issue || !named {
			continue
		}
		token, ok := provisioning(repo)
		if !ok {
			continue
		}
		r.refreshProvisioningToken(ctx, pod, owner.UID, repo, token)
	}
}

// refreshProvisioningToken writes token — the implement App's current lease for repo, the
// repository the fetch of pod names (fetchRepository), which the walk minted once for that
// repository this tick — into the provisioning Secret of pod, owned by the Sandbox with uid.
// The fetch reads the token's file anew before every transfer from GitHub (workspace.Fetch),
// and the kubelet rewrites the projected file within its sync period, so a fetch that starts
// long after the pod did, one the kubelet runs again after it rebuilds the pod's sandbox
// included, never presents an expired token. Only the token is written; the cache's token
// beside it stays. A Secret another Sandbox owns, or one that already holds the lease, is left;
// a write that fails is logged and tried at the next tick, and a write that conflicts is
// retried once on a fresh read.
func (r *Runtime) refreshProvisioningToken(ctx context.Context, pod *corev1.Pod, uid types.UID, repo ghrepo.Repository, token string) {
	name := secretName(pod.Name)
	failed := func(err error) {
		r.log.Warn("sandbox runtime: provisioning token refresh failed", "sandbox", pod.Name, "repo", repo.String(), "err", err)
	}
	secrets := r.kube.CoreV1().Secrets(r.namespace)
	for retried := false; ; retried = true {
		secret, err := secrets.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			failed(err)
			return
		}
		if !ownedBySandbox(secret.OwnerReferences, uid) || string(secret.Data[provisionTokenKey]) == token {
			return
		}
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		secret.Data[provisionTokenKey] = []byte(token)
		_, err = secrets.Update(ctx, secret, metav1.UpdateOptions{})
		if err == nil {
			r.log.Info("sandbox runtime: provisioning token refreshed", "sandbox", pod.Name, "repo", repo.String())
			return
		}
		if !apierrors.IsConflict(err) || retried {
			failed(err)
			return
		}
	}
}

// refreshGitHubCredential is one slot's refresh (refreshGitHubCredentials): the slot's Secret in
// the pod, owned by the Sandbox with uid, minted for role, the role whose App the slot holds
// (slotAppRole), and rendered for repo, the pod's own repository its caller read off its workspace
// fetch once for every slot of the pod.
//
// A workflow role's credential is rendered for that repository, and not for the Secret's
// recorded default, which names an owner and so could never name a repository: a bundle
// rendered from an owner alone would mint nothing, every mint now naming the repositories it
// reaches. A pod the previous daemon created carries no repository in its Secret and needs
// none, since its fetch argument names it, so its refresh keeps working and no live issue is
// stopped. What such a pod lacks is the repository directories of a bundle built now, which a
// pod's gh projection is fixed at and only a new pod carries. The drift check does not answer
// that: movedGhDirs runs the directories the pod holds and the directories a bundle built now
// would hold alike through ghOwnerDirs, which keeps the names holding no slash, so a
// repository directory a pod lacks is no address of its own and opens no update round. An
// owner directory the pod lacks is what opens one, and this refresh writes the tokens of the
// directories the pod does project, whatever else a bundle built now would hold.
//
// The controller's is fetched by owner instead (Options.ControllerCredential), the one its
// Secret records: it works no repository, so a bundle asked for by repository would take its
// default from the first owner the project serves, sorted, and a project that gained an owner
// sorting before the recorded one would have every tick of its running controller refused, its
// tokens expiring within the hour, until something replaced the pod. Its launch asks for no
// owner, so the default the Secret records is the launch's own.
//
// The Secret's recorded default is what the bundle is held to either way: a render whose default
// is another owner is refused rather than written, since the pod's GH_CONFIG_DIR was fixed at
// that directory.
func (r *Runtime) refreshGitHubCredential(ctx context.Context, pod *corev1.Pod, uid types.UID, slot, role claim.Role, repo ghrepo.Repository) {
	if _, err := r.writeSlotCredential(ctx, pod, uid, slot, role, repo); err != nil && !errors.Is(err, errSecretNotOwned) {
		r.log.Warn("sandbox runtime: github credential refresh failed", "sandbox", pod.Name, "slot", slot, "role", role, "err", err)
	}
}

// errSecretNotOwned is a slot Secret another Sandbox owns: the pod's Sandbox was replaced since the
// pod was read, so the write is the new Sandbox's to make.
var errSecretNotOwned = errors.New("the slot's Secret is another Sandbox's")

// slotCredential is what a slot's Secret holds after its write (writeSlotCredential): its default
// owner, the token that owner's hosts.yml holds, and the App that minted it. An empty owner is a
// slot whose gh acts as nobody, which holds no token.
type slotCredential struct {
	owner, token, app string
}

// writeSlotCredential is the one-slot write, shared by the refresher (refreshGitHubCredential) and
// a launch into a pod it keeps (writeLaunchCredential): slot's Secret in pod, owned by the Sandbox
// with uid, minted for role and rendered for repo, rewritten only in the directories whose
// hosts.yml is not the render. It answers what the Secret holds then, whether this call wrote it
// or found it already so, as its doc above (refreshGitHubCredential) says of each step.
func (r *Runtime) writeSlotCredential(ctx context.Context, pod *corev1.Pod, uid types.UID, slot, role claim.Role, repo ghrepo.Repository) (slotCredential, error) {
	sandbox := pod.Name
	name := roleSecretName(sandbox, slot)
	secrets := r.kube.CoreV1().Secrets(r.namespace)
	read := func() (*corev1.Secret, error) {
		return secrets.Get(ctx, name, metav1.GetOptions{})
	}
	secret, err := read()
	if err != nil {
		return slotCredential{}, err
	}
	if !ownedBySandbox(secret.OwnerReferences, uid) {
		return slotCredential{}, errSecretNotOwned
	}
	recorded, ok := secret.Data[GitHubDefaultKey]
	if !ok {
		return slotCredential{}, fmt.Errorf("Secret %s records no %s", name, GitHubDefaultKey)
	}
	owner := string(recorded)
	if owner == "" {
		return slotCredential{}, nil
	}
	minting, cancel := call(ctx)
	var bundle ghconfig.Bundle
	if slot == claim.RoleController {
		bundle, err = r.controllerCredential(minting, owner)
	} else {
		bundle, err = r.gitHubCredential(minting, role, repo)
	}
	cancel()
	if err != nil {
		return slotCredential{}, err
	}
	if bundle.Default != owner {
		return slotCredential{}, fmt.Errorf("the credential's default owner %s is not the Secret's %s", bundle.Default, owner)
	}
	written := bundle.Dirs[owner]
	token, err := ghconfig.TokenFromHosts([]byte(written.Hosts))
	if err != nil {
		return slotCredential{}, fmt.Errorf("the credential's %s: %w", owner, err)
	}
	held := slotCredential{owner: owner, token: token, app: written.App}
	var changed []string
	for _, dir := range bundle.DirNames() {
		if string(secret.Data[GitHubHostsKey(dir)]) != bundle.Dirs[dir].Hosts {
			changed = append(changed, dir)
		}
	}
	if len(changed) == 0 {
		return held, nil
	}
	for retried := false; ; retried = true {
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		for _, dir := range changed {
			rendered := bundle.Dirs[dir]
			secret.Data[GitHubHostsKey(dir)], secret.Data[GitHubConfigKey(dir)] = []byte(rendered.Hosts), []byte(rendered.Config)
		}
		_, err = secrets.Update(ctx, secret, metav1.UpdateOptions{})
		if err == nil {
			break
		}
		if !apierrors.IsConflict(err) || retried {
			return slotCredential{}, err
		}
		// Something else wrote the Secret since it was read — a launch binding it to its new pod
		// (bindLauncherSecrets) — so its write is kept and the changed directories' keys set over
		// a fresh read.
		if secret, err = read(); err != nil {
			return slotCredential{}, err
		}
		if !ownedBySandbox(secret.OwnerReferences, uid) {
			return slotCredential{}, errSecretNotOwned
		}
	}
	r.log.Info("sandbox runtime: github credential refreshed", "sandbox", sandbox, "slot", slot, "role", role, "app", written.App,
		"expiresAt", written.ExpiresAt, "dirs", changed)
	return held, nil
}

// writeLaunchCredential writes the slot of spec's claim in pod, a pod the launch keeps, for the App
// of the step the claim does now (runtime.SlotRole), at once and with the one-slot write
// (writeSlotCredential), and answers what the slot's gh files must hold before the child starts:
// the default owner's hosts.yml and the digest of its token. A slot whose gh acts as nobody needs
// nothing. The kubelet projects the Secret anew within its sync period, and the launcher waits for
// it; so that no wait is silent, the start logs the wait whenever the slot's files may not hold its
// credential yet: at the first start this runtime makes in the slot, and at each start of another
// App than the slot's last, whether this write or the refresher's changed the Secret.
func (r *Runtime) writeLaunchCredential(ctx context.Context, pod *corev1.Pod, spec runtime.SpawnSpec) (*shimwire.LauncherGitHubCredential, error) {
	owner := metav1.GetControllerOf(pod)
	if owner == nil {
		return nil, fmt.Errorf("pod %s names no Sandbox that owns it", pod.Name)
	}
	slot := slotOf(spec.Claim)
	role, err := runtime.SlotRole(ctx, r.actsAs, spec.Claim, spec.Role)
	if err != nil {
		return nil, err
	}
	repo, _ := fetchRepository(pod)
	held, err := r.writeSlotCredential(ctx, pod, owner.UID, slot, role, repo)
	if err != nil {
		return nil, err
	}
	if held.owner == "" {
		return nil, nil
	}
	key := slotKey{sandbox: pod.Name, slot: slot}
	r.launchedMu.Lock()
	last, started := r.launchedApp[key]
	r.launchedApp[key] = held.app
	r.launchedMu.Unlock()
	if !started || last != held.app {
		r.log.Info("sandbox runtime: the start waits for its slot's gh files to hold its credential",
			"claim", spec.Claim, "slot", slot, "app", held.app, "generation", spec.Generation)
	}
	sum := sha256.Sum256([]byte(held.token))
	return &shimwire.LauncherGitHubCredential{HostsFile: held.owner + "/" + ghconfig.HostsFile, TokenDigest: hex.EncodeToString(sum[:])}, nil
}

// slotKey names one slot of one pod: the Sandbox the pod is named for, and the slot.
type slotKey struct {
	sandbox string
	slot    claim.Role
}

// slotHolder is the stored claim that runs in a slot: its token and its stored role, the role of
// the step its agent does now.
type slotHolder struct {
	token claim.Token
	role  claim.Role
}

// slotHolders is every stored claim by the Sandbox its issue names (SandboxName) and the slot it
// runs in (slotOf), read in one call of the store.
func (r *Runtime) slotHolders(ctx context.Context) (map[slotKey]slotHolder, error) {
	claims, err := r.store.Claims(ctx)
	if err != nil {
		return nil, err
	}
	held := make(map[slotKey]slotHolder, len(claims))
	for _, c := range claims {
		// A claim the store holds no role for (a row written before claims recorded one) names no
		// slot's App, so its slot keeps its own.
		if slot := slotOf(c.Token); slot != "" && c.Role != "" {
			held[slotKey{sandbox: SandboxName(c.Token), slot: slot}] = slotHolder{token: c.Token, role: c.Role}
		}
	}
	return held, nil
}

// slotAppRole is the role whose App slot of the pod of sandbox holds: runtime.SlotRole of the
// stored claim that runs in it, which is that claim's stored role unless the runtime's ActsAs gives
// another, and the slot's own name where no stored claim runs in it. It is never the slot's name
// for a claim that holds another role: a keyed agent's slot is named by its agent.
func (r *Runtime) slotAppRole(ctx context.Context, held map[slotKey]slotHolder, sandbox string, slot claim.Role) (claim.Role, error) {
	h, ok := held[slotKey{sandbox: sandbox, slot: slot}]
	if !ok {
		return slot, nil
	}
	return runtime.SlotRole(ctx, r.actsAs, h.token, h.role)
}
