package _const

const ChunkSize = 4 * (1 << 20) // 每块为 4M

// 调度中心（dispatch center）相关调参常量。集中放此处便于统一调整。
const (
	// SchedEWMAAlpha 上行速率 EWMA 平滑系数（0~1，越大越跟随近期实测）。
	SchedEWMAAlpha = 0.3
	// SchedColdStartMbps 新节点冷启动时的乐观上行估计（Mbps，假设千兆）。
	SchedColdStartMbps = 1000.0
	// SchedCrossZoneFactor 跨机房局部性惩罚系数 β（跨机房等价走 hub，几乎永不优先）。
	SchedCrossZoneFactor = 0.15
	// SchedFailHalfLifeSec 连续失败计数的时间半衰期（秒），保证好节点能从瞬时抖动中恢复。
	SchedFailHalfLifeSec = 30.0
	// SchedLeaseMinTTLSec 调度租约最小存活时间（秒）；死客户端不会永久占用 connNum。
	SchedLeaseMinTTLSec = 5
	// SchedMaxSourcesPerWant 单次 WantChunk 返回的候选源数量上限（供客户端本地故障转移）。
	SchedMaxSourcesPerWant = 3
	// SchedDefaultZonePrefixBits 无显式 zone 标签时，按 IP 前缀推断机房所用的位数（/24）。
	SchedDefaultZonePrefixBits = 24

	// UploadConcurrencyDefault 每个源默认最大并发上传数（uploadSem 容量）。
	UploadConcurrencyDefault = 6
	// UploadConcurrencyHDD HDD 特征源的并发上传上限（避免随机读寻道抖动）。
	UploadConcurrencyHDD = 2

	// DiskIOConcurrency 单文件并发磁盘 IO（读/写）许可数（FileChunk.ioPermit 容量）。
	// 取代原先容量=1 的信号量：容量 1 会把整机分块读写串行化，使"多线程并发下载"退化为单路。
	// 上层另有 AcquireUpload 对上传侧自适应限流以保护 HDD，故此处可放宽到适度并发。
	DiskIOConcurrency = 6
)
