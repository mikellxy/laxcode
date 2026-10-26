# 现在 epoch 这块实现的比较乱，我感觉可以重构一下，锚定epoch user_id(epoch表的user_id，不是用户uuid) 去做一套抽象，让代码更整洁
先约定边界case的原则(你看看有没有问题)：
1. laxcode重启,ecs没重启，ebpf挂载还在，manager启动时做一次，ebpf usage.tx/rx写入数据库epoch表
2. ecs重启，先标记所有active epoch为close，新建一条epoch(status=active,granted=0),约定以下计数：
   unconfirmed = [sum(grant)-sum(tx)-sum(rx) from epoch where status = close]
   already_granted = [sum(grant) from all epoch]
   新建的ebpf map：usage.tx=0, usage.rx=0，grants.bytes=0，其实代码逻辑中不需要计算unconfirmed，只要保证already_granted<=day_limit，就代表了unconfirmed没有再次授权给用户
   后面跟所有情况一样都是走到 tx+rx+chunk > bytes 触发追加授权，这样到这一步就不用判断是不是ecs重启了，代码可以统一
3. usage.tx < epoch.tx，usage.rx < epoch.rx，或不满足(usage.rx + usage.tx <= grants.bytes <= epoch.granted)，即破坏了不变量，无法确定是报错遭到攻击回退还是恶性bug，报错，人工介入排查
4. 如果 ebpf中 usage,grants,bucket_meta存在至少一个，但是不都存在，
    * 如果db中epoch.granted/tx/rx=0，ebpf中grant,tx,rx=0，一律认为是之前manager启动过程中崩溃了，用epoch重建user的ebpf数据
    * 如果db中epoch.granted>0，破坏了运行中不应出现ebpf不完整的不变量，报错，人工介入排查
5. 基于1-4，看起来ebpf maps和epoch数据库的version字段没有价值了。修改ABI版本为2，c和go两侧都去掉version，上线前我删除ebpf挂载，dorp数据库version列，你不需要考虑兼容性。不使用version后，增加以下通知优化：
   完成本次额度检查、扣费或丢包后：
   余额 <= 低水位
   且：
   grants.bytes != 上次通知时的授权金额
   或距上次通知 >= 1秒
   则更新通知记录，并尝试发送补额度事件。
6. ebpf bucket_meta.dealine 过期了：
   按当前上海日期取得 active epoch
   → 初始化该 epoch 的 maps（已有则校验、复用）
   → 同步及追加授权
   → 最后切换 user_bucket
7. bucket_meta.dealine 依然使用 boot_ns，去掉对始终跳变的检查

# 伪代码
**要求**：
* 伪代码用于表达流程和规则；落地时以现有 eBPF 结构体、数据库字段名为准，不因伪代码命名而重命名。
* SyncEpochToEbpf 保留完整的步骤编排，各步骤可调用辅助函数或方法，但不把主流程纵向拆成多个阶段函数。
* 辅助函数不必照搬伪代码，但命名必须明确表达具体功能，避免含糊。
* 伪代码使用辅助函数的地方，如果功能太小不知道定义函数，可以inline实现

SyncEpochToEbpf: 检查和同步 ebpf 和 epoch db，入参为数据库epoch的数字userID，不用uuid
* manager启动时，for epoch in all active epoch list {call SyncEpochToEbpf}
* 定时追加额度任务，for epoch in all active epoch list {call SyncEpochToEbpf}
* 以下流程 用user_id单次调用 SyncEpochToEbpf，不再额外批量为所有活跃沙箱调用 SyncEpochToEbpf
    * 用户请求chat/resume/eval接口，沙箱 Ready 后
    * 查询用户沙箱网络额度情况、是否耗尽
    * ebpf ring 异步通知请求追加额度
```go
func (m *manager) SyncEpochToEbpf(ctx context.Context, userID int) error {
    m.lock()
	user := m.getUser(userID)
	m.unlock
	// 所有需要 SyncEpochToEbpf 的地方，都用户粒度锁，简化锁模型，让避免数据竞争的方法比较清晰
	user.lock()
	defer user.unlock()
	
	// 获取当前日期
	day := ...
	
	// 查询该用户在 ebpf 中 map
	bucketMeta := ...
	grants := ...
	usage := ...
	
	// 查询当日的active epoch
	activeEpoch := getActiveEpoch(user_id, day...
	
	// 
	if (bucketMeta,grants,usage不全为nil,但是有至少一个为nil) {
		if ((activeEpoch.granted/rx/tx == 0 || activeEpoch == nil) && (usage.tx/rx == 0 || usage == nil) && (grants.bytes == 0 || grants == nil)) {
			// 代表上次分配中间崩溃，for约定4
			do 从epbf maps中delete用户的grants/usage/bucket_meta
		} else {
			// 破坏不变量约束，报错，人工介入
			return err
		}
	}

    // for 约定2、约定4，走到这里重新分配ebpf
    // 新用户也走这里，但是其中的sql语句查询不到数据，不产生实际影响
    if (bucketMeta == nil && grants==nil && usage==nil) {
        update epoch set status = close where day=xxx and user_id=xxx
		// 再次查询 或 创建当日的 active epoch
        activeEpoch = getOrCreateActiveEpoch(user_id, day...
		do 在ebpf maps中创建用户的grants, usage，根据 activeEpoch
    }
	
	// 破坏不变量约束，报错，人工介入
	if usage.tx < activeEpoch.tx || usage.rx < activeEpoch.rx || (usage.rx + usage.tx <= grants.bytes <= activeEpoch.granted)  {
		return err
    }
	
	// for 约定1
	if usage.tx > activeEpoch.tx || usage.rx > activeEpoch.rx {
        activeEpoch = updateEpochRxTx(...) // update epoch set tx=xxx and rx=xxx where ...
	}
	
	// 对得上直接视为 sync 成功
	if (usage.tx+usage.tx+chunk > grants.Bytes) {
		delta := 计算同步追加授权量
		activeEpoch = UpdateEpochGranted(...) // update epoch set granted = granted+delta where ...
	}
	
	do update usage.Bytes using activeEpoch.Granted
	
	do 在ebpf maps 创建/更新用户的 bucket_meta, 根据 activeEpoch.deadline
	
	do 在ebpf maps 创建/更新用户的 user_bucket，根据 activeEpoch.ID
}
```

当前ebpf异步通知追加权限的的代码，基于上述约定和SyncEpochToEbpf重构，进行必要调整