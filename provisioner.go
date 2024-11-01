package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/golang/glog"
	v1 "k8s.io/api/core/v1"
	storage "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	storagehelpers "k8s.io/component-helpers/storage/volume"
	"os"
	"path/filepath"
	"regexp"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v6/controller"
	"strconv"
	"strings"
)

const (
	// 挂载到容器中的路径
	rootPath        = "/persistentvolumes"
	archivePathName = "archived"
	// 提供者名称
	provisionerNameKey = "PROVISIONER_NAME"
	// 当前主机的目录
	hostDirKey              = "HOST_DIR"
	kubeconfigKey           = "KUBECONFIG"
	enableLeaderElectionKey = "ENABLE_LEADER_ELECTION"
	provisionerPvPathKey    = "provisioner/pvPath"
	provisionerHostDirKey   = "provisioner/hostDir"
)

type hostProvisioner struct {
	client  kubernetes.Interface
	hostDir string
}

var pathPattern = regexp.MustCompile(`\{([^}]+)\}`)
var hostType = v1.HostPathDirectory

func getPvPath(pvPathTemplate string, variables map[string]string) (string, error) {
	result := pathPattern.FindAllStringSubmatch(pvPathTemplate, -1)
	for _, r := range result {
		val := variables[r[1]]
		if val == "" {
			errors.New("模板" + pvPathTemplate + "中找不到变量: " + r[1])
		}
		pvPathTemplate = strings.ReplaceAll(pvPathTemplate, r[0], val)
	}
	return pvPathTemplate, nil
}

func (p *hostProvisioner) Provision(ctx context.Context, options controller.ProvisionOptions) (*v1.PersistentVolume, controller.ProvisioningState, error) {
	if options.PVC.Spec.Selector != nil {
		return nil, controller.ProvisioningFinished, fmt.Errorf("claim Selector is not supported")
	}
	glog.V(4).Infof("nfs provisioner: VolumeOptions %v", options)

	pvcNamespace := options.PVC.Namespace
	pvcName := options.PVC.Name
	logPrefix := "PVC: 【" + pvcNamespace + "/" + pvcName + "】"

	pvPathTemplate, exists := options.StorageClass.Parameters["pvPathTemplate"]
	if !exists {
		pvPathTemplate = "{namespace}-{pvcName}-{pvName}"
	}
	variables := map[string]string{
		"provisioner": options.StorageClass.Provisioner,
		"pvcUid":      string(options.PVC.ObjectMeta.UID),
		"namespace":   pvcNamespace,
		"pvcName":     pvcName,
		"pvName":      options.PVName,
	}
	for label, val := range options.PVC.ObjectMeta.Annotations {
		if strings.HasPrefix(label, "pv-path-var/") {
			variables[label] = val
		}
	}

	pvPath, err := getPvPath(pvPathTemplate, variables)
	if err != nil {
		return nil, controller.ProvisioningFinished, fmt.Errorf(err.Error())
	}
	if pvPath == "" {
		return nil, controller.ProvisioningFinished, errors.New(logPrefix + "pvPath 不能为空，模板: " + pvPathTemplate)
	}

	// 在容器内的路径
	dockerFullPath := filepath.Join(rootPath, pvPath)
	// 挂载到容器的路径
	mountPath := filepath.Join(p.hostDir, pvPath)
	fmt.Printf(logPrefix+"挂载到容器的路径是：%s \n", mountPath)

	glog.V(4).Infof("%s 在容器中创建目录 %s", logPrefix, dockerFullPath)
	if err := os.MkdirAll(dockerFullPath, 0o777); err != nil {
		return nil, controller.ProvisioningFinished, errors.New(logPrefix + "创建路径错误: " + err.Error())
	}
	err = os.Chmod(dockerFullPath, 0o777)
	if err != nil {
		return nil, "", err
	}

	// 设置挂载选项
	mountOptions := options.StorageClass.MountOptions
	pvcMountOptions := options.PVC.Annotations["mount-options"]
	if pvcMountOptions != "" {
		mountOptions = []string{pvcMountOptions}
	}

	pv := &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      options.PVName,
			Namespace: pvcNamespace,
			Annotations: map[string]string{
				provisionerPvPathKey:  pvPath,
				provisionerHostDirKey: p.hostDir,
			},
		},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: *options.StorageClass.ReclaimPolicy,
			AccessModes:                   options.PVC.Spec.AccessModes,
			MountOptions:                  mountOptions,
			Capacity: v1.ResourceList{
				v1.ResourceStorage: options.PVC.Spec.Resources.Requests[v1.ResourceStorage],
			},

			PersistentVolumeSource: v1.PersistentVolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: mountPath,
					Type: &hostType,
				},
			},
		},
	}
	return pv, controller.ProvisioningFinished, nil
}

func (p *hostProvisioner) Delete(ctx context.Context, volume *v1.PersistentVolume) error {
	pvHostDir, pvHostDirExists := volume.Annotations[provisionerHostDirKey]
	pvPath, pvPathExists := volume.Annotations[provisionerPvPathKey]

	if !pvHostDirExists {
		pvHostDir = p.hostDir
	}
	if !pvPathExists {
		mountPath := volume.Spec.PersistentVolumeSource.HostPath.Path
		mountPath = strings.ReplaceAll(mountPath, "\\", "/")
		pvPath = strings.Replace(mountPath, pvHostDir, "", 1)
	}

	oldPath := filepath.Join(rootPath, pvPath)

	pvcNamespace := volume.Spec.ClaimRef.Namespace
	pvcName := volume.Spec.ClaimRef.Name
	logPrefix := "【PVC: " + pvcNamespace + "/" + pvcName + "】"

	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		glog.Warningf("%s path %s pv路径不存在", logPrefix, oldPath)
		return nil
	}
	// Get the storage class for this volume.
	storageClass, err := p.getClassForVolume(logPrefix, ctx, volume)
	if err != nil {
		return err
	}

	reclaimPolicy := storageClass.Parameters["reclaimPolicy"]
	switch reclaimPolicy {
	case "delete":
		return os.RemoveAll(oldPath)
	case "retain":
		return nil
	}

	// 默认是归档
	archivePath := filepath.Join(rootPath, archivePathName, pvPath)
	// 判断文件路径是否存在
	archiveParentPath := filepath.Join(archivePath, "../")
	if _, err := os.Stat(archiveParentPath); os.IsNotExist(err) {
		// 文件路径不存在，创建目录
		if err := os.MkdirAll(archiveParentPath, 0755); err != nil {
			fmt.Println("创建归档目录错误：" + archiveParentPath)
			return err
		}
	}
	glog.V(4).Infof("%s 使用归档模式回收 【%s】 回收路径：%s", logPrefix, oldPath, archivePath)
	return os.Rename(oldPath, archivePath)
}

// getClassForVolume returns StorageClass.
func (p *hostProvisioner) getClassForVolume(logPrefix string, ctx context.Context, pv *v1.PersistentVolume) (*storage.StorageClass, error) {
	if p.client == nil {
		return nil, fmt.Errorf(logPrefix + "cannot get kube client")
	}
	className := storagehelpers.GetPersistentVolumeClass(pv)
	if className == "" {
		return nil, fmt.Errorf(logPrefix + "volume has no storage class")
	}
	class, err := p.client.StorageV1().StorageClasses().Get(ctx, className, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	return class, nil
}

func main() {

	flag.Parse()
	flag.Set("logtostderr", "true")

	provisionerName := os.Getenv(provisionerNameKey)
	if provisionerName == "" {
		fmt.Printf("请设置提供者名称，环境变量：%s", provisionerNameKey)
	}

	hostDir := os.Getenv(hostDirKey)
	if hostDir == "" {
		fmt.Printf("请设置主机目录，环境变量： %s ", provisionerNameKey)
	}

	// 注册提供者到k8s
	kubeconfig := os.Getenv(kubeconfigKey)
	var config *rest.Config
	if kubeconfig != "" {
		// Create an OutOfClusterConfig and use it to create a client for the controller
		// to use to communicate with Kubernetes
		var err error
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			fmt.Printf("Failed to create kubeconfig: %v", err)
		}
	} else {
		// Create an InClusterConfig and use it to create a client for the controller
		// to use to communicate with Kubernetes
		var err error
		config, err = rest.InClusterConfig()
		if err != nil {
			fmt.Printf("Failed to create config: %v", err)
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Printf("Failed to create client: %v", err)
	}

	serverVersion, err := clientset.Discovery().ServerVersion()
	if err != nil {
		fmt.Printf("Error getting server version: %v", err)
	}

	leaderElection := true
	leaderElectionEnv := os.Getenv(enableLeaderElectionKey)
	if leaderElectionEnv != "" {
		leaderElection, err = strconv.ParseBool(leaderElectionEnv)
		if err != nil {
			fmt.Printf("Unable to parse %s env var: %v", enableLeaderElectionKey, err)
		}
	}

	clientHostProvisioner := &hostProvisioner{
		client:  clientset,
		hostDir: hostDir,
	}
	// Start the provision controller which will dynamically provision efs NFS
	// PVs
	pc := controller.NewProvisionController(clientset,
		provisionerName,
		clientHostProvisioner,
		serverVersion.GitVersion,
		controller.LeaderElection(leaderElection),
	)

	// Never stops.
	pc.Run(context.Background())

}
