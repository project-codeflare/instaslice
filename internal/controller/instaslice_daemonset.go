/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	inferencev1alpha1 "codeflare.dev/instaslice/api/v1alpha1"
	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/go-logr/logr"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nvdevice "github.com/NVIDIA/go-nvlib/pkg/nvlib/device"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InstaSliceDaemonsetReconciler reconciles a InstaSliceDaemonset object
type InstaSliceDaemonsetReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	kubeClient *kubernetes.Clientset
}

//+kubebuilder:rbac:groups=inference.codeflare.dev,resources=instaslices,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=inference.codeflare.dev,resources=instaslices/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=inference.codeflare.dev,resources=instaslices/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Instaslice object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.2/pkg/reconcile

var discoveredGpusOnHost []string

// Additional handler used for making NVML calls.
type deviceHandler struct {
	nvdevice nvdevice.Interface
	nvml     nvml.Interface
}

type MigProfile struct {
	C              int
	G              int
	GB             int
	GIProfileID    int
	CIProfileID    int
	CIEngProfileID int
}

const (
	// AttributeMediaExtensions holds the string representation for the media extension MIG profile attribute.
	AttributeMediaExtensions = "me"
)

func (r *InstaSliceDaemonsetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)
	logger := log.Log.WithName("InstaSlice-controller")

	pod := &v1.Pod{}
	//var podName string
	err := r.Get(ctx, req.NamespacedName, pod)
	if err != nil {
		if errors.IsNotFound(err) {
			// Pod not found. It might have been deleted.
			return ctrl.Result{}, nil
		}
		// Error fetching the Pod
		logger.Info("Error in fetching the latest version of the pod")
		return ctrl.Result{}, nil
	}

	if pod.Labels["processedbydeamonset"] == "true" && !pod.DeletionTimestamp.IsZero() {

		logger.Info("Performing cleanup ", "pod", pod.Name)

		r.cleanUp(ctx, pod, logger)
	}

	//check if pod is already processed by daemonset.
	daemonsetProcessingDone := pod.Labels["processedbydeamonset"]
	if strings.ToLower(daemonsetProcessingDone) == "true" {
		return ctrl.Result{}, nil
	}

	//check if pod needs new slice to be generated.
	decisionToCreateSlice := pod.Labels["generateslice"]
	boolDecisionToCreateSlice := false
	if strings.ToLower(decisionToCreateSlice) == "true" {
		boolDecisionToCreateSlice = true
	}

	//check if controller processed pod and added placement information.
	var boolcontrollerProcessingDone bool
	controllerProcessingDone := pod.Labels["processedbycontroller"]
	if strings.ToLower(controllerProcessingDone) == "true" {
		boolcontrollerProcessingDone = true
	}

	if boolDecisionToCreateSlice && boolcontrollerProcessingDone && pod.Status.Phase != v1.PodSucceeded {
		//Assume pod only has one container with one GPU request
		var profileName string
		var Giprofileid int
		var Ciprofileid int
		var CiEngProfileid int
		var deviceUUID string
		var migUUID string
		var deviceForMig string
		var instasliceList inferencev1alpha1.InstasliceList
		var giId uint32
		var ciId uint32
		ret := nvml.Init()
		if ret != nvml.SUCCESS {
			fmt.Printf("Unable to initialize NVML: %v \n", nvml.ErrorString(ret))
		}

		availableGpus, ret := nvml.DeviceGetCount()
		if ret != nvml.SUCCESS {
			fmt.Printf("Unable to get device count: %v \n", nvml.ErrorString(ret))
		}

		deviceForMig, profileName, Giprofileid, Ciprofileid, CiEngProfileid = r.getAllocation(ctx, instasliceList, deviceForMig, profileName, Giprofileid, Ciprofileid, CiEngProfileid)
		placement := nvml.GpuInstancePlacement{}
		for i := 0; i < availableGpus; i++ {
			device, ret := nvml.DeviceGetHandleByIndex(i)
			if ret != nvml.SUCCESS {
				fmt.Printf("Unable to get device at index %d: %v \n", i, nvml.ErrorString(ret))
			}

			uuid, ret := device.GetUUID()
			if ret != nvml.SUCCESS {
				fmt.Printf("Unable to get uuid of device at index %d: %v \n", i, nvml.ErrorString(ret))
			}
			if deviceForMig != uuid {
				continue
			}
			deviceUUID = uuid
			gpuPlacement := pod.Labels["gpuUUID"]

			//Move to next GPU as this is not the selected GPU by the controller.

			if gpuPlacement != uuid {
				continue
			}

			device, retCodeForDevice := nvml.DeviceGetHandleByUUID(uuid)

			if retCodeForDevice != nvml.SUCCESS {
				fmt.Printf("error getting GPU device handle: %v \n", ret)
			}

			giProfileInfo, retCodeForGi := device.GetGpuInstanceProfileInfo(Giprofileid)
			if retCodeForGi != nvml.SUCCESS {
				logger.Error(err, "error getting GPU instance profile info", "giProfileInfo", giProfileInfo, "retCodeForGi", retCodeForGi)
			}

			logger.Info("The profile id is", "giProfileInfo", giProfileInfo.Id, "Memory", giProfileInfo.MemorySizeMB)

			// Path to the file containing the node name
			updatedPlacement := r.getAllocationsToprepare(ctx, placement)
			gi, retCodeForGiWithPlacement := device.CreateGpuInstanceWithPlacement(&giProfileInfo, &updatedPlacement)
			if retCodeForGiWithPlacement != nvml.SUCCESS {
				fmt.Printf("error creating GPU instance for '%v': %v \n ", &giProfileInfo, retCodeForGiWithPlacement)
			}
			giInfo, retForGiInfor := gi.GetInfo()
			if retForGiInfor != nvml.SUCCESS {
				fmt.Printf("error getting GPU instance info for '%v': %v \n", &giProfileInfo, retForGiInfor)
			}
			//TODO: figure out the compute slice scenario, I think Kubernetes does not support this use case yet
			ciProfileInfo, retCodeForCiProfile := gi.GetComputeInstanceProfileInfo(Ciprofileid, CiEngProfileid)
			if retCodeForCiProfile != nvml.SUCCESS {
				fmt.Printf("error getting Compute instance profile info for '%v': %v \n", ciProfileInfo, retCodeForCiProfile)
			}
			ci, retCodeForComputeInstance := gi.CreateComputeInstance(&ciProfileInfo)
			if retCodeForComputeInstance != nvml.SUCCESS {
				fmt.Printf("error creating Compute instance for '%v': %v \n", ci, retCodeForComputeInstance)
			}
			//get created mig details
			giId, migUUID, ciId = r.getCreatedSliceDetails(giId, giInfo, ret, device, uuid, profileName, migUUID, ciId)
			//create slice only on one GPU, both CI and GI creation are succeeded.
			if retCodeForCiProfile == retCodeForGi {
				break
			}

		}
		nodeName := os.Getenv("NODE_NAME")
		failure, result, errorUpdatingCapacity := r.updateNodeCapacity(ctx, nodeName)
		if failure {
			return result, errorUpdatingCapacity
		}
		//TODO: Remove this call
		r.delayUngating()
		typeNamespacedName := types.NamespacedName{
			Name:      nodeName,
			Namespace: "default",
		}
		instaslice := &inferencev1alpha1.Instaslice{}
		errGettingobj := r.Get(context.TODO(), typeNamespacedName, instaslice)

		if errGettingobj != nil {
			fmt.Printf("Error getting instaslice obj %v", errGettingobj)
		}
		existingAllocations, updatedAllocation := r.updateAllocationProcessing(instaslice, deviceUUID, profileName)
		r.createPreparedEntry(profileName, placement, deviceUUID, pod, giId, ciId, instaslice, migUUID, updatedAllocation)

		createConfigMap(context.TODO(), r.Client, migUUID, existingAllocations.Namespace, existingAllocations.PodName, logger)

		podUpdate := r.labelsForDaemonset(pod)
		// Retry update operation with backoff
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			return r.Client.Update(ctx, podUpdate)
		})
		if retryErr != nil {
			fmt.Printf("Error adding labels from daemonset controller %v", retryErr)
		}

	}
	if boolcontrollerProcessingDone {
		podUpdate := r.labelsForDaemonset(pod)
		// Retry update operation with backoff
		retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			return r.Client.Update(ctx, podUpdate)
		})
		if retryErr != nil {
			fmt.Printf("Error adding labels from daemonset controller in boolcontrollerProcessingDone %v", retryErr)
		}
	}

	return ctrl.Result{}, nil
}

func (r *InstaSliceDaemonsetReconciler) getAllocationsToprepare(ctx context.Context, placement nvml.GpuInstancePlacement) nvml.GpuInstancePlacement {
	var instasliceList inferencev1alpha1.InstasliceList
	if err := r.List(ctx, &instasliceList, &client.ListOptions{}); err != nil {
		fmt.Printf("Error listing Instaslice %v", err)
	}
	for _, instaslice := range instasliceList.Items {

		nodeName := os.Getenv("NODE_NAME")
		if instaslice.Name == nodeName {
			for _, v := range instaslice.Spec.Allocations {
				if v.Processed == "no" {
					placement.Size = v.Size
					placement.Start = v.Start
				}
			}
		}
	}
	return placement
}

func (*InstaSliceDaemonsetReconciler) getCreatedSliceDetails(giId uint32, giInfo nvml.GpuInstanceInfo, ret nvml.Return, device nvml.Device, uuid string, profileName string, migUUID string, ciId uint32) (uint32, string, uint32) {
	giId = giInfo.Id
	h := &deviceHandler{}
	h.nvml = nvml.New()
	h.nvdevice = nvdevice.New(nvdevice.WithNvml(h.nvml))

	ret1 := h.nvml.Init()
	if ret1 != nvml.SUCCESS {
		fmt.Printf("Unable to initialize NVML: %v", nvml.ErrorString(ret))
	}
	nvlibParentDevice, err := h.nvdevice.NewDevice(device)
	if err != nil {
		fmt.Printf("unable to get nvlib GPU parent device for MIG UUID '%v': %v", uuid, ret)
	}
	migs, err := nvlibParentDevice.GetMigDevices()
	if err != nil {
		fmt.Printf("unable to get MIG devices on GPU '%v': %v", uuid, err)
	}
	for _, mig := range migs {
		obtainedProfileName, _ := mig.GetProfile()
		fmt.Printf("obtained profile is %v\n", obtainedProfileName)
		giID, ret := mig.GetGpuInstanceId()
		if ret != nvml.SUCCESS {
			fmt.Printf("error getting GPU instance ID for MIG device: %v", ret)
		}
		gpuInstance, err1 := device.GetGpuInstanceById(giID)
		if err1 != nvml.SUCCESS {
			fmt.Printf("Unable to get GPU instance %v\n", err1)
		}
		gpuInstanceInfo, err2 := gpuInstance.GetInfo()
		if err2 != nvml.SUCCESS {
			fmt.Printf("Unable to get GPU instance info %v\n", err2)
		}
		fmt.Printf("The instance info size %v and start %v\n", gpuInstanceInfo.Placement.Size, gpuInstanceInfo.Placement.Start)

		if profileName == obtainedProfileName.String() {
			realizedMig, _ := mig.GetUUID()
			migUUID = realizedMig
			migCid, _ := mig.GetComputeInstanceId()
			ci, _ := gpuInstance.GetComputeInstanceById(migCid)
			ciMigInfo, _ := ci.GetInfo()
			ciId = ciMigInfo.Id

		}
	}
	return giId, migUUID, ciId
}

func (r *InstaSliceDaemonsetReconciler) getAllocation(ctx context.Context, instasliceList inferencev1alpha1.InstasliceList, deviceForMig string, profileName string, Giprofileid int, Ciprofileid int, CiEngProfileid int) (string, string, int, int, int) {
	if err := r.List(ctx, &instasliceList, &client.ListOptions{}); err != nil {
		fmt.Printf("Error listing Instaslice %v", err)
	}
	for _, instaslice := range instasliceList.Items {
		nodeName := os.Getenv("NODE_NAME")
		if instaslice.Name == nodeName {
			for k, v := range instaslice.Spec.Allocations {
				if v.Processed == "no" {
					deviceForMig = k
					profileName = v.Profile
					Giprofileid = v.Giprofileid
					Ciprofileid = v.CIProfileID
					CiEngProfileid = v.CIEngProfileID
				}
			}
		}
	}
	return deviceForMig, profileName, Giprofileid, Ciprofileid, CiEngProfileid
}

func (r *InstaSliceDaemonsetReconciler) cleanUp(ctx context.Context, pod *v1.Pod, logger logr.Logger) {
	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		logger.Error(ret, "Unable to initialize NVML")
	}
	var instasliceList inferencev1alpha1.InstasliceList
	if err := r.List(ctx, &instasliceList, &client.ListOptions{}); err != nil {
		fmt.Printf("Error listing Instaslice %v", err)
	}
	for _, instaslice := range instasliceList.Items {

		nodeName := os.Getenv("NODE_NAME")
		if instaslice.Name == nodeName {
			prepared := instaslice.Spec.Prepared
			var candidateDel string
			for migUUID, value := range prepared {
				if value.PodUUID == string(pod.UID) {
					parent, errRecievingDeviceHandle := nvml.DeviceGetHandleByUUID(value.Parent)
					if errRecievingDeviceHandle != nvml.SUCCESS {
						logger.Error(errRecievingDeviceHandle, "Error obtaining GPU handle")
					}
					gi, errRetrievingGi := parent.GetGpuInstanceById(int(value.Giinfoid))
					if errRetrievingGi != nvml.SUCCESS {
						logger.Error(errRetrievingGi, "Error obtaining GPU instance")
					}
					ci, errRetrievingCi := gi.GetComputeInstanceById(int(value.Ciinfoid))
					if errRetrievingCi != nvml.SUCCESS {
						logger.Error(errRetrievingCi, "Error obtaining Compute instance")
					}
					errDestroyingCi := ci.Destroy()
					if errDestroyingCi != nvml.SUCCESS {
						logger.Error(errDestroyingCi, "Error deleting Compute instance")
					}
					errDestroyingGi := gi.Destroy()
					if errDestroyingGi != nvml.SUCCESS {
						logger.Error(errDestroyingGi, "Error deleting GPU instance")
					}
					candidateDel = migUUID
					logger.Info("Done deleting MIG slice for pod", "UUID", value.PodUUID)
				}
			}
			delete(instaslice.Spec.Prepared, candidateDel)

			for key, allocation := range instaslice.Spec.Allocations {
				if allocation.PodUUID == string(pod.UID) {
					deleteConfigMap(context.TODO(), r.Client, allocation.PodName, allocation.Namespace)
					delete(instaslice.Spec.Allocations, key)
					break
				}
			}
			err := r.Update(ctx, &instaslice)
			if err != nil {
				logger.Error(err, "Error updating InstasSlice object")
			}
			r.updateNodeCapacity(ctx, nodeName)
		}
	}
}

func (r *InstaSliceDaemonsetReconciler) createPreparedEntry(profileName string, placement nvml.GpuInstancePlacement, deviceUUID string, pod *v1.Pod, giId uint32, ciId uint32, instaslice *inferencev1alpha1.Instaslice, migUUID string, updatedAllocation inferencev1alpha1.AllocationDetails) {
	instaslicePrepared := inferencev1alpha1.PreparedDetails{
		Profile:  profileName,
		Start:    placement.Start,
		Size:     placement.Size,
		Parent:   deviceUUID,
		PodUUID:  string(pod.UID),
		Giinfoid: giId,
		Ciinfoid: ciId,
	}
	if instaslice.Spec.Prepared == nil {
		instaslice.Spec.Prepared = make(map[string]inferencev1alpha1.PreparedDetails)
	}
	instaslice.Spec.Prepared[migUUID] = instaslicePrepared
	instaslice.Spec.Allocations[deviceUUID] = updatedAllocation

	errForUpdate := r.Update(context.TODO(), instaslice)

	if errForUpdate != nil {
		fmt.Printf("Error updating object %v", errForUpdate)
	}
}

func (*InstaSliceDaemonsetReconciler) updateAllocationProcessing(instaslice *inferencev1alpha1.Instaslice, deviceUUID string, profileName string) (inferencev1alpha1.AllocationDetails, inferencev1alpha1.AllocationDetails) {
	existingAllocations := instaslice.Spec.Allocations[deviceUUID]
	updatedAllocation := inferencev1alpha1.AllocationDetails{
		Profile:     profileName,
		Start:       existingAllocations.Start,
		Size:        existingAllocations.Size,
		PodUUID:     existingAllocations.PodUUID,
		Nodename:    existingAllocations.Nodename,
		Giprofileid: existingAllocations.Giprofileid,
		Processed:   "yes",
		Namespace:   existingAllocations.Namespace,
		PodName:     existingAllocations.PodName,
	}
	return existingAllocations, updatedAllocation
}

// Reloads the configuration in the device plugin to update node capacity
func (r *InstaSliceDaemonsetReconciler) updateNodeCapacity(ctx context.Context, nodeName string) (bool, reconcile.Result, error) {
	node := &v1.Node{}
	nodeNameObject := types.NamespacedName{Name: nodeName}
	err := r.Get(ctx, nodeNameObject, node)
	if err != nil {
		fmt.Println("unable to fetch NodeLabeler, cannot update capacity")
	}
	// Check and update the label
	//TODO: change label name
	updated := false
	if value, exists := node.Labels["nvidia.com/device-plugin.config"]; exists && value == "a100-40gb-1" {
		node.Labels["nvidia.com/device-plugin.config"] = "a100-40gb"
		updated = true
	}
	if !updated {
		if value, exists := node.Labels["nvidia.com/device-plugin.config"]; exists && value == "a100-40gb" {
			node.Labels["nvidia.com/device-plugin.config"] = "a100-40gb-1"
			updated = true
		}
	}

	if updated {
		err = r.Update(ctx, node)
		if err != nil {
			fmt.Println("unable to update Node")
		}
	}
	return false, reconcile.Result{}, nil
}

// patch pods once slices are created on the device.
func (*InstaSliceDaemonsetReconciler) labelsForDaemonset(pod *v1.Pod) *v1.Pod {
	labels := pod.GetLabels()
	labels["processedbydeamonset"] = "true"
	pod.SetLabels(labels)
	return pod
}

// SetupWithManager sets up the controller with the Manager.
func (r *InstaSliceDaemonsetReconciler) SetupWithManager(mgr ctrl.Manager) error {

	restConfig := mgr.GetConfig()

	var err error
	r.kubeClient, err = kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}

	_, errForDiscoveringGpus := r.discoverMigEnabledGpuWithSlices()
	if errForDiscoveringGpus != nil {
		return errForDiscoveringGpus
	}
	//r.discoverExistingSlice()
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.Pod{}).Named("InstaSliceDaemonSet").
		Complete(r)
}

// TODO: be more sophisticated, check if k8s-device-pugin is running and then ungate the pod with some delay
func (r *InstaSliceDaemonsetReconciler) delayUngating() {
	time.Sleep(10 * time.Second)
}

// This function discovers MIG devices as the plugin comes up. this is run exactly once.
func (r *InstaSliceDaemonsetReconciler) discoverMigEnabledGpuWithSlices() ([]string, error) {
	instaslice, _, gpuModelMap, failed, returnValue, errorDiscoveringProfiles := r.discoverAvailableProfilesOnGpus()
	if failed {
		return returnValue, errorDiscoveringProfiles
	}

	err := r.discoverDanglingSlices(instaslice)

	if err != nil {
		return nil, err
	}

	// Path to the file containing the node name
	nodeName := os.Getenv("NODE_NAME")
	instaslice.Name = nodeName
	instaslice.Namespace = "default"
	instaslice.Spec.MigGPUUUID = gpuModelMap
	instaslice.Status.Processed = "true"
	customCtx := context.TODO()
	errToCreate := r.Create(customCtx, instaslice)
	if errToCreate != nil {
		return nil, errToCreate
	}

	// Object exists, update its status
	instaslice.Status.Processed = "true"
	if errForStatus := r.Status().Update(customCtx, instaslice); errForStatus != nil {
		return nil, errForStatus
	}

	return discoveredGpusOnHost, nil
}

func (*InstaSliceDaemonsetReconciler) discoverAvailableProfilesOnGpus() (*inferencev1alpha1.Instaslice, nvml.Return, map[string]string, bool, []string, error) {
	instaslice := &inferencev1alpha1.Instaslice{}
	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		return nil, ret, nil, false, nil, ret
	}

	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return nil, ret, nil, false, nil, ret
	}
	gpuModelMap := make(map[string]string)
	discoverProfilePerNode := true
	for i := 0; i < count; i++ {
		device, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			return nil, ret, nil, false, nil, ret
		}

		uuid, _ := device.GetUUID()
		gpuName, _ := device.GetName()
		gpuModelMap[uuid] = gpuName
		discoveredGpusOnHost = append(discoveredGpusOnHost, uuid)
		if discoverProfilePerNode {

			for i := 0; i < nvml.GPU_INSTANCE_PROFILE_COUNT; i++ {
				giProfileInfo, ret := device.GetGpuInstanceProfileInfo(i)
				if ret == nvml.ERROR_NOT_SUPPORTED {
					continue
				}
				if ret == nvml.ERROR_INVALID_ARGUMENT {
					continue
				}
				if ret != nvml.SUCCESS {
					return nil, ret, nil, false, nil, ret
				}

				memory, ret := device.GetMemoryInfo()
				if ret != nvml.SUCCESS {
					return nil, ret, nil, false, nil, ret
				}

				profile := NewMigProfile(i, i, nvml.COMPUTE_INSTANCE_ENGINE_PROFILE_SHARED, giProfileInfo.SliceCount, giProfileInfo.SliceCount, giProfileInfo.MemorySizeMB, memory.Total)

				giPossiblePlacements, ret := device.GetGpuInstancePossiblePlacements(&giProfileInfo)
				if ret == nvml.ERROR_NOT_SUPPORTED {
					continue
				}
				if ret == nvml.ERROR_INVALID_ARGUMENT {
					continue
				}
				if ret != nvml.SUCCESS {
					return nil, 0, nil, true, nil, ret
				}
				placementsForProfile := []inferencev1alpha1.Placement{}
				for _, p := range giPossiblePlacements {
					placement := inferencev1alpha1.Placement{
						Size:  int(p.Size),
						Start: int(p.Start),
					}
					placementsForProfile = append(placementsForProfile, placement)
				}

				aggregatedPlacementsForProfile := inferencev1alpha1.Mig{
					Placements:     placementsForProfile,
					Profile:        profile.String(),
					Giprofileid:    i,
					CIProfileID:    profile.CIProfileID,
					CIEngProfileID: profile.CIEngProfileID,
				}
				instaslice.Spec.Migplacement = append(instaslice.Spec.Migplacement, aggregatedPlacementsForProfile)
			}
			discoverProfilePerNode = false
		}
	}
	return instaslice, ret, gpuModelMap, false, nil, nil
}

func (*InstaSliceDaemonsetReconciler) discoverDanglingSlices(instaslice *inferencev1alpha1.Instaslice) error {
	h := &deviceHandler{}
	h.nvml = nvml.New()
	h.nvdevice = nvdevice.New(nvdevice.WithNvml(h.nvml))

	errInitNvml := h.nvml.Init()
	if errInitNvml != nvml.SUCCESS {
		return errInitNvml
	}

	availableGpusOnNode, errObtainingDeviceCount := h.nvml.DeviceGetCount()
	if errObtainingDeviceCount != nvml.SUCCESS {
		return errObtainingDeviceCount
	}

	for i := 0; i < availableGpusOnNode; i++ {
		device, errObtainingDeviceHandle := h.nvml.DeviceGetHandleByIndex(i)
		if errObtainingDeviceHandle != nvml.SUCCESS {
			return errObtainingDeviceHandle
		}

		uuid, errObtainingDeviceUUID := device.GetUUID()
		if errObtainingDeviceUUID != nvml.SUCCESS {
			return errObtainingDeviceUUID
		}

		nvlibParentDevice, errObtainingParentDevice := h.nvdevice.NewDevice(device)
		if errObtainingParentDevice != nil {
			return errObtainingParentDevice
		}
		migs, errRetrievingMigDevices := nvlibParentDevice.GetMigDevices()
		if errRetrievingMigDevices != nil {
			return errRetrievingMigDevices
		}

		for _, mig := range migs {
			migUUID, _ := mig.GetUUID()
			profile, errForProfile := mig.GetProfile()
			if errForProfile != nil {
				return errForProfile
			}

			giID, errForMigGid := mig.GetGpuInstanceId()
			if errForMigGid != nvml.SUCCESS {
				return errForMigGid
			}
			gpuInstance, errRetrievingDeviceGid := device.GetGpuInstanceById(giID)
			if errRetrievingDeviceGid != nvml.SUCCESS {
				return errRetrievingDeviceGid
			}
			gpuInstanceInfo, errObtainingInfo := gpuInstance.GetInfo()
			if errObtainingInfo != nvml.SUCCESS {
				return errObtainingInfo
			}

			ciID, ret := mig.GetComputeInstanceId()
			if ret != nvml.SUCCESS {
				return ret
			}
			ci, ret := gpuInstance.GetComputeInstanceById(ciID)
			if ret != nvml.SUCCESS {
				return ret
			}
			ciInfo, ret := ci.GetInfo()
			if ret != nvml.SUCCESS {
				return ret
			}
			prepared := inferencev1alpha1.PreparedDetails{
				Profile:  profile.GetInfo().String(),
				Start:    gpuInstanceInfo.Placement.Start,
				Size:     gpuInstanceInfo.Placement.Size,
				Parent:   uuid,
				Giinfoid: gpuInstanceInfo.Id,
				Ciinfoid: ciInfo.Id,
			}
			if instaslice.Spec.Prepared == nil {
				instaslice.Spec.Prepared = make(map[string]inferencev1alpha1.PreparedDetails)
			}
			instaslice.Spec.Prepared[migUUID] = prepared
		}
	}
	return nil
}

// NewMigProfile constructs a new MigProfile struct using info from the giProfiles and ciProfiles used to create it.
func NewMigProfile(giProfileID, ciProfileID, ciEngProfileID int, giSliceCount, ciSliceCount uint32, migMemorySizeMB, totalDeviceMemoryBytes uint64) *MigProfile {
	return &MigProfile{
		C:              int(ciSliceCount),
		G:              int(giSliceCount),
		GB:             int(getMigMemorySizeInGB(totalDeviceMemoryBytes, migMemorySizeMB)),
		GIProfileID:    giProfileID,
		CIProfileID:    ciProfileID,
		CIEngProfileID: ciEngProfileID,
	}
}

// Helper function to get GPU memory size in GBs.
func getMigMemorySizeInGB(totalDeviceMemory, migMemorySizeMB uint64) uint64 {
	const fracDenominator = 8
	const oneMB = 1024 * 1024
	const oneGB = 1024 * 1024 * 1024
	fractionalGpuMem := (float64(migMemorySizeMB) * oneMB) / float64(totalDeviceMemory)
	fractionalGpuMem = math.Ceil(fractionalGpuMem*fracDenominator) / fracDenominator
	totalMemGB := float64((totalDeviceMemory + oneGB - 1) / oneGB)
	return uint64(math.Round(fractionalGpuMem * totalMemGB))
}

// String returns the string representation of a MigProfile.
func (m MigProfile) String() string {
	var suffix string
	if len(m.Attributes()) > 0 {
		suffix = "+" + strings.Join(m.Attributes(), ",")
	}
	if m.C == m.G {
		return fmt.Sprintf("%dg.%dgb%s", m.G, m.GB, suffix)
	}
	return fmt.Sprintf("%dc.%dg.%dgb%s", m.C, m.G, m.GB, suffix)
}

// Attributes returns the list of attributes associated with a MigProfile.
func (m MigProfile) Attributes() []string {
	var attr []string
	switch m.GIProfileID {
	case nvml.GPU_INSTANCE_PROFILE_1_SLICE_REV1:
		attr = append(attr, AttributeMediaExtensions)
	}
	return attr
}

// Create configmap which is used by Pods to consume MIG device
func createConfigMap(ctx context.Context, k8sClient client.Client, migGPUUUID string, namespace string, podName string, logger logr.Logger) error {
	configMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
		},
		Data: map[string]string{
			"NVIDIA_VISIBLE_DEVICES": migGPUUUID,
			"CUDA_VISIBLE_DEVICES":   migGPUUUID,
		},
	}

	err := k8sClient.Create(ctx, configMap)
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to create ConfigMap")
		return err
	}
	logger.Info("ConfigMap created successfully", "ConfigMap.Name", configMap.Name)
	return nil
}

// Manage lifecycle of configmap, delete it once the pod is deleted from the system
func deleteConfigMap(ctx context.Context, k8sClient client.Client, configMapName string, namespace string) error {
	// Define the ConfigMap object with the name and namespace
	configMap := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
		},
	}

	err := k8sClient.Delete(ctx, configMap)
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to delete ConfigMap")
		return err
	}
	fmt.Printf("ConfigMap deleted successfully %v", configMapName)
	return nil
}
