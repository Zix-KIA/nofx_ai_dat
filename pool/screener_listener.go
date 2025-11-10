package pool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// PairStatistics 对应 pair_statistics2 表结构
type PairStatistics struct {
	Pair                 string    `json:"pair"`
	LastSignalDatetime   time.Time `json:"last_signal_datetime"`
	SCCount              int       `json:"sc_count"`
	TSBinanceCount       int       `json:"ts_binance_count"`
	TSBybitCount         int       `json:"ts_bybit_count"`
	HASBinanceCount      int       `json:"has_binance_count"`
	HASBybitCount        int       `json:"has_bybit_count"`
	TotalSignals         int       `json:"total_signals"`
	SCLastDatetime       time.Time `json:"sc_last_datetime"`
	SCLastColor          string    `json:"sc_last_color"`
	BTCCorr30            float64   `json:"btc_corr_30"`
	BTCCorr60            float64   `json:"btc_corr_60"`
	BTCCorr120           float64   `json:"btc_corr_120"`
	BTCCorr180           float64   `json:"btc_corr_180"`
	BTCCorrAvg           float64   `json:"btc_corr_avg"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// SignalMetadata SC bot metadata
type SignalMetadata struct {
	Color        string  `json:"color"`
	SCMessageL1  string  `json:"sc_message_l1"`
	SCMessageL2  string  `json:"sc_message_l2"`
	SCMessageL3  string  `json:"sc_message_l3"`
	SCMessageL4  string  `json:"sc_message_l4"`
	SCMessageL5  string  `json:"sc_message_l5"`
	SCMessageL6  string  `json:"sc_message_l6"`
	BTCCorr30    float64 `json:"btc_corr_30"`
	BTCCorr60    float64 `json:"btc_corr_60"`
	BTCCorr120   float64 `json:"btc_corr_120"`
	BTCCorr180   float64 `json:"btc_corr_180"`
	BTCCorrAvg   float64 `json:"btc_corr_avg"`
}

// Signal 对应 signals 表结构
type Signal struct {
	ID        int            `json:"id"`
	Pair      string         `json:"pair"`
	BotType   string         `json:"bot_type"` // SC / TS / HAS
	Exchange  string         `json:"exchange"` // Binance / Bybit / NULL for SC
	Metadata  SignalMetadata `json:"metadata"`
	CreatedAt time.Time      `json:"created_at"`
}

// ScreenerSignalEvent 新信号事件
type ScreenerSignalEvent struct {
	Pair       string          // 交易对
	Statistics *PairStatistics // 完整统计数据
	Signal     *Signal         // 原始信号数据（可选）
	Timestamp  time.Time       // 事件时间
}

// SupabaseAPIResponse Supabase REST API响应
type SupabaseAPIResponse []PairStatistics

// ScreenerListener 实时监听screener信号
type ScreenerListener struct {
	supabaseURL string
	supabaseKey string
	httpClient  *http.Client

	// 缓存当前pair_statistics2数据
	statistics      map[string]*PairStatistics
	statisticsMutex sync.RWMutex

	// 信号事件通道
	signalChan chan *ScreenerSignalEvent

	// 控制goroutine
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// 轮询间隔
	pollInterval time.Duration

	running bool
	mu      sync.Mutex
}

// NewScreenerListener 创建screener监听器
func NewScreenerListener(supabaseURL, supabaseKey string) (*ScreenerListener, error) {
	if supabaseURL == "" {
		return nil, fmt.Errorf("supabase_url不能为空")
	}

	ctx, cancel := context.WithCancel(context.Background())

	sl := &ScreenerListener{
		supabaseURL:  supabaseURL,
		supabaseKey:  supabaseKey,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		statistics:   make(map[string]*PairStatistics),
		signalChan:   make(chan *ScreenerSignalEvent, 100),
		pollInterval: 5 * time.Second, // 每5秒轮询一次
		ctx:          ctx,
		cancel:       cancel,
	}

	return sl, nil
}

// Start 启动监听器
func (sl *ScreenerListener) Start() error {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	if sl.running {
		return fmt.Errorf("screener listener已经在运行")
	}

	// 加载初始数据
	if err := sl.loadInitialData(); err != nil {
		return fmt.Errorf("加载初始数据失败: %w", err)
	}

	// 启动轮询goroutine
	sl.wg.Add(1)
	go sl.pollForChanges()

	sl.running = true
	log.Printf("🎯 Screener Listener已启动 (轮询间隔: %v)", sl.pollInterval)

	return nil
}

// Stop 停止监听器
func (sl *ScreenerListener) Stop() {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	if !sl.running {
		return
	}

	log.Printf("⏹️  正在停止Screener Listener...")

	sl.cancel()
	sl.wg.Wait()

	close(sl.signalChan)

	sl.running = false
	log.Printf("✓ Screener Listener已停止")
}

// loadInitialData 加载初始pair_statistics2数据
func (sl *ScreenerListener) loadInitialData() error {
	// 从Supabase REST API获取数据
	url := fmt.Sprintf("%s/rest/v1/pair_statistics2?select=*&order=total_signals.desc", sl.supabaseURL)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("创建HTTP请求失败: %w", err)
	}

	req.Header.Set("apikey", sl.supabaseKey)
	req.Header.Set("Authorization", "Bearer "+sl.supabaseKey)
	req.Header.Set("Accept", "application/json")

	resp, err := sl.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP状态错误 %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取响应失败: %w", err)
	}

	var stats SupabaseAPIResponse
	if err := json.Unmarshal(body, &stats); err != nil {
		return fmt.Errorf("JSON解析失败: %w", err)
	}

	sl.statisticsMutex.Lock()
	defer sl.statisticsMutex.Unlock()

	for i := range stats {
		sl.statistics[stats[i].Pair] = &stats[i]
	}

	log.Printf("✓ 加载了%d个交易对的历史数据", len(stats))
	return nil
}

// pollForChanges 轮询检查新信号
func (sl *ScreenerListener) pollForChanges() {
	defer sl.wg.Done()

	log.Printf("👂 开始轮询pair_statistics2表的更新...")

	ticker := time.NewTicker(sl.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sl.ctx.Done():
			return
		case <-ticker.C:
			sl.checkForNewSignals()
		}
	}
}

// checkForNewSignals 检查新信号（HTTP轮询）
func (sl *ScreenerListener) checkForNewSignals() {
	// 获取最近1分钟内更新的交易对
	// 注意：Supabase不支持NOW()，需要计算时间
	oneMinuteAgo := time.Now().Add(-1 * time.Minute).Format(time.RFC3339)
	url := fmt.Sprintf("%s/rest/v1/pair_statistics2?select=*&updated_at=gte.%s&order=updated_at.desc",
		sl.supabaseURL, oneMinuteAgo)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Printf("⚠️  创建HTTP请求失败: %v", err)
		return
	}

	req.Header.Set("apikey", sl.supabaseKey)
	req.Header.Set("Authorization", "Bearer "+sl.supabaseKey)
	req.Header.Set("Accept", "application/json")

	resp, err := sl.httpClient.Do(req)
	if err != nil {
		log.Printf("⚠️  HTTP请求失败: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	var stats SupabaseAPIResponse
	if err := json.Unmarshal(body, &stats); err != nil {
		log.Printf("⚠️  JSON解析失败: %v", err)
		return
	}

	for i := range stats {
		stat := stats[i]

		// 🔥 Binance过滤：只处理有Binance信号的交易对
		hasBinanceSignals := stat.TSBinanceCount > 0 || stat.HASBinanceCount > 0
		if !hasBinanceSignals {
			// 跳过没有Binance信号的交易对（只有Bybit信号）
			continue
		}

		// 检查是否是新信号
		sl.statisticsMutex.RLock()
		oldStat, exists := sl.statistics[stat.Pair]
		sl.statisticsMutex.RUnlock()

		if !exists || oldStat.TotalSignals < stat.TotalSignals {
			// 新信号！
			sl.statisticsMutex.Lock()
			sl.statistics[stat.Pair] = &stat
			sl.statisticsMutex.Unlock()

			// 发送事件
			event := &ScreenerSignalEvent{
				Pair:       stat.Pair,
				Statistics: &stat,
				Timestamp:  time.Now(),
			}

			select {
			case sl.signalChan <- event:
				log.Printf("🔔 新信号 [Binance]: %s (总计:%d, SC:%d, TS_Binance:%d, HAS_Binance:%d, 相关性:%.2f)",
					stat.Pair, stat.TotalSignals, stat.SCCount, stat.TSBinanceCount, stat.HASBinanceCount, stat.BTCCorrAvg)
			default:
				log.Printf("⚠️  信号通道已满，丢弃信号: %s", stat.Pair)
			}
		}
	}
}

// GetSignalChannel 获取信号事件通道
func (sl *ScreenerListener) GetSignalChannel() <-chan *ScreenerSignalEvent {
	return sl.signalChan
}

// GetStatistics 获取指定交易对的统计数据
func (sl *ScreenerListener) GetStatistics(pair string) (*PairStatistics, bool) {
	sl.statisticsMutex.RLock()
	defer sl.statisticsMutex.RUnlock()

	stat, exists := sl.statistics[pair]
	return stat, exists
}

// GetTopSignalPairs 获取信号最多的N个交易对
func (sl *ScreenerListener) GetTopSignalPairs(limit int) []string {
	sl.statisticsMutex.RLock()
	defer sl.statisticsMutex.RUnlock()

	// 将map转换为slice并排序
	type pairSignal struct {
		pair   string
		signals int
	}

	pairs := make([]pairSignal, 0, len(sl.statistics))
	for pair, stat := range sl.statistics {
		pairs = append(pairs, pairSignal{pair: pair, signals: stat.TotalSignals})
	}

	// 冒泡排序（降序）
	for i := 0; i < len(pairs); i++ {
		for j := i + 1; j < len(pairs); j++ {
			if pairs[i].signals < pairs[j].signals {
				pairs[i], pairs[j] = pairs[j], pairs[i]
			}
		}
	}

	// 取前N个
	maxCount := limit
	if len(pairs) < maxCount {
		maxCount = len(pairs)
	}

	result := make([]string, maxCount)
	for i := 0; i < maxCount; i++ {
		result[i] = pairs[i].pair
	}

	return result
}

// GetAllStatistics 获取所有统计数据
func (sl *ScreenerListener) GetAllStatistics() map[string]*PairStatistics {
	sl.statisticsMutex.RLock()
	defer sl.statisticsMutex.RUnlock()

	// 返回副本
	result := make(map[string]*PairStatistics, len(sl.statistics))
	for k, v := range sl.statistics {
		statCopy := *v
		result[k] = &statCopy
	}

	return result
}

// 全局screener listener实例
var globalScreenerListener *ScreenerListener
var globalScreenerMutex sync.Mutex

// InitGlobalScreenerListener 初始化全局screener listener
func InitGlobalScreenerListener(supabaseURL, supabaseKey string) error {
	globalScreenerMutex.Lock()
	defer globalScreenerMutex.Unlock()

	if globalScreenerListener != nil {
		globalScreenerListener.Stop()
	}

	listener, err := NewScreenerListener(supabaseURL, supabaseKey)
	if err != nil {
		return err
	}

	if err := listener.Start(); err != nil {
		return err
	}

	globalScreenerListener = listener
	return nil
}

// GetGlobalScreenerListener 获取全局screener listener
func GetGlobalScreenerListener() *ScreenerListener {
	globalScreenerMutex.Lock()
	defer globalScreenerMutex.Unlock()
	return globalScreenerListener
}

// StopGlobalScreenerListener 停止全局screener listener
func StopGlobalScreenerListener() {
	globalScreenerMutex.Lock()
	defer globalScreenerMutex.Unlock()

	if globalScreenerListener != nil {
		globalScreenerListener.Stop()
		globalScreenerListener = nil
	}
}
