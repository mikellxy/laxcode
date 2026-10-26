现在laxcode manager管理epoch和ebpf mpas这块实现的比较乱，我打算重构一下：
* 服务还没对外使用，我已经删除了laxcode所有的用户数据、各sqlite db数据、ebpf挂载数据。
* 你只需要看新流程是否能跑通，不需要兼容老数据。 重构落地编码时也不需要考虑兼容老数据。
* 需要删除数据库字段的地方，你只需改代码中的建表语句，我后面在db中执行drop。

重构需要定义一个 func (m *manager) SyncEpochToEbpf(ctx, userID) 方法，入参为manager db users表的ID，可以通过uuid查询到，职责为：
* 给用户沙箱授权、追加授权流量
* 处理好所有与 ebpf 和 db 的数据同步相关的问题
* 处理好跨天问题

以下逻辑都需要重构，重构后他们都复用 SyncEpochToEbpf 的能力：
* manager启动时，不要先关闭 active epoch，而是把 active epoch 循环交给 SyncEpochToEbpf 处理
* manager对外的 /network-usage, /ensure 接口，uuid反查userID，然后调用 SyncEpochToEbpf 处理业务逻辑
* ebpf ring 异步申请追加授权，调用 SyncEpochToEbpf 处理
* 追加授权的定时任务(如有)，循环交给 SyncEpochToEbpf 处理

关键规则：
**这是重构过程中，你需要严格遵守的规则。如之前所属老数据已经全部清空，不需要考虑这些规则是否跟老数据冲突/兼容**
1. 规则一：
   * 后续任何提到用户的bucket_meta、grants、usage 不完整存在，指的是至少存在一个，但不是三个都存在
2. 规则二
   * 重构后，每个用户的 bucket_meta、grants、usage 都是 SyncEpochToEbpf 负责插入ebpf maps的
   * 同时目前ebpf程序已经保证了：用户的 bucket_meta、grants、usage 完整存在，ebpf才能放行用户沙箱的网络流量。
   * 如果出现用户的 bucket_meta、grants、usage 不完整存在情况，**只认为原因是上一次 SyncEpochToEbpf 插入时程序中断**
   * 恢复动作为：删除ebpf中残留的数据，以数据库为事实标准，重新插入用户的 bucket_meta、grants、usage
   * 这种情况，在恢复动作前不需要检查残留的usage.tx/rx，grants.Bytes的值是多少，**不考虑ebpf已经放行一段时间后，有其他未知流程意外删除了bucket_meta、grants、usage之一，不需要为此做额外的数据校验**
3. 规则三：
   * 即跨天、新用户、ecs重启、"用户的bucket_meta、grants、usage 不完整存在"，四种情况，做完各自的简单预处理，会统一走到一处代码，来保证代码简洁，详见伪代码
   * **不考虑ebpf已经放行一段时间后，有其他未知流程意外删除了bucket_meta、grants、usage全部的情况，不需要为此做额外的数据校验**
4. 规则四：
   * 本次重构去掉所有判断时钟跳变的逻辑，未来需要再加


SyncEpochToEbpf 逻辑：
1. 二级锁，通过全局锁得到用户锁，SyncEpochToEbpf 全称上用户锁
2. 获取当前日期 day **注意: 后面用到day这个变量，都是在函数开头获取的这个**
3. 根据 userID 查询 user_bucket 得到 epochID，再以 epochID 查询 bucket_meta、grants、usage。user_bucket 缺失时，先从 DB 查询该用户最新的 active epoch（优先日期较新、ID 较大的记录），再检查三张 map；不存在 active epoch 时视为三项全无。
   * 三张 map 仍以 epochID 为 key；bucketMeta 本身不新增 epochID，后面的 epochID 指查询时使用的 key。
   * 第 4、5、6 步互斥，每次只进入其中一条，然后跳到第 7 步。部分残留清理后不再执行第 5 步，保留原 activeEpoch。
4. 如果 bucketMeta、grants、usage 不完整存在，删除残留数据 (完成了规则一的前半部分)，
   * 并设置 dayBucketMeta, dayGrants, dayUsage 为 nil
5. 如果 bucketMeta、grants、usage 都为 nil，说明是新用户或ecs重启ebpf丢失：
   * 执行 update epoch set status = close where status = active，其他where条件是day、userID
     * 对于esc重启ebpf丢失，select sum(granted-rx-tx) form epoch where status=close，这部分下称unconfirmed。unconfirmed流量不会再授权给用户，用户可能会吃亏，这里你先不用关注，我以后说怎么优化。
     * 对于新用户 update 语句的影响行数是0，因此不需要两种场景，都可以执行update语句
   * 设置 dayBucketMeta, dayGrants, dayUsage 为 nil
6. 如果 bucketMeta、grants、usage 完整存在
   * 设置 dayBucketMeta, dayGrants, dayUsage := bucketMeta, grants, usage
7. 根据day，user，get or create，得到 activeEpoch
8. 如果 dayBucketMeta 非 nil，且其对应的 epochID != activeEpoch.ID，说明跨天了，先记 dayBucketMeta的epochID为oldEpochID
   * 把 dayGrants, dayUsage 中的tx、rx、bytes以oldEpochID为ID同步到数据库，close数据库中oldEpochID这条数据
   * 按“快照 → 关闭旧 epoch → 切新桶”的顺序执行，接受快照之后到切换之前的少量流量未计入旧 epoch，不增加停流量步骤。
   * 设置 dayBucketMeta, dayGrants, dayUsage 为 nil
9. 如果 dayBucketMeta, dayGrants, dayUsage 全为nil，即跨天、新用户、ecs重启、数据不完整前面做好了各自的前置操作，统一都到了这里，(p.s.规则一后半部分也在这里闭环了)：
   * 根据 activeEpoch 向 ebpf 插入用户的 grants、usage；usage.RX/TX 从 DB 初始化，bucket_meta 留到第 11 步发布
   * dayGrants, dayUsage = 新插入的值, 新插入的值
10. 根据 dayGrants, dayUsage 判断是否在此次同步追加授权一次
   * 无论是否追加授权，都将读取到的 usage.RX/TX 同步到 DB；授权先提交 DB，再更新 ebpf 中的 grants.Bytes。
   * 普通追加授权不回写 usage，避免覆盖 eBPF 并发累计的流量；只有第 9 步重建时才初始化 usage。
   * DB 已提交但 grants 尚未更新时，补发已提交的累计授权，不重复扣减当天余额。
11. 在 ebpf maps 插入/跟新 bucket_meta
12. 在 ebpf maps 插入/更新 user_bucket
13. 函数结尾异步清理该用户 day <= 本次 day 的历史遗留 active；持有与同步相同的用户锁，重新读取当前 user_bucket，排除清理时正在使用的 epoch，避免误关后续调用新建的 epoch。清理旧 epoch 的 map 条目，失败只记录日志，后续调用重试，不要求本次成功。同一 epoch 清理成功后不重复扫描历史；异步清理尚未完成时不重复排队。


重构后：
1. 清理manager db中epoch表的version字段，以及c语言和go侧的grants.version字段，ABI版本升级为3。因为根据上述重构，version没用了
2. 清理manager db中的boot_id字段，因为根据上述重构，boot_id没用了

本次不做，以后的优化：
1. min(unconfirmed, threshold)，是当日被冻结无法授权给用户的上限，避免服务频繁重启，用户吃亏太多

落地约定：
* `SyncEpochToEbpf(ctx, userID)` 保留上述完整编排和步骤顺序。每步可以调用具体读写辅助函数，不把编排层拆成多个函数。
* 保留低水位、批次授权、北京时间日界线和现有日截止宽限；删除时钟跳变检测及日期回退拦截。
* 删除 epoch/grants 的 version、基于 version 的 ring 通知字段，以及 boot_id/adopt 依赖。ring 事件携带用户 ID，按用户调用统一同步入口。
* 启动恢复、Docker 发现与 ensure、network-usage、ring 和定时任务都复用统一入口。network-usage 只同步目标用户；未知 UUID 先建立 users 记录，历史日查询仍同步当前日后读取指定日账本。
* 账本只保留 active/closed；closed 的 granted-rx-tx 计入 uncertain_bytes，不退回当天可授权余额。维护脚本保留账本，重建后由统一入口识别 maps 丢失。
* 本次仅修改新建表结构，不编写老数据迁移；仍保留 pinned metadata 的 ABI、DB UUID、父 cgroup、配置哈希及 Ready 校验。
