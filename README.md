# filesystem provisioner

用于为k8s提供pvc卷，仿照nfs provisioner开发

## 部署
> 查看deploy目录
## 配置
### 环境变量
* `* PROVISIONER_NAME` 提供者名称
* `* HOST_DIR` 主机目录
* `KUBECONFIG` k8s配置，默认使用当前pod中的
* `ENABLE_LEADER_ELECTION` 是否开启选举，多个节点时启用
### storageclass 配置
* `pvPathTemplate` pv目录模板
  > provisioner 会根据用户配置的模板生成挂载到容器的目录
  * 默认值：`{namespace}-{pvcName}-{pvName}`
  * 模板可用变量：
    * `provisioner` 提供者名称
    * `pvcUid` pvc id
    * `namespace` 命名空间
    * `pvcName` pvc 名称
    * `pvName` pv 名称
    * PVC Annotations 中 `pv-path-var/`开头的注解
* `mount-options` 挂载参数
* `reclaimPolicy` 回收策略
  * `delete` 直接删除
  * `retain` 保留
  * `archive` 归档，默认方式