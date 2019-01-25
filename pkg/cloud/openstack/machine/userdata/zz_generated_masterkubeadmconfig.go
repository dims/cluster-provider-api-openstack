/*
This file is auto-generated DO NOT TOUCH!
*/
package userdata

const (
	masterKubeadmCloudConfig = `apiVersion: kubeadm.k8s.io/v1alpha3
kind: InitConfiguration
apiEndpoint:
  bindPort: 443
nodeRegistration:
{{- if (ne .CloudConf "") }}
  kubeletExtraArgs:
    cloud-provider: "openstack"
    cloud-config: "/etc/kubernetes/cloud.conf"
{{- end }}
---
apiVersion: kubeadm.k8s.io/v1alpha3
kind: ClusterConfiguration
kubernetesVersion: v{{.ControlPlaneVersion}}
networking:
  serviceSubnet: {{.ServiceCIDR}}
clusterName: kubernetes
controlPlaneEndpoint: {{.ControlPlaneEndpoint}}
{{- if (ne .CloudConf "") }}
apiServerExtraArgs:
  cloud-provider: "openstack"
  cloud-config: "/etc/kubernetes/cloud.conf"
apiServerExtraVolumes:
- name: cloud
  hostPath: "/etc/kubernetes/cloud.conf"
  mountPath: "/etc/kubernetes/cloud.conf"
{{- end }}
controllerManagerExtraArgs:
  cluster-cidr: {{.PodCIDR}}
  service-cluster-ip-range: {{.ServiceCIDR}}
  allocate-node-cidrs: "true"
{{- if (ne .CloudConf "") }}
  cloud-provider: "openstack"
  cloud-config: "/etc/kubernetes/cloud.conf"
controllerManagerExtraVolumes:
- name: cloud
  hostPath: "/etc/kubernetes/cloud.conf"
  mountPath: "/etc/kubernetes/cloud.conf"
{{- end }}
`
)
