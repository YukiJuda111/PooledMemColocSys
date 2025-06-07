package device_plugin

import (
	"fmt"
	"liuyang/colocation-memory-device-plugin/pkg/common"
	"liuyang/colocation-memory-device-plugin/pkg/memory_manager"
	"liuyang/colocation-memory-device-plugin/pkg/utils"
	"sort"
	"strings"
	"time"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

type DeviceMonitor struct {
	devices map[string]*pluginapi.Device // uuid -> device
	notify  chan struct{}                // notify when device update
	mm      *memory_manager.MemoryManager
}

func NewDeviceMonitor(mm *memory_manager.MemoryManager) *DeviceMonitor {
	monitor := &DeviceMonitor{
		devices: make(map[string]*pluginapi.Device),
		notify:  make(chan struct{}),
		mm:      mm,
	}
	return monitor
}

// List all devices
func (d *DeviceMonitor) List() error {
	// for _, dev := range d.mm.ColocMemoryList {
	// 	d.devices = append(d.devices, &pluginapi.Device{
	// 		ID:     dev,
	// 		Health: pluginapi.Healthy,
	// 	})
	// }

	for _, dev := range d.mm.Uuid2ColocMetaData {
		d.devices[dev.Uuid] = &pluginapi.Device{
			ID:     dev.Uuid,
			Health: pluginapi.Healthy,
		}
	}

	return nil
}

// Watch device change
func (d *DeviceMonitor) Watch() error {

	ticker := time.NewTicker(common.RefreshInterval)
	defer ticker.Stop()

	for range ticker.C {
		// 防止pods_monitor在等待Pod进入running的时候还没更新ColocMetaData
		if d.mm.PodCreateRunning.Load() {
			klog.Info("[Watch] 有pod在创建过程中,跳过本次监控")
			continue
		}
		if d.mm.PeriodicReclaimRunning.Load() {
			klog.Info("[Watch] 有pod在回收过程中,跳过本次监控")
			continue
		}

		d.mm.UpdateState()
		if err := d.adjustDevices(); err != nil {
			klog.Errorf("[Watch] 调整设备失败: %v", err)
			return errors.WithMessagef(err, "调整设备失败")
		}
		klog.Info("[Watch] 混部内存块数量: ", d.mm.PrevBlocks)
	}

	return nil

}

func (d *DeviceMonitor) PeriodicReclaimCheck() {
	ticker := time.NewTicker(common.ReclaimCheckInterval)
	defer ticker.Stop()
	for range ticker.C {
		if d.mm.PodCreateRunning.Load() {
			klog.Info("[periodicReclaimCheck] 有pod在创建过程中,跳过本次巡检")
			continue
		}
		klog.Infof("[periodicReclaimCheck] 开始周期性检查交换块并尝试迁回 Pod")
		d.tryReclaimSwapBlocks()
	}
}

func (d *DeviceMonitor) tryReclaimSwapBlocks() {
	d.mm.PeriodicReclaimRunning.Store(true)
	defer func() {
		d.mm.PeriodicReclaimRunning.Store(false)
	}()

	for podName, podInfo := range d.mm.Pod2PodInfo {
		// 如果没有待回收的块，跳过
		if len(podInfo.SwapColocIds) == 0 {
			continue
		}

		// 检查是否有足够的空闲块（Used == false）
		freeCount := 0
		for _, meta := range d.mm.Uuid2ColocMetaData {
			if !meta.Used {
				freeCount++
			}
		}

		if freeCount < len(podInfo.SwapColocIds) {
			klog.Infof("[periodicReclaimCheck] 空闲块不足，无法将 pod %s 迁回", podName)
			continue
		}

		// 开始恢复块
		for _, blkID := range podInfo.SwapColocIds {
			d.generateBlock(true, blkID, podInfo.Name)
			podInfo.BindColocIds = append(podInfo.BindColocIds, blkID)
		}

		// 触发 Pod 迁移逻辑
		d.mm.MigratePod(podName, "2", "0,1")
		klog.Infof("[periodicCheck] Pod %s 已迁回，恢复 %d 个块", podName, len(podInfo.SwapColocIds))

		// 清空 SwapColocIds
		podInfo.SwapColocIds = []string{}
	}
}

// 调整虚拟块队列块数和设备列表
func (d *DeviceMonitor) adjustDevices() error {

	// 计算当前块数
	currentBlocks := int(d.mm.ColocMemory / common.BlockSize)
	delta := currentBlocks - d.mm.PrevBlocks

	// TODO: 为了测试方便,先不做防抖,记得改回来

	// // 滞后区间
	// if delta >= -common.DebounceThreshold && delta <= common.DebounceThreshold {
	// 	klog.Info("[adjustDevices] 防抖: 设备数量变化小于阈值，不做扩缩")
	// 	return nil
	// }

	// // 冷却保护
	// now := time.Now()
	// if !d.mm.LastUpdateTime.IsZero() && now.Sub(d.mm.LastUpdateTime) < common.MinAdjustmentInterval {
	// 	klog.Infof("[adjustDevices] 防抖: 两次调整间隔小于 %v,不做扩缩", common.MinAdjustmentInterval)
	// 	return nil
	// }

	switch {
	case delta > 0:
		// 增加块
		d.addColocDevices(delta)
	case delta < 0:
		// 减少块
		d.removeColocDevices(-delta)
	case delta == 0:
		klog.Info("[adjustDevices] 设备数量不变")
	}

	d.mm.PrevBlocks = currentBlocks
	d.mm.LastUpdateTime = time.Now()
	return nil
}

func (d *DeviceMonitor) addColocDevices(addCount int) {
	// // 增加块
	// remainingDelta := addCount

	/*
		这里的逻辑会产生一个问题：Pod会永远处于Swap状态，无法迁回到原来的节点
		因此考虑通过一个巡检任务回迁Pod
		新的逻辑在PeriodicReclaimCheck()中实现
	*/

	// // Step 1: 优先从 SwapColocIds 中取回块
	// for podName, podInfo := range d.mm.Pod2PodInfo {
	// 	if remainingDelta <= 0 {
	// 		break
	// 	}

	// 	// 如果 Pod 的 SwapColocIds 中的块数大于 remainingDelta或者不存在交换块，则跳过
	// 	if len(podInfo.SwapColocIds) == 0 || len(podInfo.SwapColocIds) > remainingDelta {
	// 		continue
	// 	}

	// 	for _, blkID := range podInfo.SwapColocIds {
	// 		if remainingDelta <= 0 {
	// 			break
	// 		}

	// 		d.generateBlock(true, blkID, podInfo.Name)
	// 		d.mm.Pod2PodInfo[podName].BindColocIds = append(d.mm.Pod2PodInfo[podName].BindColocIds, blkID)
	// 		remainingDelta--
	// 	}

	// 	d.mm.MigratePod(podName, "2", "0,1")

	// 	// 清空 Pod 的 SwapColocIds
	// 	d.mm.Pod2PodInfo[podName].SwapColocIds = []string{}

	// }

	// // Step 2: 如果不足，生成新的块
	for range addCount {
		d.generateBlock(false, "", "")
	}
	d.notify <- struct{}{}
	klog.Infof("[adjustDevices] 增加设备完成，总共增加设备数量: %d", addCount)
}

func (d *DeviceMonitor) removeColocDevices(removeCount int) {
	deletedCount := 0
	targetDeleteCount := removeCount

	// Step 1: 收集未使用的块
	unusedKeys := make([]string, 0, removeCount)
	for k, v := range d.mm.Uuid2ColocMetaData {
		if !v.Used {
			unusedKeys = append(unusedKeys, k)
			if len(unusedKeys) >= targetDeleteCount {
				break
			}
		}
	}

	// Step 2: 删除未使用的块
	for _, blkID := range unusedKeys {
		klog.Infof("[adjustDevices] 删除未使用块: %v", d.mm.Uuid2ColocMetaData[blkID])
		delete(d.mm.Uuid2ColocMetaData, blkID)
		delete(d.devices, blkID)
		deletedCount++
	}

	// Step 3: 如果不够，按 most_used 策略删除使用中的块（整 pod）
	if deletedCount < targetDeleteCount {
		type podStat struct {
			PodName  string
			BlockIDs []string
		}
		var podStats []podStat
		for podName, podInfo := range d.mm.Pod2PodInfo {
			podStats = append(podStats, podStat{
				PodName:  podName,
				BlockIDs: podInfo.BindColocIds,
			})
		}

		sort.Slice(podStats, func(i, j int) bool {
			return len(podStats[i].BlockIDs) > len(podStats[j].BlockIDs)
		})

		// 删除使用中块
		for _, pod := range podStats {
			if deletedCount >= targetDeleteCount {
				break
			}
			klog.Infof("[adjustDevices] 迁移 Pod: %s, 删除绑定块: %v", pod.PodName, pod.BlockIDs)
			for _, blkID := range pod.BlockIDs {
				delete(d.mm.Uuid2ColocMetaData, blkID)
				delete(d.devices, blkID)
				deletedCount++

				// 记录交换出去的块
				d.mm.Pod2PodInfo[pod.PodName].SwapColocIds = append(d.mm.Pod2PodInfo[pod.PodName].SwapColocIds, blkID)

				// 如果删除的块数量超过目标数量，生成新的块
				// 这样子对k8s来说多余的块空了出来，作为一个新的设备
				if deletedCount > targetDeleteCount {
					d.generateBlock(false, "", "")
				}
			}

			// 先看看NUMA节点的内存是否充足，不充足直接兜底迁移
			// 这段逻辑因为根据反亲和性调度，没有其他节点进行调度，暂时不启用

			// cxlNodeInfo, err := memory_manager.GetNumaMemInfo(2)
			// if err != nil {
			// 	klog.Errorf("[adjustDevices] 获取NUMA节点内存信息失败: %v", err)
			// }
			// if cxlNodeInfo.Free < common.BlockSize*uint64(len(d.mm.Pod2PodInfo[pod.PodName].SwapColocIds)) {
			// 	klog.Info("[adjustDevices] NUMA节点内存不足,兜底迁移,NUMA节点内存信息: ", cxlNodeInfo, "Pod信息: ", d.mm.Pod2PodInfo[pod.PodName])
			// // TODO: 这里别忘了修改yaml文件的路径
			// 	fallback_migrator.MigratePodToAnotherNode(fmt.Sprintf("/home/liuyang/i-device-plugin-main/deploy/%s.yaml", pod.PodName))
			// 	klog.Info("[adjustDevices] Pod迁移完成")
			// 	continue
			// }

			// 这里写死了NUMA node
			d.mm.MigratePod(pod.PodName, "0,1", "2")

			// 清空Pod绑定的虚拟内存块
			d.mm.Pod2PodInfo[pod.PodName].BindColocIds = []string{}

			klog.Infof("[adjustDevices] %s信息更新, BindColocIds数量: %d, SwapColocIds数量: %d", d.mm.Pod2PodInfo[pod.PodName].Name, len(d.mm.Pod2PodInfo[pod.PodName].BindColocIds), len(d.mm.Pod2PodInfo[pod.PodName].SwapColocIds))
		}
	}

	d.notify <- struct{}{}
	klog.Infof("[adjustDevices] 总共删除设备数量: %d(目标 %d)", deletedCount, targetDeleteCount)
}

func (d *DeviceMonitor) generateBlock(isSwap bool, swapColocId string, podName string) {

	var deviceId string

	switch isSwap {
	case true:
		// Step 1: 删除一个旧的 Used == false 的空闲块
		var removedID string
		for id, meta := range d.mm.Uuid2ColocMetaData {
			if !meta.Used {
				removedID = id
				break
			}
		}

		if removedID != "" {
			delete(d.mm.Uuid2ColocMetaData, removedID)
			delete(d.devices, removedID)
		} else {
			klog.Warningf("[generateBlock] 未找到可回收的空闲块，无法清理空间恢复块 %s", swapColocId)
		}

		// Step 2: 恢复 swapColocId 块为已用状态
		deviceId = swapColocId
		d.mm.Uuid2ColocMetaData[deviceId] = &memory_manager.ColocMemoryBlockMetaData{
			Uuid:       deviceId,
			Used:       true,
			BindPod:    podName,
			UpdateTime: time.Now(),
		}
	case false:
		deviceId = fmt.Sprintf(common.DeviceName, utils.GetUuid())
		d.mm.Uuid2ColocMetaData[deviceId] = &memory_manager.ColocMemoryBlockMetaData{
			Uuid:       deviceId,
			Used:       false,
			BindPod:    "",
			UpdateTime: time.Now(),
		}
	}

	d.devices[deviceId] = &pluginapi.Device{
		ID:     deviceId,
		Health: pluginapi.Healthy,
	}
}

// Devices transformer map to slice
func (d *DeviceMonitor) Devices() []*pluginapi.Device {
	devices := make([]*pluginapi.Device, 0, len(d.devices))
	for _, device := range d.devices {
		devices = append(devices, device)
	}
	return devices
}

func String(devs []*pluginapi.Device) string {
	ids := make([]string, 0, len(devs))
	for _, device := range devs {
		ids = append(ids, device.ID)
	}
	return strings.Join(ids, ",")
}
