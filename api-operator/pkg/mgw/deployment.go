// Copyright (c)  WSO2 Inc. (http://www.wso2.org) All Rights Reserved.
//
// WSO2 Inc. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package mgw

import (
	wso2v1alpha1 "github.com/wso2/k8s-api-operator/api-operator/pkg/apis/wso2/v1alpha1"
	"github.com/wso2/k8s-api-operator/api-operator/pkg/k8s"
	"github.com/wso2/k8s-api-operator/api-operator/pkg/registry"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/runtime/log"
	"strconv"
	"strings"
)

const (
	analyticsLocation = "/home/ballerina/wso2/api-usage-data/"
)

// controller config properties
const (
	readinessProbeInitialDelaySeconds = "readinessProbeInitialDelaySeconds"
	readinessProbePeriodSeconds       = "readinessProbePeriodSeconds"
	livenessProbeInitialDelaySeconds  = "livenessProbeInitialDelaySeconds"
	livenessProbePeriodSeconds        = "livenessProbePeriodSeconds"

	resourceRequestCPU    = "resourceRequestCPU"
	resourceRequestMemory = "resourceRequestMemory"
	resourceLimitCPU      = "resourceLimitCPU"
	resourceLimitMemory   = "resourceLimitMemory"

	envKeyValSeparator = "="
)

// mgw-deployment-configs
const (
	mgwDeploymentConfigMapName = "mgw-deployment-configs"
	mgwConfigMaps              = "mgwConfigMaps"
	mgwSecrets                 = "mgwSecrets"
)

type DeploymentConfig struct {
	Name          string `yaml:"name"`
	MountLocation string `yaml:"mountLocation"`
	SubPath       string `yaml:"subPath"`
	Namespace     string `yaml:"namespace,omitempty"`
}

var logDeploy = log.Log.WithName("mgw.deployment")

var (
	ContainerList *[]corev1.Container
)

func InitContainers() {
	initContainerList := make([]corev1.Container, 0, 2)
	ContainerList = &initContainerList
}

func AddContainers(containers *[]corev1.Container) {
	*ContainerList = append(*ContainerList, *containers...)
}

// Deployment returns a MGW deployment for the given API definition
func Deployment(client *client.Client, api *wso2v1alpha1.API, controlConfigData map[string]string,
	owner *[]metav1.OwnerReference) (*appsv1.Deployment, error) {
	regConfig := registry.GetConfig()
	labels := map[string]string{"app": api.Name}
	var deployVolume []corev1.Volume
	var deployVolumeMount []corev1.VolumeMount
	var mgwDeployVol *v1.Volume
	var mgwDeployMount *v1.VolumeMount
	mgwDeploymentConfMap := k8s.NewConfMap()
	errGetDeploy := k8s.Get(client, types.NamespacedName{Name: mgwDeploymentConfigMapName, Namespace: api.Namespace}, mgwDeploymentConfMap)
	if errGetDeploy != nil && errors.IsNotFound(errGetDeploy) {
		logDeploy.Info("Get mgw deployment configs", "from namespace", wso2NameSpaceConst)
		//retrieve mgw deployment configs from wso2-system namespace
		err := k8s.Get(client, types.NamespacedName{Namespace: wso2NameSpaceConst, Name: mgwDeploymentConfigMapName},
			mgwDeploymentConfMap)
		if err != nil && !errors.IsNotFound(err) {
			logDeploy.Error(err, "MGW Deployment configs not defined")
			return nil, err
		}
	} else if errGetDeploy != nil {
		logDeploy.Error(errGetDeploy, "Error getting mgw deployment configs from user namespace")
		return nil, errGetDeploy
	}

	liveDelay, _ := strconv.ParseInt(controlConfigData[livenessProbeInitialDelaySeconds], 10, 32)
	livePeriod, _ := strconv.ParseInt(controlConfigData[livenessProbePeriodSeconds], 10, 32)
	readDelay, _ := strconv.ParseInt(controlConfigData[readinessProbeInitialDelaySeconds], 10, 32)
	readPeriod, _ := strconv.ParseInt(controlConfigData[readinessProbePeriodSeconds], 10, 32)
	reps := int32(api.Spec.Replicas)

	resReqCPU := controlConfigData[resourceRequestCPU]
	resReqMemory := controlConfigData[resourceRequestMemory]
	resLimitCPU := controlConfigData[resourceLimitCPU]
	resLimitMemory := controlConfigData[resourceLimitMemory]

	if Configs.AnalyticsEnabled {
		// mounts an empty dir volume to be used when analytics is enabled
		analVol, analMount := k8s.EmptyDirVolumeMount("analytics", analyticsLocation)
		deployVolume = append(deployVolume, *analVol)
		deployVolumeMount = append(deployVolumeMount, *analMount)
	}

	var deploymentConfigMaps []DeploymentConfig
	yamlErrDeploymentConfigMaps := yaml.Unmarshal([]byte(mgwDeploymentConfMap.Data[mgwConfigMaps]), &deploymentConfigMaps)
	if yamlErrDeploymentConfigMaps != nil {
		logDeploy.Error(yamlErrDeploymentConfigMaps, "Error marshalling mgw config maps yaml",
			"configmap", mgwDeploymentConfMap)
	}
	var deploymentSecrets []DeploymentConfig
	yamlErrDeploymentSecrets := yaml.Unmarshal([]byte(mgwDeploymentConfMap.Data[mgwSecrets]), &deploymentSecrets)
	if yamlErrDeploymentSecrets != nil {
		logDeploy.Error(yamlErrDeploymentSecrets, "Error marshalling mgw secrets yaml", "configmap",
			mgwDeploymentConfMap)
	}
	// mount the MGW config maps to volume
	for _, deploymentConfigMap := range deploymentConfigMaps {
		if deploymentConfigMap.Namespace == "" {
			mgwConfigMap := k8s.NewConfMap()
			mgwConfigMapErr := k8s.Get(client, types.NamespacedName{Namespace: mgwDeploymentConfMap.Namespace,
				Name: deploymentConfigMap.Name}, mgwConfigMap)
			if mgwConfigMapErr != nil {
				logDeploy.Error(mgwConfigMapErr, "Error Getting the mgw Config map")
			}
			newMgwConfigMap := CopyMgwConfigMap(types.NamespacedName{Namespace: api.Namespace,
				Name: deploymentConfigMap.Name}, mgwConfigMap)
			createConfigMapErr := k8s.Apply(client, newMgwConfigMap)
			if createConfigMapErr != nil {
				logDeploy.Error(createConfigMapErr, "Error Copying mgw config map to user namespace")
			}
			mgwDeployVol, mgwDeployMount = k8s.MgwConfigDirVolumeMount(deploymentConfigMap.Name,
				deploymentConfigMap.MountLocation, deploymentConfigMap.SubPath)
			deployVolume = append(deployVolume, *mgwDeployVol)
			deployVolumeMount = append(deployVolumeMount, *mgwDeployMount)
		} else if strings.EqualFold(deploymentConfigMap.Namespace, api.Namespace) {
			mgwDeployVol, mgwDeployMount = k8s.MgwConfigDirVolumeMount(deploymentConfigMap.Name,
				deploymentConfigMap.MountLocation, deploymentConfigMap.SubPath)
			deployVolume = append(deployVolume, *mgwDeployVol)
			deployVolumeMount = append(deployVolumeMount, *mgwDeployMount)
		}
	}
	// mount MGW secrets to volume
	for _, deploymentSecret := range deploymentSecrets {
		if deploymentSecret.Namespace == "" {
			mgwSecret := k8s.NewSecret()
			mgwSecretErr := k8s.Get(client, types.NamespacedName{Namespace: mgwDeploymentConfMap.Namespace,
				Name: deploymentSecret.Name}, mgwSecret)
			if mgwSecretErr != nil {
				logDeploy.Error(mgwSecretErr, "Error Getting the mgw Secret")
			}
			newMgwSecret := CopyMgwSecret(types.NamespacedName{Namespace: api.Namespace,
				Name: deploymentSecret.Name}, mgwSecret)
			createSecretErr := k8s.Apply(client, newMgwSecret)
			if createSecretErr != nil {
				logDeploy.Error(createSecretErr, "Error Copying mgw secret to user namespace")
			}
			mgwDeployVol, mgwDeployMount = k8s.MgwSecretVolumeMount(deploymentSecret.Name,
				deploymentSecret.MountLocation,
				deploymentSecret.SubPath)
			deployVolume = append(deployVolume, *mgwDeployVol)
			deployVolumeMount = append(deployVolumeMount, *mgwDeployMount)
		} else if strings.EqualFold(deploymentSecret.Namespace, api.Namespace) {
			mgwDeployVol, mgwDeployMount = k8s.MgwSecretVolumeMount(deploymentSecret.Name,
				deploymentSecret.MountLocation,
				deploymentSecret.SubPath)
			deployVolume = append(deployVolume, *mgwDeployVol)
			deployVolumeMount = append(deployVolumeMount, *mgwDeployMount)
		}
	}

	req := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(resReqCPU),
		corev1.ResourceMemory: resource.MustParse(resReqMemory),
	}
	lim := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(resLimitCPU),
		corev1.ResourceMemory: resource.MustParse(resLimitMemory),
	}

	// container ports
	containerPorts := []corev1.ContainerPort{
		{
			ContainerPort: Configs.HttpPort,
		},
		{
			ContainerPort: Configs.HttpsPort,
		},
	}
	// setting observability port
	if Configs.ObservabilityEnabled {
		containerPorts = append(containerPorts, corev1.ContainerPort{
			ContainerPort: observabilityPrometheusPort,
		})
	}

	// setting environment variables
	// env from registry configs
	env := regConfig.Env
	// env from API CRD Spec
	for _, variable := range api.Spec.EnvironmentVariables {
		envKeyVal := strings.SplitN(variable, envKeyValSeparator, 2)
		env = append(env, corev1.EnvVar{
			Name:  envKeyVal[0],
			Value: envKeyVal[:2][1],
		})
	}

	// setting container image
	var image string
	if api.Spec.Image != "" {
		image = api.Spec.Image
	} else {
		image = regConfig.ImagePath
	}

	// API container
	apiContainer := corev1.Container{
		Name:            "mgw" + api.Name,
		Image:           image,
		ImagePullPolicy: "Always",
		Resources: corev1.ResourceRequirements{
			Requests: req,
			Limits:   lim,
		},
		VolumeMounts: deployVolumeMount,
		Env:          env,
		Ports:        containerPorts,
		ReadinessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/health",
					Port:   intstr.IntOrString{Type: intstr.Int, IntVal: Configs.HttpsPort},
					Scheme: "HTTPS",
				},
			},
			InitialDelaySeconds: int32(readDelay),
			PeriodSeconds:       int32(readPeriod),
			TimeoutSeconds:      1,
		},
		LivenessProbe: &corev1.Probe{
			Handler: corev1.Handler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/health",
					Port:   intstr.IntOrString{Type: intstr.Int, IntVal: Configs.HttpsPort},
					Scheme: "HTTPS",
				},
			},
			InitialDelaySeconds: int32(liveDelay),
			PeriodSeconds:       int32(livePeriod),
			TimeoutSeconds:      1,
		},
	}

	*(ContainerList) = append(*(ContainerList), apiContainer)

	deploy := k8s.NewDeployment()
	deploy.ObjectMeta = metav1.ObjectMeta{
		Name:            api.Name,
		Namespace:       api.Namespace,
		Labels:          labels,
		OwnerReferences: *owner,
	}
	deploy.Spec = appsv1.DeploymentSpec{
		Replicas: &reps,
		Selector: &metav1.LabelSelector{
			MatchLabels: labels,
		},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels: labels,
			},
			Spec: corev1.PodSpec{
				Containers:       *(ContainerList),
				Volumes:          deployVolume,
				ImagePullSecrets: regConfig.ImagePullSecrets,
			},
		},
	}
	return deploy, nil
}

// CopyMgwConfigMap returns a copied configMap object with given namespacedName
func CopyMgwConfigMap(namespacedName types.NamespacedName, confMap *corev1.ConfigMap) *corev1.ConfigMap {
	confMap.ObjectMeta = metav1.ObjectMeta{
		Name:      namespacedName.Name,
		Namespace: namespacedName.Namespace,
	}
	return confMap
}

// CopyMgwSecret returns a copied secret object with given namespacedName
func CopyMgwSecret(namespacedName types.NamespacedName, secret *corev1.Secret) *corev1.Secret {
	secret.ObjectMeta = metav1.ObjectMeta{
		Name:      namespacedName.Name,
		Namespace: namespacedName.Namespace,
	}
	return secret
}
