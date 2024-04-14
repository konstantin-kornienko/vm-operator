package vmagent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"testing"

	victoriametricsv1beta1 "github.com/VictoriaMetrics/operator/api/v1beta1"
	"github.com/VictoriaMetrics/operator/controllers/factory/k8stools"
	"github.com/VictoriaMetrics/operator/internal/config"
	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v2"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

func Test_addTLStoYaml(t *testing.T) {
	type args struct {
		cfg       yaml.MapSlice
		namespace string
		tls       *victoriametricsv1beta1.TLSConfig
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "check ca only added to config",
			args: args{
				namespace: "default",
				cfg:       yaml.MapSlice{},
				tls: &victoriametricsv1beta1.TLSConfig{
					CA: victoriametricsv1beta1.SecretOrConfigMap{
						Secret: &v1.SecretKeySelector{
							Key: "ca",
							LocalObjectReference: v1.LocalObjectReference{
								Name: "tls-secret",
							},
						},
					},
					Cert: victoriametricsv1beta1.SecretOrConfigMap{},
				},
			},
			want: `tls_config:
  insecure_skip_verify: false
  ca_file: /etc/vmagent-tls/certs/default_tls-secret_ca
`,
		},
		{
			name: "check ca,cert and key added to config",
			args: args{
				namespace: "default",
				cfg:       yaml.MapSlice{},
				tls: &victoriametricsv1beta1.TLSConfig{
					CA: victoriametricsv1beta1.SecretOrConfigMap{
						Secret: &v1.SecretKeySelector{
							Key: "ca",
							LocalObjectReference: v1.LocalObjectReference{
								Name: "tls-secret",
							},
						},
					},
					Cert: victoriametricsv1beta1.SecretOrConfigMap{
						Secret: &v1.SecretKeySelector{
							Key: "cert",
							LocalObjectReference: v1.LocalObjectReference{
								Name: "tls-secret",
							},
						},
					},
					KeySecret: &v1.SecretKeySelector{
						Key: "key",
						LocalObjectReference: v1.LocalObjectReference{
							Name: "tls-secret",
						},
					},
				},
			},
			want: `tls_config:
  insecure_skip_verify: false
  ca_file: /etc/vmagent-tls/certs/default_tls-secret_ca
  cert_file: /etc/vmagent-tls/certs/default_tls-secret_cert
  key_file: /etc/vmagent-tls/certs/default_tls-secret_key
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := addTLStoYaml(tt.args.cfg, tt.args.namespace, tt.args.tls, false)
			gotBytes, err := yaml.Marshal(got)
			if err != nil {
				t.Errorf("cannot marshal tlsConfig to yaml format: %e", err)
				return
			}
			if !reflect.DeepEqual(string(gotBytes), tt.want) {
				t.Errorf("addTLStoYaml() \ngot: \n%v \nwant \n%v", string(gotBytes), tt.want)
			}
		})
	}
}

func Test_generateRelabelConfig(t *testing.T) {
	type args struct {
		rc *victoriametricsv1beta1.RelabelConfig
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "ok base cfg",
			args: args{rc: &victoriametricsv1beta1.RelabelConfig{
				TargetLabel:  "address",
				SourceLabels: []string{"__address__"},
				Action:       "replace",
			}},
			want: `source_labels:
- __address__
target_label: address
action: replace
`,
		},
		{
			name: "ok base with underscore",
			args: args{rc: &victoriametricsv1beta1.RelabelConfig{
				UnderScoreTargetLabel:  "address",
				UnderScoreSourceLabels: []string{"__address__"},
				Action:                 "replace",
			}},
			want: `source_labels:
- __address__
target_label: address
action: replace
`,
		},
		{
			name: "ok base with graphite match labels",
			args: args{rc: &victoriametricsv1beta1.RelabelConfig{
				UnderScoreTargetLabel:  "address",
				UnderScoreSourceLabels: []string{"__address__"},
				Action:                 "graphite",
				Labels:                 map[string]string{"job": "$1", "instance": "${2}:8080"},
				Match:                  `foo.*.*.bar`,
			}},
			want: `source_labels:
- __address__
target_label: address
action: graphite
match: foo.*.*.bar
labels:
  instance: ${2}:8080
  job: $1
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// related fields only filled during json unmarshal
			j, err := json.Marshal(tt.args.rc)
			if err != nil {
				t.Fatalf("cannto serialize relabelConfig : %s", err)
			}
			var rlbCfg victoriametricsv1beta1.RelabelConfig
			if err := json.Unmarshal(j, &rlbCfg); err != nil {
				t.Fatalf("cannot parse relabelConfig : %s", err)
			}
			got := generateRelabelConfig(&rlbCfg)
			gotBytes, err := yaml.Marshal(got)
			if err != nil {
				t.Errorf("cannot marshal generateRelabelConfig to yaml,err :%e", err)
				return
			}
			assert.Equal(t, tt.want, string(gotBytes))
		})
	}
}

func TestCreateOrUpdateConfigurationSecret(t *testing.T) {
	type args struct {
		cr *victoriametricsv1beta1.VMAgent
		c  *config.BaseOperatorConf
	}
	tests := []struct {
		name              string
		args              args
		predefinedObjects []runtime.Object
		wantConfig        string
		wantErr           bool
	}{
		{
			name: "complete test",
			args: args{
				cr: &victoriametricsv1beta1.VMAgent{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test",
						Namespace: "default",
					},
					Spec: victoriametricsv1beta1.VMAgentSpec{
						ServiceScrapeNamespaceSelector: &metav1.LabelSelector{},
						ServiceScrapeSelector:          &metav1.LabelSelector{},
						PodScrapeSelector:              &metav1.LabelSelector{},
						PodScrapeNamespaceSelector:     &metav1.LabelSelector{},
						NodeScrapeNamespaceSelector:    &metav1.LabelSelector{},
						NodeScrapeSelector:             &metav1.LabelSelector{},
						StaticScrapeNamespaceSelector:  &metav1.LabelSelector{},
						StaticScrapeSelector:           &metav1.LabelSelector{},
						ProbeNamespaceSelector:         &metav1.LabelSelector{},
						ProbeSelector:                  &metav1.LabelSelector{},
					},
				},
				c: config.MustGetBaseConfig(),
			},
			predefinedObjects: []runtime.Object{
				&v1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "default",
					},
				},
				&v1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kube-system",
					},
				},
				&victoriametricsv1beta1.VMServiceScrape{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "test-vms",
					},
					Spec: victoriametricsv1beta1.VMServiceScrapeSpec{
						Selector:          metav1.LabelSelector{},
						JobLabel:          "app",
						NamespaceSelector: victoriametricsv1beta1.NamespaceSelector{},
						Endpoints: []victoriametricsv1beta1.Endpoint{
							{
								Path: "/metrics",
								Port: "8085",
								BearerTokenSecret: &v1.SecretKeySelector{
									Key: "bearer",
									LocalObjectReference: v1.LocalObjectReference{
										Name: "access-creds",
									},
								},
							},
							{
								Path: "/metrics-2",
								Port: "8083",
							},
						},
					},
				},
				&victoriametricsv1beta1.VMProbe{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "kube-system",
						Name:      "test-vmp",
					},
					Spec: victoriametricsv1beta1.VMProbeSpec{
						Targets: victoriametricsv1beta1.VMProbeTargets{
							StaticConfig: &victoriametricsv1beta1.VMProbeTargetStaticConfig{
								Targets: []string{"localhost:8428"},
							},
						},
						VMProberSpec: victoriametricsv1beta1.VMProberSpec{URL: "http://blackbox"},
					},
				},
				&victoriametricsv1beta1.VMPodScrape{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "test-vps",
					},
					Spec: victoriametricsv1beta1.VMPodScrapeSpec{
						JobLabel:          "app",
						NamespaceSelector: victoriametricsv1beta1.NamespaceSelector{},
						Selector: metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{
									Key:      "app",
									Operator: metav1.LabelSelectorOpIn,
									Values:   []string{"prod"},
								},
							},
						},
						SampleLimit: 10,
						PodMetricsEndpoints: []victoriametricsv1beta1.PodMetricsEndpoint{
							{
								Path: "/metrics-3",
								Port: "805",
								VMScrapeParams: &victoriametricsv1beta1.VMScrapeParams{
									StreamParse: ptr.To(true),
									ProxyClientConfig: &victoriametricsv1beta1.ProxyAuth{
										TLSConfig: &victoriametricsv1beta1.TLSConfig{
											InsecureSkipVerify: true,
											KeySecret: &v1.SecretKeySelector{
												Key: "key",
												LocalObjectReference: v1.LocalObjectReference{
													Name: "access-creds",
												},
											},
											Cert: victoriametricsv1beta1.SecretOrConfigMap{Secret: &v1.SecretKeySelector{
												Key: "cert",
												LocalObjectReference: v1.LocalObjectReference{
													Name: "access-creds",
												},
											}},
											CA: victoriametricsv1beta1.SecretOrConfigMap{
												Secret: &v1.SecretKeySelector{
													Key: "ca",
													LocalObjectReference: v1.LocalObjectReference{
														Name: "access-creds",
													},
												},
											},
										},
									},
								},
							},
							{
								Port: "801",
								Path: "/metrics-5",
								TLSConfig: &victoriametricsv1beta1.TLSConfig{
									InsecureSkipVerify: true,
									KeySecret: &v1.SecretKeySelector{
										Key: "key",
										LocalObjectReference: v1.LocalObjectReference{
											Name: "access-creds",
										},
									},
									Cert: victoriametricsv1beta1.SecretOrConfigMap{Secret: &v1.SecretKeySelector{
										Key: "cert",
										LocalObjectReference: v1.LocalObjectReference{
											Name: "access-creds",
										},
									}},
									CA: victoriametricsv1beta1.SecretOrConfigMap{
										Secret: &v1.SecretKeySelector{
											Key: "ca",
											LocalObjectReference: v1.LocalObjectReference{
												Name: "access-creds",
											},
										},
									},
								},
							},
						},
					},
				},
				&victoriametricsv1beta1.VMNodeScrape{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "test-vms",
					},
					Spec: victoriametricsv1beta1.VMNodeScrapeSpec{
						BasicAuth: &victoriametricsv1beta1.BasicAuth{
							Username: v1.SecretKeySelector{
								Key: "username",
								LocalObjectReference: v1.LocalObjectReference{
									Name: "access-creds",
								},
							},
							Password: v1.SecretKeySelector{
								Key: "password",
								LocalObjectReference: v1.LocalObjectReference{
									Name: "access-creds",
								},
							},
						},
					},
				},
				&victoriametricsv1beta1.VMStaticScrape{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "test-vmstatic",
					},
					Spec: victoriametricsv1beta1.VMStaticScrapeSpec{
						TargetEndpoints: []*victoriametricsv1beta1.TargetEndpoint{
							{
								Path:     "/metrics-3",
								Port:     "3031",
								Scheme:   "https",
								ProxyURL: ptr.To("https://some-proxy-1"),
								OAuth2: &victoriametricsv1beta1.OAuth2{
									TokenURL: "https://some-tr",
									ClientSecret: &v1.SecretKeySelector{
										Key: "cs",
										LocalObjectReference: v1.LocalObjectReference{
											Name: "access-creds",
										},
									},
									ClientID: victoriametricsv1beta1.SecretOrConfigMap{
										Secret: &v1.SecretKeySelector{
											Key: "cid",
											LocalObjectReference: v1.LocalObjectReference{
												Name: "access-creds",
											},
										},
									},
								},
							},
						},
					},
				},
				&v1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "access-creds",
						Namespace: "default",
					},
					Data: map[string][]byte{
						"cid":      []byte(`some-client-id`),
						"cs":       []byte(`some-client-secret`),
						"username": []byte(`some-username`),
						"password": []byte(`some-password`),
						"ca":       []byte(`some-ca-cert`),
						"cert":     []byte(`some-cert`),
						"key":      []byte(`some-key`),
						"bearer":   []byte(`some-bearer`),
					},
				},
			},
			wantConfig: `global:
  scrape_interval: 30s
  external_labels:
    prometheus: default/test
scrape_configs:
- job_name: serviceScrape/default/test-vms/0
  honor_labels: false
  kubernetes_sd_configs:
  - role: endpoints
    namespaces:
      names:
      - default
  metrics_path: /metrics
  bearer_token: some-bearer
  relabel_configs:
  - action: keep
    source_labels:
    - __meta_kubernetes_endpoint_port_name
    regex: "8085"
  - source_labels:
    - __meta_kubernetes_endpoint_address_target_kind
    - __meta_kubernetes_endpoint_address_target_name
    separator: ;
    regex: Node;(.*)
    replacement: ${1}
    target_label: node
  - source_labels:
    - __meta_kubernetes_endpoint_address_target_kind
    - __meta_kubernetes_endpoint_address_target_name
    separator: ;
    regex: Pod;(.*)
    replacement: ${1}
    target_label: pod
  - source_labels:
    - __meta_kubernetes_pod_name
    target_label: pod
  - source_labels:
    - __meta_kubernetes_pod_container_name
    target_label: container
  - source_labels:
    - __meta_kubernetes_namespace
    target_label: namespace
  - source_labels:
    - __meta_kubernetes_service_name
    target_label: service
  - source_labels:
    - __meta_kubernetes_service_name
    target_label: job
    replacement: ${1}
  - source_labels:
    - __meta_kubernetes_service_label_app
    target_label: job
    regex: (.+)
    replacement: ${1}
  - target_label: endpoint
    replacement: "8085"
- job_name: serviceScrape/default/test-vms/1
  honor_labels: false
  kubernetes_sd_configs:
  - role: endpoints
    namespaces:
      names:
      - default
  metrics_path: /metrics-2
  relabel_configs:
  - action: keep
    source_labels:
    - __meta_kubernetes_endpoint_port_name
    regex: "8083"
  - source_labels:
    - __meta_kubernetes_endpoint_address_target_kind
    - __meta_kubernetes_endpoint_address_target_name
    separator: ;
    regex: Node;(.*)
    replacement: ${1}
    target_label: node
  - source_labels:
    - __meta_kubernetes_endpoint_address_target_kind
    - __meta_kubernetes_endpoint_address_target_name
    separator: ;
    regex: Pod;(.*)
    replacement: ${1}
    target_label: pod
  - source_labels:
    - __meta_kubernetes_pod_name
    target_label: pod
  - source_labels:
    - __meta_kubernetes_pod_container_name
    target_label: container
  - source_labels:
    - __meta_kubernetes_namespace
    target_label: namespace
  - source_labels:
    - __meta_kubernetes_service_name
    target_label: service
  - source_labels:
    - __meta_kubernetes_service_name
    target_label: job
    replacement: ${1}
  - source_labels:
    - __meta_kubernetes_service_label_app
    target_label: job
    regex: (.+)
    replacement: ${1}
  - target_label: endpoint
    replacement: "8083"
- job_name: podScrape/default/test-vps/0
  honor_labels: false
  kubernetes_sd_configs:
  - role: pod
    namespaces:
      names:
      - default
  metrics_path: /metrics-3
  relabel_configs:
  - action: drop
    source_labels:
    - __meta_kubernetes_pod_phase
    regex: (Failed|Succeeded)
  - action: keep
    source_labels:
    - __meta_kubernetes_pod_label_app
    regex: prod
  - action: keep
    source_labels:
    - __meta_kubernetes_pod_container_port_name
    regex: "805"
  - source_labels:
    - __meta_kubernetes_namespace
    target_label: namespace
  - source_labels:
    - __meta_kubernetes_pod_container_name
    target_label: container
  - source_labels:
    - __meta_kubernetes_pod_name
    target_label: pod
  - target_label: job
    replacement: default/test-vps
  - source_labels:
    - __meta_kubernetes_pod_label_app
    target_label: job
    regex: (.+)
    replacement: ${1}
  - target_label: endpoint
    replacement: "805"
  sample_limit: 10
  stream_parse: true
  proxy_tls_config:
    insecure_skip_verify: true
    ca_file: /etc/vmagent-tls/certs/default_access-creds_ca
    cert_file: /etc/vmagent-tls/certs/default_access-creds_cert
    key_file: /etc/vmagent-tls/certs/default_access-creds_key
- job_name: podScrape/default/test-vps/1
  honor_labels: false
  kubernetes_sd_configs:
  - role: pod
    namespaces:
      names:
      - default
  metrics_path: /metrics-5
  tls_config:
    insecure_skip_verify: true
    ca_file: /etc/vmagent-tls/certs/default_access-creds_ca
    cert_file: /etc/vmagent-tls/certs/default_access-creds_cert
    key_file: /etc/vmagent-tls/certs/default_access-creds_key
  relabel_configs:
  - action: drop
    source_labels:
    - __meta_kubernetes_pod_phase
    regex: (Failed|Succeeded)
  - action: keep
    source_labels:
    - __meta_kubernetes_pod_label_app
    regex: prod
  - action: keep
    source_labels:
    - __meta_kubernetes_pod_container_port_name
    regex: "801"
  - source_labels:
    - __meta_kubernetes_namespace
    target_label: namespace
  - source_labels:
    - __meta_kubernetes_pod_container_name
    target_label: container
  - source_labels:
    - __meta_kubernetes_pod_name
    target_label: pod
  - target_label: job
    replacement: default/test-vps
  - source_labels:
    - __meta_kubernetes_pod_label_app
    target_label: job
    regex: (.+)
    replacement: ${1}
  - target_label: endpoint
    replacement: "801"
  sample_limit: 10
- job_name: probe/kube-system/test-vmp/0
  params:
    module:
    - ""
  metrics_path: /probe
  static_configs:
  - targets:
    - localhost:8428
  relabel_configs:
  - source_labels:
    - __address__
    target_label: __param_target
  - source_labels:
    - __param_target
    target_label: instance
  - target_label: __address__
    replacement: http://blackbox
- job_name: nodeScrape/default/test-vms/0
  honor_labels: false
  kubernetes_sd_configs:
  - role: node
  basic_auth:
    username: some-username
    password: some-password
  relabel_configs:
  - source_labels:
    - __meta_kubernetes_node_name
    target_label: node
  - target_label: job
    replacement: default/test-vms
- job_name: staticScrape/default/test-vmstatic/0
  honor_labels: false
  static_configs:
  - targets: []
  metrics_path: /metrics-3
  proxy_url: https://some-proxy-1
  scheme: https
  relabel_configs: []
  oauth2:
    client_id: some-client-id
    client_secret: some-client-secret
    token_url: https://some-tr
`,
		},
		{
			name: "with missing secret references",
			args: args{
				cr: &victoriametricsv1beta1.VMAgent{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test",
						Namespace: "default",
					},
					Spec: victoriametricsv1beta1.VMAgentSpec{
						ServiceScrapeNamespaceSelector: &metav1.LabelSelector{},
						ServiceScrapeSelector:          &metav1.LabelSelector{},
						PodScrapeSelector:              &metav1.LabelSelector{},
						PodScrapeNamespaceSelector:     &metav1.LabelSelector{},
						NodeScrapeNamespaceSelector:    &metav1.LabelSelector{},
						NodeScrapeSelector:             &metav1.LabelSelector{},
						StaticScrapeNamespaceSelector:  &metav1.LabelSelector{},
						StaticScrapeSelector:           &metav1.LabelSelector{},
						ProbeNamespaceSelector:         &metav1.LabelSelector{},
						ProbeSelector:                  &metav1.LabelSelector{},
					},
				},
				c: config.MustGetBaseConfig(),
			},

			predefinedObjects: []runtime.Object{
				&v1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "default",
					},
				},

				&victoriametricsv1beta1.VMNodeScrape{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "test-bad-0",
					},
					Spec: victoriametricsv1beta1.VMNodeScrapeSpec{
						BasicAuth: &victoriametricsv1beta1.BasicAuth{
							Username: v1.SecretKeySelector{
								Key: "username",
								LocalObjectReference: v1.LocalObjectReference{
									Name: "access-creds",
								},
							},
							Password: v1.SecretKeySelector{
								Key: "password",
								LocalObjectReference: v1.LocalObjectReference{
									Name: "access-creds",
								},
							},
						},
					},
				},

				&victoriametricsv1beta1.VMNodeScrape{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "test-good",
					},
					Spec: victoriametricsv1beta1.VMNodeScrapeSpec{},
				},

				&victoriametricsv1beta1.VMNodeScrape{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "bad-1",
					},
					Spec: victoriametricsv1beta1.VMNodeScrapeSpec{
						BearerTokenSecret: &v1.SecretKeySelector{
							Key: "username",
							LocalObjectReference: v1.LocalObjectReference{
								Name: "access-creds",
							},
						},
					},
				},
				&victoriametricsv1beta1.VMPodScrape{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "test-vps-mixed",
					},
					Spec: victoriametricsv1beta1.VMPodScrapeSpec{
						JobLabel:          "app",
						NamespaceSelector: victoriametricsv1beta1.NamespaceSelector{},
						Selector: metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{
									Key:      "app",
									Operator: metav1.LabelSelectorOpIn,
									Values:   []string{"prod"},
								},
							},
						},
						SampleLimit: 10,
						PodMetricsEndpoints: []victoriametricsv1beta1.PodMetricsEndpoint{
							{
								Path: "/metrics-3",
								Port: "805",
								VMScrapeParams: &victoriametricsv1beta1.VMScrapeParams{
									StreamParse: ptr.To(true),
									ProxyClientConfig: &victoriametricsv1beta1.ProxyAuth{
										BearerToken: &v1.SecretKeySelector{
											Key: "username",
											LocalObjectReference: v1.LocalObjectReference{
												Name: "access-creds",
											},
										},
									},
								},
							},
							{
								Port: "801",
								Path: "/metrics-5",
								BasicAuth: &victoriametricsv1beta1.BasicAuth{
									Username: v1.SecretKeySelector{
										Key: "username",
										LocalObjectReference: v1.LocalObjectReference{
											Name: "access-creds",
										},
									},
									Password: v1.SecretKeySelector{
										Key: "password",
										LocalObjectReference: v1.LocalObjectReference{
											Name: "access-creds",
										},
									},
								},
							},
							{
								Port: "801",
								Path: "/metrics-5-good",
							},
						},
					},
				},
				&victoriametricsv1beta1.VMPodScrape{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "test-vps-good",
					},
					Spec: victoriametricsv1beta1.VMPodScrapeSpec{
						JobLabel:          "app",
						NamespaceSelector: victoriametricsv1beta1.NamespaceSelector{},
						Selector: metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{
									Key:      "app",
									Operator: metav1.LabelSelectorOpIn,
									Values:   []string{"prod"},
								},
							},
						},
						PodMetricsEndpoints: []victoriametricsv1beta1.PodMetricsEndpoint{
							{
								Port: "8011",
								Path: "/metrics-1-good",
							},
						},
					},
				},
				&victoriametricsv1beta1.VMStaticScrape{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "test-vmstatic-bad",
					},
					Spec: victoriametricsv1beta1.VMStaticScrapeSpec{
						TargetEndpoints: []*victoriametricsv1beta1.TargetEndpoint{
							{
								Path:     "/metrics-3",
								Port:     "3031",
								Scheme:   "https",
								ProxyURL: ptr.To("https://some-proxy-1"),
								OAuth2: &victoriametricsv1beta1.OAuth2{
									TokenURL: "https://some-tr",
									ClientSecret: &v1.SecretKeySelector{
										Key: "cs",
										LocalObjectReference: v1.LocalObjectReference{
											Name: "access-creds",
										},
									},
									ClientID: victoriametricsv1beta1.SecretOrConfigMap{
										Secret: &v1.SecretKeySelector{
											Key: "cid",
											LocalObjectReference: v1.LocalObjectReference{
												Name: "access-creds",
											},
										},
									},
								},
							},
						},
					},
				},
				&victoriametricsv1beta1.VMStaticScrape{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "test-vmstatic-bad-tls",
					},
					Spec: victoriametricsv1beta1.VMStaticScrapeSpec{
						TargetEndpoints: []*victoriametricsv1beta1.TargetEndpoint{
							{
								Path:     "/metrics-3",
								Port:     "3031",
								Scheme:   "https",
								ProxyURL: ptr.To("https://some-proxy-1"),
								TLSConfig: &victoriametricsv1beta1.TLSConfig{
									Cert: victoriametricsv1beta1.SecretOrConfigMap{
										Secret: &v1.SecretKeySelector{
											Key: "cert",
											LocalObjectReference: v1.LocalObjectReference{
												Name: "tls-creds",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			wantConfig: `global:
  scrape_interval: 30s
  external_labels:
    prometheus: default/test
scrape_configs:
- job_name: podScrape/default/test-vps-good/0
  honor_labels: false
  kubernetes_sd_configs:
  - role: pod
    namespaces:
      names:
      - default
  metrics_path: /metrics-1-good
  relabel_configs:
  - action: drop
    source_labels:
    - __meta_kubernetes_pod_phase
    regex: (Failed|Succeeded)
  - action: keep
    source_labels:
    - __meta_kubernetes_pod_label_app
    regex: prod
  - action: keep
    source_labels:
    - __meta_kubernetes_pod_container_port_name
    regex: "8011"
  - source_labels:
    - __meta_kubernetes_namespace
    target_label: namespace
  - source_labels:
    - __meta_kubernetes_pod_container_name
    target_label: container
  - source_labels:
    - __meta_kubernetes_pod_name
    target_label: pod
  - target_label: job
    replacement: default/test-vps-good
  - source_labels:
    - __meta_kubernetes_pod_label_app
    target_label: job
    regex: (.+)
    replacement: ${1}
  - target_label: endpoint
    replacement: "8011"
- job_name: podScrape/default/test-vps-mixed/0
  honor_labels: false
  kubernetes_sd_configs:
  - role: pod
    namespaces:
      names:
      - default
  metrics_path: /metrics-5-good
  relabel_configs:
  - action: drop
    source_labels:
    - __meta_kubernetes_pod_phase
    regex: (Failed|Succeeded)
  - action: keep
    source_labels:
    - __meta_kubernetes_pod_label_app
    regex: prod
  - action: keep
    source_labels:
    - __meta_kubernetes_pod_container_port_name
    regex: "801"
  - source_labels:
    - __meta_kubernetes_namespace
    target_label: namespace
  - source_labels:
    - __meta_kubernetes_pod_container_name
    target_label: container
  - source_labels:
    - __meta_kubernetes_pod_name
    target_label: pod
  - target_label: job
    replacement: default/test-vps-mixed
  - source_labels:
    - __meta_kubernetes_pod_label_app
    target_label: job
    regex: (.+)
    replacement: ${1}
  - target_label: endpoint
    replacement: "801"
  sample_limit: 10
- job_name: nodeScrape/default/test-good/0
  honor_labels: false
  kubernetes_sd_configs:
  - role: node
  relabel_configs:
  - source_labels:
    - __meta_kubernetes_node_name
    target_label: node
  - target_label: job
    replacement: default/test-good
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testClient := k8stools.GetTestClientWithObjects(tt.predefinedObjects)
			if _, err := createOrUpdateConfigurationSecret(context.TODO(), tt.args.cr, testClient, tt.args.c); (err != nil) != tt.wantErr {
				t.Errorf("CreateOrUpdateConfigurationSecret() error = %v, wantErr %v", err, tt.wantErr)
			}
			var expectSecret v1.Secret
			if err := testClient.Get(context.TODO(), types.NamespacedName{Namespace: tt.args.cr.Namespace, Name: tt.args.cr.PrefixedName()}, &expectSecret); err != nil {
				t.Fatalf("cannot get vmagent config secret: %s", err)
			}
			gotCfg := expectSecret.Data[vmagentGzippedFilename]
			cfgB := bytes.NewBuffer(gotCfg)
			gr, err := gzip.NewReader(cfgB)
			if err != nil {
				t.Fatalf("er: %s", err)
			}
			data, err := io.ReadAll(gr)
			if err != nil {
				t.Fatalf("cannot read cfg: %s", err)
			}
			gr.Close()

			assert.Equal(t, tt.wantConfig, string(data))
		})
	}
}
