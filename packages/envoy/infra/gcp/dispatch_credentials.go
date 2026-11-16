package gcp

import (
	"github.com/pulumi/pulumi-gcp/sdk/v10/go/gcp/secretmanager"
	corev1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	random "github.com/pulumi/pulumi-random/sdk/v4/go/random"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// serviceTokenSeconds is the lifetime of each projected ServiceAccount token the app and the
// listener present: envoy-dispatch's to the secrets broker and to its listener, and both pods' to
// the auth service. The broker and the auth service refuse a token living longer than an hour from
// its issue, so the kubelet mints each for exactly that and rewrites it at 80% of that life, 48
// minutes, ahead of the 50 minutes at which the bus moves to a later token
// (natscred.ServiceRenewBefore).
const serviceTokenSeconds = 3600

// Where the pods' credentials are mounted, each volume named for its one file. Both Envoy
// containers mount their NATS credential at dispatchCredentialMount: the seed, the one file of its
// SecretProviderClass, which NATS_NKEY_SEED_FILE names, or, with natsAudience, the projected token
// of their ServiceAccount for it, which NATS_SERVICE_TOKEN_FILE names. The app's token for its
// listener is another projected token, for listenerAudience, in a volume of its own, which
// ENVOY_ACCOUNT_TOKEN_FILE names.
const (
	dispatchCredentialMount    = "/var/run/secrets/envoy"
	dispatchNatsSeedName       = "nats-seed"
	dispatchNatsSeedFile       = dispatchCredentialMount + "/" + dispatchNatsSeedName
	dispatchNatsTokenName      = "nats-token"
	dispatchNatsTokenFile      = dispatchCredentialMount + "/" + dispatchNatsTokenName
	dispatchListenerTokenMount = "/var/run/secrets/envoy-listener"
	dispatchListenerTokenName  = "listener-account-token"
	dispatchListenerTokenFile  = dispatchListenerTokenMount + "/" + dispatchListenerTokenName
)

// projectedTokenVolume is a volume holding one file, name, the pod's own ServiceAccount token for
// audience, which the kubelet mints for serviceTokenSeconds and rewrites in place before it
// expires, so nothing stores the token and no rotation restarts a pod.
func projectedTokenVolume(name, audience string) corev1.VolumeArgs {
	return corev1.VolumeArgs{Name: pulumi.String(name), Projected: &corev1.ProjectedVolumeSourceArgs{Sources: corev1.VolumeProjectionArray{
		corev1.VolumeProjectionArgs{ServiceAccountToken: &corev1.ServiceAccountTokenProjectionArgs{Audience: pulumi.String(audience), ExpirationSeconds: pulumi.Int(serviceTokenSeconds), Path: pulumi.String(name)}},
	}}}
}

// dispatchNATSCredential is the one NATS credential the app's envoy-dispatch and the listener's
// envoy-listener connect with, prepared once (newDispatchNATSCredential) and consumed whole by
// both pods: each mounts volume at mount, and each names the file in its environment.
type dispatchNATSCredential struct {
	// serviceTokenFile is the listener's core.ListenerConfig.ServiceTokenFile, which core's Env
	// renders as NATS_SERVICE_TOKEN_FILE, and listenerEnv what follows core's rendering in the
	// listener's environment. appEnv is the app's one variable, in the seed's slot of its list.
	serviceTokenFile string
	listenerEnv      corev1.EnvVarArray
	appEnv           corev1.EnvVarArgs
	volume           corev1.VolumeArgs
	mount            corev1.VolumeMountArgs
	// grants are what both Deployments wait for before a pod can read the credential.
	grants []pulumi.Resource
}

// newDispatchNATSCredential prepares the pods' NATS credential. Without natsAudience it is the
// seed: a SecretProviderClass of natsSeedSecret (secretClass), mounted through the CSI driver,
// which a pod can read only once its principal holds roles/secretmanager.secretAccessor on the
// secret, so the grant is registered here and both Deployments wait for it rather than racing IAM
// on the first deployment; the grant waits for the cluster, whose pool the principal lives in.
// With natsAudience it is the pods' projected token for that audience, which needs no class and no
// grant. A deployment without natsAudience renders what it always rendered.
func newDispatchNATSCredential(ctx *pulumi.Context, comp *Dispatch, dispatch string, args *DispatchArgs, s dispatchSettings, cloud *dispatchCluster, principal pulumi.StringOutput, secretClass func(name string, secret pulumi.StringInput, input, file string) error) (dispatchNATSCredential, error) {
	if s.natsAudience != "" {
		return dispatchNATSCredential{
			serviceTokenFile: dispatchNatsTokenFile,
			appEnv:           valueEnv("NATS_SERVICE_TOKEN_FILE", dispatchNatsTokenFile),
			volume:           projectedTokenVolume(dispatchNatsTokenName, s.natsAudience),
			mount:            credentialMount(dispatchNatsTokenName),
		}, nil
	}
	grant, err := secretmanager.NewSecretIamMember(ctx, s.hostname+"-nats-seed-accessor", &secretmanager.SecretIamMemberArgs{SecretId: refuseDispatchSeed(args.NatsSeedSecret, dispatch, "natsSeedSecret"), Role: pulumi.String("roles/secretmanager.secretAccessor"), Member: principal}, pulumi.Parent(comp), pulumi.DependsOn([]pulumi.Resource{cloud.cluster}))
	if err != nil {
		return dispatchNATSCredential{}, err
	}
	seedName := s.hostname + "-nats-seed"
	if err := secretClass(seedName, args.NatsSeedSecret, "natsSeedSecret", dispatchNatsSeedName); err != nil {
		return dispatchNATSCredential{}, err
	}
	return dispatchNATSCredential{
		listenerEnv: corev1.EnvVarArray{valueEnv("NATS_NKEY_SEED_FILE", dispatchNatsSeedFile)},
		appEnv:      valueEnv("NATS_NKEY_SEED_FILE", dispatchNatsSeedFile),
		volume:      corev1.VolumeArgs{Name: pulumi.String(dispatchNatsSeedName), Csi: &corev1.CSIVolumeSourceArgs{Driver: pulumi.String("secrets-store-gke.csi.k8s.io"), ReadOnly: pulumi.Bool(true), VolumeAttributes: pulumi.StringMap{"secretProviderClass": pulumi.String(seedName)}}},
		mount:       credentialMount(dispatchNatsSeedName),
		grants:      []pulumi.Resource{grant},
	}, nil
}

// credentialMount mounts the NATS credential's volume, name, read-only at
// dispatchCredentialMount.
func credentialMount(name string) corev1.VolumeMountArgs {
	return corev1.VolumeMountArgs{Name: pulumi.String(name), MountPath: pulumi.String(dispatchCredentialMount), ReadOnly: pulumi.Bool(true)}
}

// dispatchListenerCredential is how the app calls its listener, prepared once
// (newDispatchListenerCredential) and consumed whole by both pods: the app's variable, after
// ENVOY_URL, with what it mounts, and the end of the listener's environment, its API credential.
type dispatchListenerCredential struct {
	appEnv     corev1.EnvVarArgs
	appMounts  corev1.VolumeMountArray
	appVolumes corev1.VolumeArray
	// apiEnv is the listener's API credential, rendered once the cluster's issuer resolves.
	apiEnv func(issuer string) corev1.EnvVarArray
	// checksums are the component Secrets both pods read it from (secretChecksum).
	checksums map[string]*corev1.Secret
}

// newDispatchListenerCredential prepares the app's credential for its listener, whose
// Deployment, Service and bearer share listenerName. The listener serves on every address of its
// pod, and its API refuses to start on a non-loopback listen host without a bearer or an OIDC pair
// (packages/envoy/cmd/listener/apiauth.go).
//
// Without listenerAudience it is a bearer the two share: a generated password kept in a Secret of
// its own, which the listener reads as ENVOY_API_TOKEN and the app as ENVOY_TOKEN, never a key of
// the component's dispatch Secret, which holds DATABASE_URL and DISPATCH_SIGNING_KEY. The Secret's
// checksum is on both pods. With listenerAudience the app presents its own token instead: the
// kubelet mints it for listenerAudience into a projected volume mounted read-only into
// envoy-dispatch alone, ENVOY_ACCOUNT_TOKEN_FILE names the file Dispatch reads at call time, and
// the listener verifies it with the pair ENVOY_OIDC_ISSUER, this cluster's issuer, which mints it,
// and ENVOY_OIDC_AUDIENCE. No bearer is shared then, so there is no password, no Secret and no
// checksum, and Dispatch refuses ENVOY_TOKEN beside the file.
func newDispatchListenerCredential(ctx *pulumi.Context, comp *Dispatch, s dispatchSettings, listenerName string, metadata func(string) *metav1.ObjectMetaArgs, kube []pulumi.ResourceOption) (dispatchListenerCredential, error) {
	if s.listenerAudience != "" {
		return dispatchListenerCredential{
			appEnv:     valueEnv("ENVOY_ACCOUNT_TOKEN_FILE", dispatchListenerTokenFile),
			appMounts:  corev1.VolumeMountArray{corev1.VolumeMountArgs{Name: pulumi.String(dispatchListenerTokenName), MountPath: pulumi.String(dispatchListenerTokenMount), ReadOnly: pulumi.Bool(true)}},
			appVolumes: corev1.VolumeArray{projectedTokenVolume(dispatchListenerTokenName, s.listenerAudience)},
			apiEnv: func(issuer string) corev1.EnvVarArray {
				return corev1.EnvVarArray{valueEnv("ENVOY_OIDC_ISSUER", issuer), valueEnv("ENVOY_OIDC_AUDIENCE", s.listenerAudience)}
			},
		}, nil
	}
	token, err := random.NewRandomPassword(ctx, s.hostname+"-listener-token", &random.RandomPasswordArgs{Length: pulumi.Int(48), Special: pulumi.Bool(false)}, pulumi.Parent(comp))
	if err != nil {
		return dispatchListenerCredential{}, err
	}
	secret, err := corev1.NewSecret(ctx, listenerName, &corev1.SecretArgs{
		Metadata: metadata(listenerName), StringData: pulumi.StringMap{"ENVOY_API_TOKEN": token.Result},
	}, kube...)
	if err != nil {
		return dispatchListenerCredential{}, err
	}
	return dispatchListenerCredential{
		appEnv: secretEnv("ENVOY_TOKEN", listenerName, "ENVOY_API_TOKEN"),
		apiEnv: func(string) corev1.EnvVarArray {
			return corev1.EnvVarArray{secretEnv("ENVOY_API_TOKEN", listenerName, "ENVOY_API_TOKEN")}
		},
		checksums: map[string]*corev1.Secret{listenerName: secret},
	}, nil
}
