package advertisement_operator

import (
	"os"

	protocolv1 "github.com/liqoTech/liqo/api/advertisement-operator/v1"

	v1 "k8s.io/api/core/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// get config to create a client
// parameters:
// - path: the path to the kubeconfig file
// - cm: the configMap containing the kubeconfig
// - sec: the secret containing the kubeconfig
// if path is specified create a config from a kubeconfig file, otherwise create or a inCluster config or read the kubeconfig from the configMap/secret.
func GetConfig(path string, cm *v1.ConfigMap, sec *v1.Secret) (*rest.Config, error) {
	var config *rest.Config
	var err error

	if path == "" && cm == nil && sec == nil {
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, err
		}
	} else if path == "" && cm != nil && sec == nil {
		// Get the kubeconfig from configMap
		kubeconfigGetter := GetKubeconfigFromConfigMap(*cm)
		config, err = clientcmd.BuildConfigFromKubeconfigGetter("", kubeconfigGetter)
		if err != nil {
			return nil, err
		}
	} else if path == "" && cm == nil && sec != nil {
		// Get the kubeconfig from secret
		kubeconfigGetter := GetKubeconfigFromSecret(*sec)
		config, err = clientcmd.BuildConfigFromKubeconfigGetter("", kubeconfigGetter)
		if err != nil {
			return nil, err
		}
	} else if path != "" {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			// Get the kubeconfig from the filepath.
			config, err = clientcmd.BuildConfigFromFlags("", path)
			if err != nil {
				return nil, err
			}
		}
	}

	return config, err
}

// create a standard K8s client -> to access use client.CoreV1().<resource>(<namespace>).<method>()).
func NewK8sClient(path string, cm *v1.ConfigMap, sec *v1.Secret) (*kubernetes.Clientset, error) {
	config, err := GetConfig(path, cm, sec)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

// create a crd client (kubebuilder-like) -> to access use client.<method>(context, <NamespacedName>, <resource>).
func NewCRDClient(path string, cm *v1.ConfigMap, sec *v1.Secret) (client.Client, error) {
	config, err := GetConfig(path, cm, sec)
	if err != nil {
		return nil, err
	}

	scheme := k8sruntime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = protocolv1.AddToScheme(scheme)

	remoteClient, err := client.New(config, client.Options{
		Scheme: scheme,
	})
	if err != nil {
		return nil, err
	}

	return remoteClient, nil
}

// extract kubeconfig from a configMap.
func GetKubeconfigFromConfigMap(cm v1.ConfigMap) clientcmd.KubeconfigGetter {
	return func() (*clientcmdapi.Config, error) {

		data := []byte(cm.Data["kubeconfig"])
		return clientcmd.Load(data)
	}
}

// extract kubeconfig from a secret.
func GetKubeconfigFromSecret(sec v1.Secret) clientcmd.KubeconfigGetter {
	return func() (*clientcmdapi.Config, error) {

		return clientcmd.Load(sec.Data["kubeconfig"])
	}
}
