package holdtest

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sjawhar/envoy/infra/core"
	"github.com/sjawhar/envoy/internal/testnats"
)

// ChartNatsConf is nats.conf as the nats chart 2.15.1 renders it into ConfigMap envoy-nats-config
// for the clustered hub (testdata/chart-nats.conf: helm template with gcp.clusteredHubChartValues'
// values, the callout's cluster ping merge included), with t-13's route authorization: the route
// URLs and password as variables the pods take from a secretKeyRef on Secret envoy-nats-route. Its
// server_name is $SERVER_NAME, which each pod resolves from its own name, and its route password a
// secret the operator cannot read, so the reader skips the digest equality for every pod, as it
// must after t-13. The stack installs it (InstallRender), and the writer hold's render rows read it.
//
//go:embed testdata/chart-nats.conf
var ChartNatsConf string

// AuthServiceUser is the auth service's own user as core renders it for keys: an nkey user granted
// the callout's subjects alone, subscribe $SYS.REQ.USER.AUTH and publish under $SYS._INBOX.
func AuthServiceUser(keys testnats.CalloutKeys) core.User {
	return core.User{
		Name: "envoy-auth", NKey: keys.ServicePublic,
		Publish:   &core.Rules{Allow: []string{core.CalloutReplyPrefix + ">"}},
		Subscribe: &core.Rules{Allow: []string{core.CalloutRequestSubject}},
	}
}

// AuthConf is auth.conf as core.Authorization renders it for the clustered hub (gcp
// clusteredHubSettings' authConf) behind keys' auth service: the auth service's user first, then
// users, with the auth callout.
func AuthConf(t testing.TB, keys testnats.CalloutKeys, users ...core.User) string {
	t.Helper()
	a := core.Authorization{
		Users:   append([]core.User{AuthServiceUser(keys)}, users...),
		Callout: &core.AuthCallout{Issuer: keys.IssuerPublic, XKey: keys.XKeyPublic, ServiceUser: "envoy-auth"},
	}
	text, err := a.Render([]string{"auth.conf of the writer hold's chart fixture"})
	if err != nil {
		t.Fatalf("holdtest: render auth.conf: %v", err)
	}
	return text
}

// RenderConfigMap is ConfigMap envoy-nats-config holding natsConf and authConf, at
// resourceVersion 4711.
func RenderConfigMap(natsConf, authConf string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: ConfigMap, Namespace: Namespace, ResourceVersion: "4711"},
		Data:       map[string]string{"nats.conf": natsConf, "auth.conf": authConf},
	}
}

// ChartEnv is the nats container's env as the chart renders it for replicas pods (POD_NAME from the
// pod's name, SERVER_NAME from it, the hub's GOMEMLIMIT), with t-13's route credentials from Secret
// envoy-nats-route: the password from its key route-1, and the route URLs, which carry it, built
// from it by Kubernetes' $(NAME) expansion. The URLs' shape is this fixture's own reading of t-13
// (plan v62 line 1007), which names the two variables and their secretKeyRef source.
func ChartEnv(replicas int) []corev1.EnvVar {
	urls := make([]string, replicas)
	for i := range urls {
		urls[i] = fmt.Sprintf(`"nats://route:$(NATS_ROUTE_PASSWORD)@%s.%s-headless:6222"`, Pod(i), StatefulSet)
	}
	return []corev1.EnvVar{
		{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
		{Name: "SERVER_NAME", Value: "$(POD_NAME)"},
		{Name: "GOMEMLIMIT", Value: "1638MiB"},
		{Name: "NATS_ROUTE_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "envoy-nats-route"}, Key: "route-1"}}},
		{Name: "NATS_ROUTE_URLS", Value: "[" + strings.Join(urls, ", ") + "]"},
	}
}

// ChartPodSpec is the nats chart 2.15.1's pod spec of StatefulSet envoy-nats
// (templates/stateful-set.yaml): the nats container running image, started with --config
// /etc/nats-config/nats.conf from ConfigMap envoy-nats-config with env, the JetStream claim
// template mounted at /data, and the config reloader beside it.
func ChartPodSpec(image string, env []corev1.EnvVar) corev1.PodSpec {
	configMounts := []corev1.VolumeMount{{Name: "pid", MountPath: "/var/run/nats"}, {Name: "config", MountPath: "/etc/nats-config"}}
	return corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name: Container, Image: image, Args: []string{"--config", "/etc/nats-config/nats.conf"}, Env: env,
				VolumeMounts: []corev1.VolumeMount{
					{Name: "config", MountPath: "/etc/nats-config"}, {Name: "pid", MountPath: "/var/run/nats"},
					{Name: ClaimTemplate, MountPath: "/data"},
				},
			},
			{
				Name: "reloader", Image: "natsio/nats-server-config-reloader:0.24.0",
				Args:         []string{"-pid", "/var/run/nats/nats.pid", "-config", "/etc/nats-config/nats.conf", "-config", "/etc/nats-config/auth.conf"},
				VolumeMounts: configMounts,
			},
		},
		Volumes: []corev1.Volume{
			{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: ConfigMap}}}},
			{Name: "pid", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		},
	}
}

// InstallRender gives the Kube the render the clustered hub runs, as the chart shapes it: the
// StatefulSet's pod spec ChartPodSpec(Image, ChartEnv(n)), and ConfigMap envoy-nats-config holding
// ChartNatsConf and AuthConf(keys). The cluster's own members run testnats' configurations; the
// hold reads this render, as it reads the hub's records, and binds each restarted pod to it through
// its revision, its image and the ConfigMap's version.
func (k *Kube) InstallRender(t testing.TB, keys testnats.CalloutKeys) {
	t.Helper()
	ctx := context.Background()
	set, err := k.Client.AppsV1().StatefulSets(Namespace).Get(ctx, StatefulSet, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("holdtest: read the StatefulSet: %v", err)
	}
	set.Spec.Template.Spec = ChartPodSpec(Image, ChartEnv(len(k.Cluster.Servers)))
	if _, err := k.Client.AppsV1().StatefulSets(Namespace).Update(ctx, set, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("holdtest: update the StatefulSet: %v", err)
	}
	if _, err := k.Client.CoreV1().ConfigMaps(Namespace).Create(ctx, RenderConfigMap(ChartNatsConf, AuthConf(t, keys)), metav1.CreateOptions{}); err != nil {
		t.Fatalf("holdtest: create the ConfigMap: %v", err)
	}
}
