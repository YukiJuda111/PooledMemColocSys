package common

import "time"

const (
	ResourceName   string = "test.com/colocation-memory"
	DeviceSocket   string = "colocation-memory.sock"
	ConnectTimeout        = time.Second * 5
	BlockSize             = 512 * 1024 * 1024 // 512MB

	// 这里的安全水位有两层含义：
	// 1.系统本身就有除k8s以外的其他进程在运行，需要留出一定的内存空间
	// 2.在做虚拟内存块计算时，采用了冷却时间+滞后区间来防抖动，需要留出一定的内存空间来防止实际内存溢出
	SafetyWatermark = 0.1 // 10%安全水位

	K8sPodsBasePath = "/sys/fs/cgroup/kubepods.slice"
	BurstablePath   = "/kubepods-burstable.slice/memory.current"
	BestEffortPath  = "/kubepods-besteffort.slice/memory.current"
	RefreshInterval = 10 * time.Second
	DeviceName      = "CM-%s"                // 设备名称
	KubeConfigPath  = "/home/liuyang/config" // kubeconfig路径

	DebounceThreshold     = 1                // 防抖阈值
	MinAdjustmentInterval = 60 * time.Second // 最小调整间隔

	ReclaimCheckInterval = 13 * time.Second // 回收Pod检查间隔
)
