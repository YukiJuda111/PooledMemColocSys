# README

## PRE CHECK

master和worker的配置都是在root权限下配置的，且本项目的运行涉及文件的读写也需要root权限，后续所有指令全部给root权限：

```bash
sudo su
```

先检查下集群的健康状态：

```bash
# (master)集群节点是否正常运行
kubectl get nodes 
kubectl get pods
```

目前已知117节点kubelet无法正常运行的情况及解决方案：

- contaierd没有正常运行，需要重启
- kubelet不是systemctl管理的kubelet，如果是snap管理的可能无法正常运行集群
- 交换分区没有关闭，117重启会导致交换分区打开,kubelet无法正常运行，关闭交换分区 `swapoff -a` 再重启kubelet `systemctl restart kubelet`
- 实在没办法了在118节点上删除117的节点`kubectl delete nodes cxl-server`,再在117重新加入一次集群：在118节点执行`kubeadm create token --print-join-command`获取加入指令，再在117节点通过指令加入集群`rm -rf /var/lib/kubelet/* /etc/cni/net.d/* /var/lib/cni/* /etc/kubernetes/*` `kubeadm join ...`

再检查下相关参数有没有正常配置好，看`./pkg/common/constant.go`中的变量，主要检查`K8sPodsBasePath`是否是正确的k8s cgroup路径(117是cgroups v2，肯定是正确的)，此外`KubeConfigPath`应该存在正确的kubeconfig

```go
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
```

没有kubeconfig的话应该从master节点获取对应的kubeconfig再放在worker节点中，并把`KubeConfigPath`设置为对应的路径：

```bash
# (master)
cat /etc/kubernetes/admin.conf
```

最后检查池化内存在系统中是NUMA2
```bash
# (worker)
numactl --hardware
```

## QUICK START

关于device plugin相关的原理及规范可以参考 [https://www.lixueduan.com/posts/kubernetes/21-device-plugin/](https://www.lixueduan.com/posts/kubernetes/21-device-plugin/)

在命令行启动本程序：

```bash
# (worker)在当前目录下
go run ./cmd/main.go
```

此时在118节点检查:

```bash
# (master)
kubectl describe nodes cxl-server
```

在Allocatable中会出现可用的资源`test.com/colocation-memory`

```bash
#(master) kubectl describe nodes cxl-server
Allocatable:
  cpu:                         64
  ephemeral-storage:           1698763807594
  hugepages-1Gi:               0
  hugepages-2Mi:               0
  memory:                      263713784Ki
  pods:                        110
  test.com/colocation-memory:  326
```

这时候117节点的程序的日志系统也应该正常运行了

部署一个Pod测试：
在118节点新建配置文件`test.yaml`，并部署在集群中，这里检查resource中的名称是否和上面node可用的资源名称一致，例如这里是`test.com/colocation-memory`

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
  namespace: colocation-memory
spec:
  nodeName: cxl-server
  containers:
    - name: colocation-memory-container
      image: docker.io/library/busybox:latest
      imagePullPolicy: IfNotPresent
      command: ["sh", "-c", "echo Hello, Kubernetes! && sleep 3600"]
      resources:
        requests:
          test.com/colocation-memory: "3"
        limits:
          test.com/colocation-memory: "3"
```

Pod启动成功时应该观测到的现象：
master节点上

```bash
# (master)kubectl get pods -n colocation-memory
NAME       READY   STATUS    RESTARTS   AGE
test-pod   1/1     Running   0          81s

# (master)kubectl exec  test-pod -n colocation-memory -- env | grep test.com/colocation-memory
test.com/colocation-memory=CM-4617e80b-784c-474f-af77-ae0d2b9e389f,CM-1aeb3175-97aa-4e3f-88fa-70ab7789dee9,CM-b2934c44-4227-43f7-a450-7273d8e960d7
```

运行日志中

```bash
I0607 18:12:03.553528 1155455 pods_monitor.go:303] [handlePodAdded] Pod created: colocation-memory/test-pod
I0607 18:12:03.553770 1155455 api.go:88] [Allocate] received request: CM-4617e80b-784c-474f-af77-ae0d2b9e389f,CM-1aeb3175-97aa-4e3f-88fa-70ab7789dee9,CM-b2934c44-4227-43f7-a450-7273d8e960d7
I0607 18:12:05.893678 1155455 pods_monitor.go:114] [waitForPodAndFetchEnv] Pod2PodInfo update: &{test-pod [CM-4617e80b-784c-474f-af77-ae0d2b9e389f CM-1aeb3175-97aa-4e3f-88fa-70ab7789dee9 CM-b2934c44-4227-43f7-a450-7273d8e960d7] 1155756 []}
I0607 18:12:05.893879 1155455 pods_monitor.go:126] [setCgroupsMemoryLimit] Read cgroup file: 0::/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-podc1bfa25a_6d80_4310_9cb4_8aed729ca1b4.slice/cri-containerd-fef3b73e4609257c5373fd0e21e670e0c1c599ada4e37ef6a35b1c42d8c7c625.scope
I0607 18:12:05.893942 1155455 pods_monitor.go:157] [setCgroupsMemoryLimit] Set memory limit for PID 1155756 to 1610612736 bytes
I0607 18:12:09.147473 1155455 memory_manager.go:141] [getSystemMemoryInfo] k8sOnlineMemoryUsage:761909248
```

## 注意事项

- 117和118的kubelet管理的容器是containerd，如果找不到镜像可以看看containerd中是否存在相应的镜像，检查docker是没用的
- 目前已知存在的问题是如果该程序进程被直接中断，可能会导致kubelet端仍然缓存了这次的设备列表，下次重新启用时会存在旧的设备列表，可以通过直接重新注册新的资源名称来解决，即修改`./pkg/common/constant.go`中的`ResourceName`
- 由于目前只存在117一个节点，没有其他节点进行反亲和性调度，`fallback_migrator.go`中的策略暂时未启用
