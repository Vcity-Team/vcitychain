package dpos

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// DPoSMetricsCollector 指标收集器
type DPoSMetricsCollector struct {
	// Prometheus指标
	totalVotes         prometheus.Counter
	totalDelegates     prometheus.Gauge
	activeVoters       prometheus.Gauge
	blockRewards       prometheus.Counter
	blockTime          prometheus.Histogram
	voteProcessingTime prometheus.Histogram
	delegateUpdateTime prometheus.Histogram

	// 自定义指标
	votingPower     map[types.Address]*big.Int
	delegateStakes  map[types.Address]*big.Int
	lastBlockNumber uint64
	lastBlockTime   time.Time

	// 健康检查
	healthStatus    string
	lastHealthCheck time.Time

	lock   sync.RWMutex
	server *http.Server
}

// NewDPoSMetricsCollector 创建指标收集器
func NewDPoSMetricsCollector(port int) *DPoSMetricsCollector {
	collector := &DPoSMetricsCollector{
		votingPower:    make(map[types.Address]*big.Int),
		delegateStakes: make(map[types.Address]*big.Int),
		healthStatus:   "healthy",
	}

	// 初始化Prometheus指标
	collector.initPrometheusMetrics()

	// 启动HTTP服务器
	collector.startHTTPServer(port)

	return collector
}

// 初始化Prometheus指标
func (c *DPoSMetricsCollector) initPrometheusMetrics() {
	c.totalVotes = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "dpos_total_votes",
		Help: "Total number of votes processed",
	})

	c.totalDelegates = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "dpos_total_delegates",
		Help: "Total number of delegates",
	})

	c.activeVoters = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "dpos_active_voters",
		Help: "Number of active voters",
	})

	c.blockRewards = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "dpos_block_rewards",
		Help: "Total block rewards distributed",
	})

	c.blockTime = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "dpos_block_time_seconds",
		Help:    "Time taken to produce blocks",
		Buckets: prometheus.DefBuckets,
	})

	c.voteProcessingTime = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "dpos_vote_processing_time_seconds",
		Help:    "Time taken to process votes",
		Buckets: prometheus.DefBuckets,
	})

	c.delegateUpdateTime = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "dpos_delegate_update_time_seconds",
		Help:    "Time taken to update delegates",
		Buckets: prometheus.DefBuckets,
	})

	// 注册指标
	prometheus.MustRegister(
		c.totalVotes,
		c.totalDelegates,
		c.activeVoters,
		c.blockRewards,
		c.blockTime,
		c.voteProcessingTime,
		c.delegateUpdateTime,
	)
}

// 启动HTTP服务器
func (c *DPoSMetricsCollector) startHTTPServer(port int) {
	mux := http.NewServeMux()

	// Prometheus指标端点
	mux.Handle("/metrics", promhttp.Handler())

	// 健康检查端点
	mux.HandleFunc("/health", c.healthCheckHandler)

	// 自定义指标端点
	mux.HandleFunc("/dpos/stats", c.statsHandler)

	c.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	go func() {
		if err := c.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("Metrics server error: %v\n", err)
		}
	}()
}

// 健康检查处理器
func (c *DPoSMetricsCollector) healthCheckHandler(w http.ResponseWriter, r *http.Request) {
	c.lock.RLock()
	defer c.lock.RUnlock()

	// 检查最后健康检查时间
	if time.Since(c.lastHealthCheck) > 5*time.Minute {
		c.healthStatus = "unhealthy"
	}

	w.Header().Set("Content-Type", "application/json")
	if c.healthStatus == "healthy" {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"%s","timestamp":"%s"}`, c.healthStatus, time.Now().Format(time.RFC3339))
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"status":"%s","timestamp":"%s"}`, c.healthStatus, time.Now().Format(time.RFC3339))
	}
}

// 统计信息处理器
func (c *DPoSMetricsCollector) statsHandler(w http.ResponseWriter, r *http.Request) {
	c.lock.RLock()
	defer c.lock.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{
		"total_votes": %d,
		"total_delegates": %d,
		"active_voters": %d,
		"last_block_number": %d,
		"last_block_time": "%s",
		"health_status": "%s"
	}`,
		c.totalVotes,
		c.totalDelegates,
		c.activeVoters,
		c.lastBlockNumber,
		c.lastBlockTime.Format(time.RFC3339),
		c.healthStatus)
}

// 记录投票
func (c *DPoSMetricsCollector) RecordVote(voter, delegate types.Address, amount *big.Int, processingTime time.Duration) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.totalVotes.Inc()
	c.voteProcessingTime.Observe(processingTime.Seconds())

	// 更新投票权重
	if c.votingPower[voter] == nil {
		c.votingPower[voter] = big.NewInt(0)
	}
	c.votingPower[voter].Add(c.votingPower[voter], amount)

	c.lastHealthCheck = time.Now()
}

// 记录受托人更新
func (c *DPoSMetricsCollector) RecordDelegateUpdate(delegates []types.Address, processingTime time.Duration) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.totalDelegates.Set(float64(len(delegates)))
	c.delegateUpdateTime.Observe(processingTime.Seconds())

	c.lastHealthCheck = time.Now()
}

// 记录区块生产
func (c *DPoSMetricsCollector) RecordBlockProduction(blockNumber uint64, blockTime time.Duration, reward *big.Int) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.lastBlockNumber = blockNumber
	c.lastBlockTime = time.Now()
	c.blockTime.Observe(blockTime.Seconds())

	if isPositive(reward) {
		c.blockRewards.Add(float64(reward.Uint64()))
	}

	c.lastHealthCheck = time.Now()
}

// 记录活跃投票者
func (c *DPoSMetricsCollector) RecordActiveVoters(count uint64) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.activeVoters.Set(float64(count))
	c.lastHealthCheck = time.Now()
}

// 获取投票权重统计
func (c *DPoSMetricsCollector) GetVotingPowerStats() map[types.Address]*big.Int {
	c.lock.RLock()
	defer c.lock.RUnlock()

	result := make(map[types.Address]*big.Int)
	for addr, power := range c.votingPower {
		result[addr] = new(big.Int).Set(power)
	}
	return result
}

// 关闭指标收集器
func (c *DPoSMetricsCollector) Close() error {
	if c.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return c.server.Shutdown(ctx)
	}
	return nil
}
