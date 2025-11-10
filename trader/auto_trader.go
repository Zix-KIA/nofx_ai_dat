package trader

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"nofx/decision"
	"nofx/logger"
	"nofx/market"
	"nofx/mcp"
	"nofx/pool"
	"sort"
	"strings"
	"sync"
	"time"
)

// AutoTraderConfig 自动交易配置（简化版 - AI全权决策）
type AutoTraderConfig struct {
	// Trader标识
	ID      string // Trader唯一标识（用于日志目录等）
	Name    string // Trader显示名称
	AIModel string // AI模型: "qwen" 或 "deepseek"

	// 交易平台选择
	Exchange string // "binance", "hyperliquid" 或 "aster"

	// 币安API配置
	BinanceAPIKey    string
	BinanceSecretKey string

	// Hyperliquid配置
	HyperliquidPrivateKey string
	HyperliquidWalletAddr string
	HyperliquidTestnet    bool

	// Aster配置
	AsterUser       string // Aster主钱包地址
	AsterSigner     string // Aster API钱包地址
	AsterPrivateKey string // Aster API钱包私钥

	CoinPoolAPIURL string

	// AI配置
	UseQwen     bool
	DeepSeekKey string
	QwenKey     string

	// 自定义AI API配置
	CustomAPIURL    string
	CustomAPIKey    string
	CustomModelName string

	// 扫描配置
	ScanInterval time.Duration // 扫描间隔（建议3分钟）

	// 账户配置
	InitialBalance float64 // 初始金额（用于计算盈亏，需手动设置）

	// 杠杆配置
	BTCETHLeverage  int // BTC和ETH的杠杆倍数
	AltcoinLeverage int // 山寨币的杠杆倍数

	// 风险控制（仅作为提示，AI可自主决定）
	MaxDailyLoss    float64       // 最大日亏损百分比（提示）
	MaxDrawdown     float64       // 最大回撤百分比（提示）
	StopTradingTime time.Duration // 触发风控后暂停时长

	// 仓位模式
	IsCrossMargin bool // true=全仓模式, false=逐仓模式

	// 币种配置
	DefaultCoins []string // 默认币种列表（从数据库获取）
	TradingCoins []string // 实际交易币种列表

	// 系统提示词模板
	SystemPromptTemplate string // 系统提示词模板名称（如 "default", "aggressive"）

	// Screener信号配置
	UseScreenerSignals bool // 是否使用screener实时信号

	// Paper Trading配置
	PaperTradingMode bool // Paper Trading模式（虚拟交易，用于学习）
	MaxPositions     int  // 最大持仓数量
}

// WatchlistEntry 监控列表条目
type WatchlistEntry struct {
	Symbol         string    // 交易对符号
	AddedAt        time.Time // 添加时间
	Reason         string    // 添加原因（AI推理）
	LastAnalyzedAt time.Time // 最后分析时间
	Priority       int       // 优先级 (1-10, 10最高)
	Sources        []string  // 来源 (screener, ai500, oi_top等)

	// Screener相关数据（如果来自screener）
	ScreenerTotalSignals int     // 总信号数
	ScreenerSCCount      int     // SC信号数
	ScreenerBTCCorr      float64 // BTC相关性
	ScreenerSCColor      string  // SC颜色
}

// AutoTrader 自动交易器
type AutoTrader struct {
	id                    string // Trader唯一标识
	name                  string // Trader显示名称
	aiModel               string // AI模型名称
	exchange              string // 交易平台名称
	config                AutoTraderConfig
	trader                Trader // 使用Trader接口（支持多平台）
	mcpClient             *mcp.Client
	decisionLogger        *logger.DecisionLogger // 决策日志记录器
	initialBalance        float64
	dailyPnL              float64
	customPrompt          string   // 自定义交易策略prompt
	overrideBasePrompt    bool     // 是否覆盖基础prompt
	systemPromptTemplate  string   // 系统提示词模板名称
	defaultCoins          []string // 默认币种列表（从数据库获取）
	tradingCoins          []string // 实际交易币种列表
	lastResetTime         time.Time
	stopUntil             time.Time
	isRunning             bool
	startTime             time.Time          // 系统启动时间
	callCount             int                // AI调用次数
	positionFirstSeenTime map[string]int64   // 持仓首次出现时间 (symbol_side -> timestamp毫秒)
	stopMonitorCh         chan struct{}      // 用于停止监控goroutine
	monitorWg             sync.WaitGroup     // 用于等待监控goroutine结束
	peakPnLCache          map[string]float64 // 最高收益缓存 (symbol -> 峰值盈亏百分比)
	peakPnLCacheMutex     sync.RWMutex       // 缓存读写锁
	lastBalanceSyncTime   time.Time          // 上次余额同步时间
	database              interface{}        // 数据库引用（用于自动更新余额）
	userID                string             // 用户ID

	// Screener信号相关
	useScreenerSignals    bool               // 是否使用screener信号
	screenerCandidates    map[string]bool    // 来自screener的候选币种
	screenerCandidatesMux sync.RWMutex       // 候选币种的读写锁

	// Watchlist相关
	watchlist             map[string]*WatchlistEntry // 监控列表 (symbol -> entry)
	watchlistMux          sync.RWMutex               // 监控列表读写锁
	maxWatchlistSize      int                        // 最大监控列表大小 (默认50)
	maxPositions          int                        // 最大持仓数量 (默认10)

	// Paper Trading相关
	paperTradingMode       bool                       // Paper Trading模式
	virtualPositionManager *VirtualPositionManager    // 虚拟持仓管理器
	knowledgeBase          *KnowledgeBase             // 知识库
}

// NewAutoTrader 创建自动交易器
func NewAutoTrader(config AutoTraderConfig, database interface{}, userID string) (*AutoTrader, error) {
	// 设置默认值
	if config.ID == "" {
		config.ID = "default_trader"
	}
	if config.Name == "" {
		config.Name = "Default Trader"
	}
	if config.AIModel == "" {
		if config.UseQwen {
			config.AIModel = "qwen"
		} else {
			config.AIModel = "deepseek"
		}
	}

	mcpClient := mcp.New()

	// 初始化AI
	if config.AIModel == "custom" {
		// 使用自定义API
		mcpClient.SetCustomAPI(config.CustomAPIURL, config.CustomAPIKey, config.CustomModelName)
		log.Printf("🤖 [%s] 使用自定义AI API: %s (模型: %s)", config.Name, config.CustomAPIURL, config.CustomModelName)
	} else if config.UseQwen || config.AIModel == "qwen" {
		// 使用Qwen (支持自定义URL和Model)
		mcpClient.SetQwenAPIKey(config.QwenKey, config.CustomAPIURL, config.CustomModelName)
		if config.CustomAPIURL != "" || config.CustomModelName != "" {
			log.Printf("🤖 [%s] 使用阿里云Qwen AI (自定义URL: %s, 模型: %s)", config.Name, config.CustomAPIURL, config.CustomModelName)
		} else {
			log.Printf("🤖 [%s] 使用阿里云Qwen AI", config.Name)
		}
	} else {
		// 默认使用DeepSeek (支持自定义URL和Model)
		mcpClient.SetDeepSeekAPIKey(config.DeepSeekKey, config.CustomAPIURL, config.CustomModelName)
		if config.CustomAPIURL != "" || config.CustomModelName != "" {
			log.Printf("🤖 [%s] 使用DeepSeek AI (自定义URL: %s, 模型: %s)", config.Name, config.CustomAPIURL, config.CustomModelName)
		} else {
			log.Printf("🤖 [%s] 使用DeepSeek AI", config.Name)
		}
	}

	// 初始化币种池API
	if config.CoinPoolAPIURL != "" {
		pool.SetCoinPoolAPI(config.CoinPoolAPIURL)
	}

	// 设置默认交易平台
	if config.Exchange == "" {
		config.Exchange = "binance"
	}

	// 根据配置创建对应的交易器
	var trader Trader
	var err error

	// 记录仓位模式（通用）
	marginModeStr := "全仓"
	if !config.IsCrossMargin {
		marginModeStr = "逐仓"
	}
	log.Printf("📊 [%s] 仓位模式: %s", config.Name, marginModeStr)

	switch config.Exchange {
	case "binance":
		log.Printf("🏦 [%s] 使用币安合约交易", config.Name)
		trader = NewFuturesTrader(config.BinanceAPIKey, config.BinanceSecretKey)
	case "hyperliquid":
		log.Printf("🏦 [%s] 使用Hyperliquid交易", config.Name)
		trader, err = NewHyperliquidTrader(config.HyperliquidPrivateKey, config.HyperliquidWalletAddr, config.HyperliquidTestnet)
		if err != nil {
			return nil, fmt.Errorf("初始化Hyperliquid交易器失败: %w", err)
		}
	case "aster":
		log.Printf("🏦 [%s] 使用Aster交易", config.Name)
		trader, err = NewAsterTrader(config.AsterUser, config.AsterSigner, config.AsterPrivateKey)
		if err != nil {
			return nil, fmt.Errorf("初始化Aster交易器失败: %w", err)
		}
	default:
		return nil, fmt.Errorf("不支持的交易平台: %s", config.Exchange)
	}

	// 验证初始金额配置
	if config.InitialBalance <= 0 {
		return nil, fmt.Errorf("初始金额必须大于0，请在配置中设置InitialBalance")
	}

	// 初始化决策日志记录器（使用trader ID创建独立目录）
	logDir := fmt.Sprintf("decision_logs/%s", config.ID)
	decisionLogger := logger.NewDecisionLogger(logDir)

	// 设置默认系统提示词模板
	systemPromptTemplate := config.SystemPromptTemplate
	if systemPromptTemplate == "" {
		// feature/partial-close-dynamic-tpsl 分支默认使用 adaptive（支持动态止盈止损）
		systemPromptTemplate = "adaptive"
	}

	// 设置默认限制
	maxWatchlistSize := 50 // 默认最大watchlist大小
	maxPositions := 10     // 默认最大持仓数量
	if config.MaxPositions > 0 {
		maxPositions = config.MaxPositions
	}

	// 初始化Paper Trading和Knowledge Base（如果启用）
	var virtualPosManager *VirtualPositionManager
	var kb *KnowledgeBase

	if config.PaperTradingMode {
		// 获取数据库连接
		var sqlDB *sql.DB
		if dbConn, ok := database.(interface{ GetDB() *sql.DB }); ok {
			sqlDB = dbConn.GetDB()
		}

		if sqlDB != nil {
			// 创建Knowledge Base
			var err error
			kb, err = NewKnowledgeBase(sqlDB, config.ID)
			if err != nil {
				log.Printf("⚠️  创建Knowledge Base失败: %v", err)
			} else {
				log.Printf("✓ [%s] Knowledge Base已初始化", config.Name)
			}

			// 创建Virtual Position Manager
			virtualPosManager = NewVirtualPositionManager(config.ID, config.InitialBalance, kb)
			log.Printf("✓ [%s] Paper Trading模式已启用 (初始余额: %.2f USDT)", config.Name, config.InitialBalance)
		} else {
			log.Printf("⚠️  无法获取数据库连接，Paper Trading功能不可用")
		}
	}

	return &AutoTrader{
		id:                    config.ID,
		name:                  config.Name,
		aiModel:               config.AIModel,
		exchange:              config.Exchange,
		config:                config,
		trader:                trader,
		mcpClient:             mcpClient,
		decisionLogger:        decisionLogger,
		initialBalance:        config.InitialBalance,
		systemPromptTemplate:  systemPromptTemplate,
		defaultCoins:          config.DefaultCoins,
		tradingCoins:          config.TradingCoins,
		lastResetTime:         time.Now(),
		startTime:             time.Now(),
		callCount:             0,
		isRunning:             false,
		positionFirstSeenTime: make(map[string]int64),
		stopMonitorCh:         make(chan struct{}),
		monitorWg:             sync.WaitGroup{},
		peakPnLCache:          make(map[string]float64),
		peakPnLCacheMutex:     sync.RWMutex{},
		lastBalanceSyncTime:   time.Now(), // 初始化为当前时间
		database:              database,
		userID:                userID,
		useScreenerSignals:    config.UseScreenerSignals,
		screenerCandidates:    make(map[string]bool),
		// Watchlist初始化
		watchlist:        make(map[string]*WatchlistEntry),
		watchlistMux:     sync.RWMutex{},
		maxWatchlistSize: maxWatchlistSize,
		maxPositions:     maxPositions,
		// Paper Trading初始化
		paperTradingMode:       config.PaperTradingMode,
		virtualPositionManager: virtualPosManager,
		knowledgeBase:          kb,
	}, nil
}

// Run 运行自动交易主循环
func (at *AutoTrader) Run() error {
	at.isRunning = true
	log.Println("🚀 AI驱动自动交易系统启动")
	log.Printf("💰 初始余额: %.2f USDT", at.initialBalance)
	log.Printf("⚙️  扫描间隔: %v", at.config.ScanInterval)
	log.Println("🤖 AI将全权决定杠杆、仓位大小、止损止盈等参数")

	// 启动screener信号监听（如果启用）
	if at.useScreenerSignals {
		at.monitorWg.Add(1)
		go at.watchScreenerSignals()
		log.Printf("👂 [%s] Screener信号监听已启动", at.name)
	}

	// 启动watchlist监控
	at.monitorWg.Add(1)
	go at.watchlistMonitor()

	// 启动Paper Trading监控（如果启用）
	if at.paperTradingMode && at.virtualPositionManager != nil {
		at.monitorWg.Add(1)
		go at.paperTradingMonitor()
		log.Printf("📝 [%s] Paper Trading监控已启动", at.name)
	}

	// 启动回撤监控
	at.startDrawdownMonitor()

	ticker := time.NewTicker(at.config.ScanInterval)
	defer ticker.Stop()

	// 首次立即执行
	if err := at.runCycle(); err != nil {
		log.Printf("❌ 执行失败: %v", err)
	}

	for at.isRunning {
		select {
		case <-ticker.C:
			if err := at.runCycle(); err != nil {
				log.Printf("❌ 执行失败: %v", err)
			}
		}
	}

	return nil
}

// Stop 停止自动交易
func (at *AutoTrader) Stop() {
	at.isRunning = false
	close(at.stopMonitorCh) // 通知监控goroutine停止
	at.monitorWg.Wait()     // 等待监控goroutine结束
	log.Println("⏹ 自动交易系统停止")
}

// watchScreenerSignals 监听screener实时信号
func (at *AutoTrader) watchScreenerSignals() {
	defer at.monitorWg.Done()

	listener := pool.GetGlobalScreenerListener()
	if listener == nil {
		log.Printf("⚠️  [%s] Screener listener未初始化，无法监听信号", at.name)
		return
	}

	signalChan := listener.GetSignalChannel()
	log.Printf("👂 [%s] 开始监听screener信号...", at.name)

	for {
		select {
		case <-at.stopMonitorCh:
			log.Printf("⏹  [%s] Screener信号监听已停止", at.name)
			return

		case event, ok := <-signalChan:
			if !ok {
				log.Printf("⚠️  [%s] Screener信号通道已关闭", at.name)
				return
			}

			// 记录新信号
			log.Printf("🔔 [%s] 收到screener信号: %s", at.name, event.Pair)
			log.Printf("   总信号: %d (SC:%d, TS_Binance:%d, TS_Bybit:%d, HAS_Binance:%d, HAS_Bybit:%d)",
				event.Statistics.TotalSignals,
				event.Statistics.SCCount,
				event.Statistics.TSBinanceCount,
				event.Statistics.TSBybitCount,
				event.Statistics.HASBinanceCount,
				event.Statistics.HASBybitCount,
			)
			log.Printf("   BTC相关性: %.2f (30m:%.2f, 60m:%.2f, 120m:%.2f, 180m:%.2f)",
				event.Statistics.BTCCorrAvg,
				event.Statistics.BTCCorr30,
				event.Statistics.BTCCorr60,
				event.Statistics.BTCCorr120,
				event.Statistics.BTCCorr180,
			)
			log.Printf("   SC颜色: %s, 最新信号时间: %s",
				event.Statistics.SCLastColor,
				event.Statistics.LastSignalDatetime.Format("15:04:05"),
			)

			// 过滤条件1: 最少信号数量
			if event.Statistics.TotalSignals < 10 {
				log.Printf("   ⏭️  跳过: 信号数量不足 (%d < 10)", event.Statistics.TotalSignals)
				continue
			}

			// 过滤条件2: SC颜色（只接受蓝色🟦或绿色🟢）
			if event.Statistics.SCLastColor != "🟦" && event.Statistics.SCLastColor != "🟢" {
				log.Printf("   ⏭️  跳过: SC颜色不符合 (%s)", event.Statistics.SCLastColor)
				continue
			}

			// 过滤条件3: BTC相关性（避免过高正相关）
			if event.Statistics.BTCCorrAvg > 0.7 {
				log.Printf("   ⏭️  跳过: BTC相关性过高 (%.2f > 0.7)", event.Statistics.BTCCorrAvg)
				continue
			}

			// 通过所有过滤条件

			// 检查是否已在watchlist中
			if at.isInWatchlist(event.Pair) {
				// 已在watchlist，更新screener数据并提高优先级
				log.Printf("   📝 [%s] %s 已在Watchlist中，更新数据", at.name, event.Pair)
				at.updateWatchlistWithNewSignal(event)
				continue
			}

			// 添加到候选列表（用于getCandidateCoins合并）
			at.screenerCandidatesMux.Lock()
			at.screenerCandidates[event.Pair] = true
			at.screenerCandidatesMux.Unlock()

			// 🔥 实时AI分析（在独立goroutine中执行，避免阻塞信号通道）
			go at.analyzeNewSignalRealtime(event)

			log.Printf("   ✅ [%s] 已添加 %s 到候选币种列表并触发实时分析", at.name, event.Pair)
		}
	}
}

// updateWatchlistWithNewSignal 更新watchlist中的币种（收到新screener信号时）
func (at *AutoTrader) updateWatchlistWithNewSignal(event *pool.ScreenerSignalEvent) {
	at.watchlistMux.Lock()
	defer at.watchlistMux.Unlock()

	entry, exists := at.watchlist[event.Pair]
	if !exists {
		return
	}

	// 更新screener数据
	entry.ScreenerTotalSignals = event.Statistics.TotalSignals
	entry.ScreenerSCCount = event.Statistics.SCCount
	entry.ScreenerBTCCorr = event.Statistics.BTCCorrAvg
	entry.ScreenerSCColor = event.Statistics.SCLastColor
	entry.LastAnalyzedAt = time.Now()

	// 提升优先级（最高到10）
	if entry.Priority < 10 {
		entry.Priority++
		log.Printf("   ⬆️  优先级提升: %s %d -> %d", event.Pair, entry.Priority-1, entry.Priority)
	}
}

// analyzeNewSignalRealtime 实时分析新的screener信号（在独立goroutine中运行）
func (at *AutoTrader) analyzeNewSignalRealtime(event *pool.ScreenerSignalEvent) {
	log.Printf("\n" + strings.Repeat("=", 70))
	log.Printf("🔥 [%s] 实时分析新信号: %s", at.name, event.Pair)
	log.Println(strings.Repeat("=", 70))

	// 检查当前持仓数量
	positions, err := at.trader.GetPositions()
	if err != nil {
		log.Printf("   ❌ 获取持仓失败: %v", err)
		return
	}

	activePositionCount := 0
	for _, pos := range positions {
		if quantity, ok := pos["positionAmt"].(float64); ok && quantity != 0 {
			activePositionCount++
		}
	}

	// 如果已达最大持仓数，添加到watchlist
	if activePositionCount >= at.maxPositions {
		log.Printf("   📝 持仓已满(%d/%d)，添加到Watchlist", activePositionCount, at.maxPositions)

		screenerData := &decision.ScreenerData{
			TotalSignals:  event.Statistics.TotalSignals,
			SCCount:       event.Statistics.SCCount,
			TSBinance:     event.Statistics.TSBinanceCount,
			TSBybit:       event.Statistics.TSBybitCount,
			BTCCorr:       event.Statistics.BTCCorrAvg,
			SCColor:       event.Statistics.SCLastColor,
			LastSignalAge: 0, // 刚收到信号
		}

		err := at.addToWatchlist(
			event.Pair,
			fmt.Sprintf("Screener信号: %d总信号, SC颜色%s, BTC相关性%.2f",
				event.Statistics.TotalSignals, event.Statistics.SCLastColor, event.Statistics.BTCCorrAvg),
			7, // 默认优先级7（较高）
			[]string{"screener"},
			screenerData,
		)
		if err != nil {
			log.Printf("   ❌ 添加到Watchlist失败: %v", err)
		}
		return
	}

	// 构建分析上下文（只包含这个币种）
	ctx, err := at.buildSingleCoinAnalysisContext(event.Pair, event.Statistics)
	if err != nil {
		log.Printf("   ❌ 构建分析上下文失败: %v", err)
		return
	}

	// 调用AI分析
	log.Printf("   🤖 请求AI分析...")
	fullDecision, err := decision.GetFullDecisionWithCustomPrompt(ctx, at.mcpClient, at.customPrompt, at.overrideBasePrompt, at.systemPromptTemplate)
	if err != nil {
		log.Printf("   ❌ AI分析失败: %v", err)
		return
	}

	// 处理AI决策
	if fullDecision == nil || len(fullDecision.Decisions) == 0 {
		log.Printf("   ⏸  AI决定: 暂不行动")
		return
	}

	for _, d := range fullDecision.Decisions {
		if d.Symbol != event.Pair {
			continue
		}

		log.Printf("   🤖 AI决策: %s - %s", d.Action, d.Reasoning)

		switch d.Action {
		case "open_long", "open_short":
			// AI决定立即开仓
			log.Printf("   ✅ 条件成熟，立即开仓")

			actionRecord := &logger.DecisionAction{
				Symbol:    d.Symbol,
				Action:    d.Action,
			}

			err := at.executeDecisionWithRecord(&d, actionRecord)
			if err != nil {
				log.Printf("   ❌ 开仓失败: %v", err)
			} else {
				// 开仓成功，从候选列表移除
				at.screenerCandidatesMux.Lock()
				delete(at.screenerCandidates, event.Pair)
				at.screenerCandidatesMux.Unlock()
			}

		case "add_to_watchlist":
			// AI决定添加到watchlist观察
			screenerData := &decision.ScreenerData{
				TotalSignals:  event.Statistics.TotalSignals,
				SCCount:       event.Statistics.SCCount,
				TSBinance:     event.Statistics.TSBinanceCount,
				TSBybit:       event.Statistics.TSBybitCount,
				BTCCorr:       event.Statistics.BTCCorrAvg,
				SCColor:       event.Statistics.SCLastColor,
				LastSignalAge: 0,
			}

			priority := d.Priority
			if priority <= 0 {
				priority = 5
			}

			err := at.addToWatchlist(event.Pair, d.Reasoning, priority, []string{"screener"}, screenerData)
			if err != nil {
				log.Printf("   ❌ 添加到Watchlist失败: %v", err)
			}

		case "wait", "hold":
			log.Printf("   ⏸  暂不行动，继续观察")

		default:
			log.Printf("   ⚠️  未知操作: %s", d.Action)
		}
	}

	log.Printf(strings.Repeat("=", 70) + "\n")
}

// buildSingleCoinAnalysisContext 为单个币种构建分析上下文（用于实时分析）
func (at *AutoTrader) buildSingleCoinAnalysisContext(symbol string, stats *pool.PairStatistics) (*decision.Context, error) {
	// 获取账户信息
	balance, err := at.trader.GetBalance()
	if err != nil {
		return nil, fmt.Errorf("获取账户余额失败: %w", err)
	}

	totalWalletBalance := 0.0
	totalUnrealizedProfit := 0.0
	availableBalance := 0.0

	if wallet, ok := balance["totalWalletBalance"].(float64); ok {
		totalWalletBalance = wallet
	}
	if unrealized, ok := balance["totalUnrealizedProfit"].(float64); ok {
		totalUnrealizedProfit = unrealized
	}
	if avail, ok := balance["availableBalance"].(float64); ok {
		availableBalance = avail
	}

	totalEquity := totalWalletBalance + totalUnrealizedProfit

	// 创建候选币种列表（只包含这一个币种）
	candidateCoins := []decision.CandidateCoin{
		{
			Symbol:  symbol,
			Sources: []string{"screener"},
		},
	}

	// 创建Screener数据映射
	screenerDataMap := make(map[string]*decision.ScreenerData)
	screenerDataMap[symbol] = &decision.ScreenerData{
		TotalSignals:  stats.TotalSignals,
		SCCount:       stats.SCCount,
		TSBinance:     stats.TSBinanceCount,
		TSBybit:       stats.TSBybitCount,
		BTCCorr:       stats.BTCCorrAvg,
		SCColor:       stats.SCLastColor,
		LastSignalAge: 0, // 刚收到信号
	}

	// 构建上下文
	ctx := &decision.Context{
		CurrentTime:      time.Now().Format("2006-01-02 15:04:05"),
		RuntimeMinutes:   int(time.Since(at.startTime).Minutes()),
		CallCount:        at.callCount,
		BTCETHLeverage:   at.config.BTCETHLeverage,
		AltcoinLeverage:  at.config.AltcoinLeverage,
		Account: decision.AccountInfo{
			TotalEquity:      totalEquity,
			AvailableBalance: availableBalance,
			PositionCount:    0, // 实时分析时不关心持仓数
		},
		Positions:       []decision.PositionInfo{}, // 空持仓列表
		CandidateCoins:  candidateCoins,
		ScreenerDataMap: screenerDataMap,
	}

	// 获取市场数据
	if err := decision.FetchMarketDataForContext(ctx); err != nil {
		return nil, fmt.Errorf("获取市场数据失败: %w", err)
	}

	// 添加Knowledge Base学习摘要（Paper Trading模式）
	if at.knowledgeBase != nil {
		summary, err := at.knowledgeBase.GetKnowledgeBaseSummary()
		if err == nil {
			ctx.KnowledgeBaseSummary = summary
		}
	}

	return ctx, nil
}

// autoSyncBalanceIfNeeded 自动同步余额（每10分钟检查一次，变化>5%才更新）
func (at *AutoTrader) autoSyncBalanceIfNeeded() {
	// 距离上次同步不足10分钟，跳过
	if time.Since(at.lastBalanceSyncTime) < 10*time.Minute {
		return
	}

	log.Printf("🔄 [%s] 开始自动检查余额变化...", at.name)

	// 查询实际余额
	balanceInfo, err := at.trader.GetBalance()
	if err != nil {
		log.Printf("⚠️ [%s] 查询余额失败: %v", at.name, err)
		at.lastBalanceSyncTime = time.Now() // 即使失败也更新时间，避免频繁重试
		return
	}

	// 提取可用余额
	var actualBalance float64
	if availableBalance, ok := balanceInfo["available_balance"].(float64); ok && availableBalance > 0 {
		actualBalance = availableBalance
	} else if availableBalance, ok := balanceInfo["availableBalance"].(float64); ok && availableBalance > 0 {
		actualBalance = availableBalance
	} else if totalBalance, ok := balanceInfo["balance"].(float64); ok && totalBalance > 0 {
		actualBalance = totalBalance
	} else {
		log.Printf("⚠️ [%s] 无法提取可用余额", at.name)
		at.lastBalanceSyncTime = time.Now()
		return
	}

	oldBalance := at.initialBalance

	// 防止除以零：如果初始余额无效，直接更新为实际余额
	if oldBalance <= 0 {
		log.Printf("⚠️ [%s] 初始余额无效 (%.2f)，直接更新为实际余额 %.2f USDT", at.name, oldBalance, actualBalance)
		at.initialBalance = actualBalance
		if at.database != nil {
			type DatabaseUpdater interface {
				UpdateTraderInitialBalance(userID, id string, newBalance float64) error
			}
			if db, ok := at.database.(DatabaseUpdater); ok {
				if err := db.UpdateTraderInitialBalance(at.userID, at.id, actualBalance); err != nil {
					log.Printf("❌ [%s] 更新数据库失败: %v", at.name, err)
				} else {
					log.Printf("✅ [%s] 已自动同步余额到数据库", at.name)
				}
			} else {
				log.Printf("⚠️ [%s] 数据库类型不支持UpdateTraderInitialBalance接口", at.name)
			}
		} else {
			log.Printf("⚠️ [%s] 数据库引用为空，余额仅在内存中更新", at.name)
		}
		at.lastBalanceSyncTime = time.Now()
		return
	}

	changePercent := ((actualBalance - oldBalance) / oldBalance) * 100

	// 变化超过5%才更新
	if math.Abs(changePercent) > 5.0 {
		log.Printf("🔔 [%s] 检测到余额大幅变化: %.2f → %.2f USDT (%.2f%%)",
			at.name, oldBalance, actualBalance, changePercent)

		// 更新内存中的 initialBalance
		at.initialBalance = actualBalance

		// 更新数据库（需要类型断言）
		if at.database != nil {
			// 这里需要根据实际的数据库类型进行类型断言
			// 由于使用了 interface{}，我们需要在 TraderManager 层面处理更新
			// 或者在这里进行类型检查
			type DatabaseUpdater interface {
				UpdateTraderInitialBalance(userID, id string, newBalance float64) error
			}
			if db, ok := at.database.(DatabaseUpdater); ok {
				err := db.UpdateTraderInitialBalance(at.userID, at.id, actualBalance)
				if err != nil {
					log.Printf("❌ [%s] 更新数据库失败: %v", at.name, err)
				} else {
					log.Printf("✅ [%s] 已自动同步余额到数据库", at.name)
				}
			} else {
				log.Printf("⚠️ [%s] 数据库类型不支持UpdateTraderInitialBalance接口", at.name)
			}
		} else {
			log.Printf("⚠️ [%s] 数据库引用为空，余额仅在内存中更新", at.name)
		}
	} else {
		log.Printf("✓ [%s] 余额变化不大 (%.2f%%)，无需更新", at.name, changePercent)
	}

	at.lastBalanceSyncTime = time.Now()
}

// runCycle 运行一个交易周期（使用AI全权决策）
func (at *AutoTrader) runCycle() error {
	at.callCount++

	log.Print("\n" + strings.Repeat("=", 70) + "\n")
	log.Printf("⏰ %s - AI决策周期 #%d", time.Now().Format("2006-01-02 15:04:05"), at.callCount)
	log.Println(strings.Repeat("=", 70))

	// 创建决策记录
	record := &logger.DecisionRecord{
		ExecutionLog: []string{},
		Success:      true,
	}

	// 1. 检查是否需要停止交易
	if time.Now().Before(at.stopUntil) {
		remaining := at.stopUntil.Sub(time.Now())
		log.Printf("⏸ 风险控制：暂停交易中，剩余 %.0f 分钟", remaining.Minutes())
		record.Success = false
		record.ErrorMessage = fmt.Sprintf("风险控制暂停中，剩余 %.0f 分钟", remaining.Minutes())
		at.decisionLogger.LogDecision(record)
		return nil
	}

	// 2. 重置日盈亏（每天重置）
	if time.Since(at.lastResetTime) > 24*time.Hour {
		at.dailyPnL = 0
		at.lastResetTime = time.Now()
		log.Println("📅 日盈亏已重置")
	}

	// 3. 自动同步余额（每10分钟检查一次，充值/提现后自动更新）
	at.autoSyncBalanceIfNeeded()

	// 4. 收集交易上下文
	ctx, err := at.buildTradingContext()
	if err != nil {
		record.Success = false
		record.ErrorMessage = fmt.Sprintf("构建交易上下文失败: %v", err)
		at.decisionLogger.LogDecision(record)
		return fmt.Errorf("构建交易上下文失败: %w", err)
	}

	// 保存账户状态快照
	record.AccountState = logger.AccountSnapshot{
		TotalBalance:          ctx.Account.TotalEquity,
		AvailableBalance:      ctx.Account.AvailableBalance,
		TotalUnrealizedProfit: ctx.Account.TotalPnL,
		PositionCount:         ctx.Account.PositionCount,
		MarginUsedPct:         ctx.Account.MarginUsedPct,
	}

	// 保存持仓快照
	for _, pos := range ctx.Positions {
		record.Positions = append(record.Positions, logger.PositionSnapshot{
			Symbol:           pos.Symbol,
			Side:             pos.Side,
			PositionAmt:      pos.Quantity,
			EntryPrice:       pos.EntryPrice,
			MarkPrice:        pos.MarkPrice,
			UnrealizedProfit: pos.UnrealizedPnL,
			Leverage:         float64(pos.Leverage),
			LiquidationPrice: pos.LiquidationPrice,
		})
	}

	log.Print(strings.Repeat("=", 70))
	for _, coin := range ctx.CandidateCoins {
		record.CandidateCoins = append(record.CandidateCoins, coin.Symbol)
	}

	log.Printf("📊 账户净值: %.2f USDT | 可用: %.2f USDT | 持仓: %d",
		ctx.Account.TotalEquity, ctx.Account.AvailableBalance, ctx.Account.PositionCount)

	// 5. 调用AI获取完整决策
	log.Printf("🤖 正在请求AI分析并决策... [模板: %s]", at.systemPromptTemplate)
	decision, err := decision.GetFullDecisionWithCustomPrompt(ctx, at.mcpClient, at.customPrompt, at.overrideBasePrompt, at.systemPromptTemplate)

	// 即使有错误，也保存思维链、决策和输入prompt（用于debug）
	if decision != nil {
		record.SystemPrompt = decision.SystemPrompt // 保存系统提示词
		record.InputPrompt = decision.UserPrompt
		record.CoTTrace = decision.CoTTrace
		if len(decision.Decisions) > 0 {
			decisionJSON, _ := json.MarshalIndent(decision.Decisions, "", "  ")
			record.DecisionJSON = string(decisionJSON)
		}
	}

	if err != nil {
		record.Success = false
		record.ErrorMessage = fmt.Sprintf("获取AI决策失败: %v", err)

		// 打印系统提示词和AI思维链（即使有错误，也要输出以便调试）
		if decision != nil {
			log.Print("\n" + strings.Repeat("=", 70) + "\n")
			log.Printf("📋 系统提示词 [模板: %s] (错误情况)", at.systemPromptTemplate)
			log.Println(strings.Repeat("=", 70))
			log.Println(decision.SystemPrompt)
			log.Println(strings.Repeat("=", 70))

			if decision.CoTTrace != "" {
				log.Print("\n" + strings.Repeat("-", 70) + "\n")
				log.Println("💭 AI思维链分析（错误情况）:")
				log.Println(strings.Repeat("-", 70))
				log.Println(decision.CoTTrace)
				log.Println(strings.Repeat("-", 70))
			}
		}

		at.decisionLogger.LogDecision(record)
		return fmt.Errorf("获取AI决策失败: %w", err)
	}

	// // 5. 打印系统提示词
	// log.Printf("\n" + strings.Repeat("=", 70))
	// log.Printf("📋 系统提示词 [模板: %s]", at.systemPromptTemplate)
	// log.Println(strings.Repeat("=", 70))
	// log.Println(decision.SystemPrompt)
	// log.Printf(strings.Repeat("=", 70) + "\n")

	// 6. 打印AI思维链
	// log.Printf("\n" + strings.Repeat("-", 70))
	// log.Println("💭 AI思维链分析:")
	// log.Println(strings.Repeat("-", 70))
	// log.Println(decision.CoTTrace)
	// log.Printf(strings.Repeat("-", 70) + "\n")

	// 7. 打印AI决策
	// log.Printf("📋 AI决策列表 (%d 个):\n", len(decision.Decisions))
	// for i, d := range decision.Decisions {
	//     log.Printf("  [%d] %s: %s - %s", i+1, d.Symbol, d.Action, d.Reasoning)
	//     if d.Action == "open_long" || d.Action == "open_short" {
	//        log.Printf("      杠杆: %dx | 仓位: %.2f USDT | 止损: %.4f | 止盈: %.4f",
	//           d.Leverage, d.PositionSizeUSD, d.StopLoss, d.TakeProfit)
	//     }
	// }
	log.Println()
	log.Print(strings.Repeat("-", 70))
	// 8. 对决策排序：确保先平仓后开仓（防止仓位叠加超限）
	log.Print(strings.Repeat("-", 70))

	// 8. 对决策排序：确保先平仓后开仓（防止仓位叠加超限）
	sortedDecisions := sortDecisionsByPriority(decision.Decisions)

	log.Println("🔄 执行顺序（已优化）: 先平仓→后开仓")
	for i, d := range sortedDecisions {
		log.Printf("  [%d] %s %s", i+1, d.Symbol, d.Action)
	}
	log.Println()

	// 执行决策并记录结果
	for _, d := range sortedDecisions {
		actionRecord := logger.DecisionAction{
			Action:    d.Action,
			Symbol:    d.Symbol,
			Quantity:  0,
			Leverage:  d.Leverage,
			Price:     0,
			Timestamp: time.Now(),
			Success:   false,
		}

		if err := at.executeDecisionWithRecord(&d, &actionRecord); err != nil {
			log.Printf("❌ 执行决策失败 (%s %s): %v", d.Symbol, d.Action, err)
			actionRecord.Error = err.Error()
			record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("❌ %s %s 失败: %v", d.Symbol, d.Action, err))
		} else {
			actionRecord.Success = true
			record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("✓ %s %s 成功", d.Symbol, d.Action))
			// 成功执行后短暂延迟
			time.Sleep(1 * time.Second)
		}

		record.Decisions = append(record.Decisions, actionRecord)
	}

	// 9. 保存决策记录
	if err := at.decisionLogger.LogDecision(record); err != nil {
		log.Printf("⚠ 保存决策记录失败: %v", err)
	}

	return nil
}

// buildTradingContext 构建交易上下文
func (at *AutoTrader) buildTradingContext() (*decision.Context, error) {
	// 1. 获取账户信息
	balance, err := at.trader.GetBalance()
	if err != nil {
		return nil, fmt.Errorf("获取账户余额失败: %w", err)
	}

	// 获取账户字段
	totalWalletBalance := 0.0
	totalUnrealizedProfit := 0.0
	availableBalance := 0.0

	if wallet, ok := balance["totalWalletBalance"].(float64); ok {
		totalWalletBalance = wallet
	}
	if unrealized, ok := balance["totalUnrealizedProfit"].(float64); ok {
		totalUnrealizedProfit = unrealized
	}
	if avail, ok := balance["availableBalance"].(float64); ok {
		availableBalance = avail
	}

	// Total Equity = 钱包余额 + 未实现盈亏
	totalEquity := totalWalletBalance + totalUnrealizedProfit

	// 2. 获取持仓信息
	positions, err := at.trader.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("获取持仓失败: %w", err)
	}

	var positionInfos []decision.PositionInfo
	totalMarginUsed := 0.0

	// 当前持仓的key集合（用于清理已平仓的记录）
	currentPositionKeys := make(map[string]bool)

	for _, pos := range positions {
		symbol := pos["symbol"].(string)
		side := pos["side"].(string)
		entryPrice := pos["entryPrice"].(float64)
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity // 空仓数量为负，转为正数
		}

		// 跳过已平仓的持仓（quantity = 0），防止"幽灵持仓"传递给AI
		if quantity == 0 {
			continue
		}

		unrealizedPnl := pos["unRealizedProfit"].(float64)
		liquidationPrice := pos["liquidationPrice"].(float64)

		// 计算盈亏百分比
		pnlPct := 0.0
		if side == "long" {
			pnlPct = ((markPrice - entryPrice) / entryPrice) * 100
		} else {
			pnlPct = ((entryPrice - markPrice) / entryPrice) * 100
		}

		// 计算占用保证金（估算）
		leverage := 10 // 默认值，实际应该从持仓信息获取
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}
		marginUsed := (quantity * markPrice) / float64(leverage)
		totalMarginUsed += marginUsed

		// 跟踪持仓首次出现时间
		posKey := symbol + "_" + side
		currentPositionKeys[posKey] = true
		if _, exists := at.positionFirstSeenTime[posKey]; !exists {
			// 新持仓，记录当前时间
			at.positionFirstSeenTime[posKey] = time.Now().UnixMilli()
		}
		updateTime := at.positionFirstSeenTime[posKey]

		positionInfos = append(positionInfos, decision.PositionInfo{
			Symbol:           symbol,
			Side:             side,
			EntryPrice:       entryPrice,
			MarkPrice:        markPrice,
			Quantity:         quantity,
			Leverage:         leverage,
			UnrealizedPnL:    unrealizedPnl,
			UnrealizedPnLPct: pnlPct,
			LiquidationPrice: liquidationPrice,
			MarginUsed:       marginUsed,
			UpdateTime:       updateTime,
		})
	}

	// 清理已平仓的持仓记录
	for key := range at.positionFirstSeenTime {
		if !currentPositionKeys[key] {
			delete(at.positionFirstSeenTime, key)
		}
	}

	// 3. 获取交易员的候选币种池
	candidateCoins, err := at.getCandidateCoins()
	if err != nil {
		return nil, fmt.Errorf("获取候选币种失败: %w", err)
	}

	// 4. 计算总盈亏
	totalPnL := totalEquity - at.initialBalance
	totalPnLPct := 0.0
	if at.initialBalance > 0 {
		totalPnLPct = (totalPnL / at.initialBalance) * 100
	}

	marginUsedPct := 0.0
	if totalEquity > 0 {
		marginUsedPct = (totalMarginUsed / totalEquity) * 100
	}

	// 5. 分析历史表现（最近100个周期，避免长期持仓的交易记录丢失）
	// 假设每3分钟一个周期，100个周期 = 5小时，足够覆盖大部分交易
	performance, err := at.decisionLogger.AnalyzePerformance(100)
	if err != nil {
		log.Printf("⚠️  分析历史表现失败: %v", err)
		// 不影响主流程，继续执行（但设置performance为nil以避免传递错误数据）
		performance = nil
	}

	// 6. 获取Screener统计数据（如果启用）
	var screenerDataMap map[string]*decision.ScreenerData
	if at.useScreenerSignals {
		screenerDataMap = make(map[string]*decision.ScreenerData)
		listener := pool.GetGlobalScreenerListener()
		if listener != nil {
			for _, coin := range candidateCoins {
				// 检查该币种是否来自screener
				hasScreener := false
				for _, source := range coin.Sources {
					if source == "screener" {
						hasScreener = true
						break
					}
				}

				if hasScreener {
					if stats, ok := listener.GetStatistics(coin.Symbol); ok {
						// 计算最后信号距今分钟数
						lastSignalAge := int(time.Since(stats.LastSignalDatetime).Minutes())

						screenerDataMap[coin.Symbol] = &decision.ScreenerData{
							TotalSignals:  stats.TotalSignals,
							SCCount:       stats.SCCount,
							TSBinance:     stats.TSBinanceCount,
							TSBybit:       stats.TSBybitCount,
							BTCCorr:       stats.BTCCorrAvg,
							SCColor:       stats.SCLastColor,
							LastSignalAge: lastSignalAge,
						}
					}
				}
			}
		}
	}

	// 7. 构建上下文
	ctx := &decision.Context{
		CurrentTime:     time.Now().Format("2006-01-02 15:04:05"),
		RuntimeMinutes:  int(time.Since(at.startTime).Minutes()),
		CallCount:       at.callCount,
		BTCETHLeverage:  at.config.BTCETHLeverage,  // 使用配置的杠杆倍数
		AltcoinLeverage: at.config.AltcoinLeverage, // 使用配置的杠杆倍数
		Account: decision.AccountInfo{
			TotalEquity:      totalEquity,
			AvailableBalance: availableBalance,
			TotalPnL:         totalPnL,
			TotalPnLPct:      totalPnLPct,
			MarginUsed:       totalMarginUsed,
			MarginUsedPct:    marginUsedPct,
			PositionCount:    len(positionInfos),
		},
		Positions:       positionInfos,
		CandidateCoins:  candidateCoins,
		ScreenerDataMap: screenerDataMap, // 添加Screener统计数据
		Performance:     performance,      // 添加历史表现分析
	}

	// 获取市场数据（包括BTC/ETH/候选币种等）
	if err := decision.FetchMarketDataForContext(ctx); err != nil {
		return nil, fmt.Errorf("获取市场数据失败: %w", err)
	}

	// 添加Knowledge Base学习摘要（Paper Trading模式）
	if at.knowledgeBase != nil {
		summary, err := at.knowledgeBase.GetKnowledgeBaseSummary()
		if err == nil {
			ctx.KnowledgeBaseSummary = summary
		}
	}

	return ctx, nil
}

// executeDecisionWithRecord 执行AI决策并记录详细信息
func (at *AutoTrader) executeDecisionWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	switch decision.Action {
	case "open_long":
		return at.executeOpenLongWithRecord(decision, actionRecord)
	case "open_short":
		return at.executeOpenShortWithRecord(decision, actionRecord)
	case "close_long":
		return at.executeCloseLongWithRecord(decision, actionRecord)
	case "close_short":
		return at.executeCloseShortWithRecord(decision, actionRecord)
	case "update_stop_loss":
		return at.executeUpdateStopLossWithRecord(decision, actionRecord)
	case "update_take_profit":
		return at.executeUpdateTakeProfitWithRecord(decision, actionRecord)
	case "partial_close":
		return at.executePartialCloseWithRecord(decision, actionRecord)
	case "add_to_watchlist":
		return at.executeAddToWatchlistWithRecord(decision, actionRecord)
	case "remove_from_watchlist":
		return at.executeRemoveFromWatchlistWithRecord(decision, actionRecord)
	case "hold", "wait":
		// 无需执行，仅记录
		return nil
	default:
		return fmt.Errorf("未知的action: %s", decision.Action)
	}
}

// executeOpenLongWithRecord 执行开多仓并记录详细信息
func (at *AutoTrader) executeOpenLongWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  📈 开多仓: %s", decision.Symbol)

	// Paper Trading模式
	if at.paperTradingMode && at.virtualPositionManager != nil {
		return at.executeOpenLongPaperTrading(decision, actionRecord)
	}

	// 真实交易模式
	// ⚠️ 关键：检查是否已有同币种同方向持仓，如果有则拒绝开仓（防止仓位叠加超限）
	positions, err := at.trader.GetPositions()
	if err == nil {
		for _, pos := range positions {
			if pos["symbol"] == decision.Symbol && pos["side"] == "long" {
				return fmt.Errorf("❌ %s 已有多仓，拒绝开仓以防止仓位叠加超限。如需换仓，请先给出 close_long 决策", decision.Symbol)
			}
		}
	}

	// 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}

	// 计算数量
	quantity := decision.PositionSizeUSD / marketData.CurrentPrice
	actionRecord.Quantity = quantity
	actionRecord.Price = marketData.CurrentPrice

	// ⚠️ 保证金验证：防止保证金不足错误（code=-2019）
	requiredMargin := decision.PositionSizeUSD / float64(decision.Leverage)

	balance, err := at.trader.GetBalance()
	if err != nil {
		return fmt.Errorf("获取账户余额失败: %w", err)
	}
	availableBalance := 0.0
	if avail, ok := balance["availableBalance"].(float64); ok {
		availableBalance = avail
	}

	// 手续费估算（Taker费率 0.04%）
	estimatedFee := decision.PositionSizeUSD * 0.0004
	totalRequired := requiredMargin + estimatedFee

	if totalRequired > availableBalance {
		return fmt.Errorf("❌ 保证金不足: 需要 %.2f USDT（保证金 %.2f + 手续费 %.2f），可用 %.2f USDT",
			totalRequired, requiredMargin, estimatedFee, availableBalance)
	}

	// 设置仓位模式
	if err := at.trader.SetMarginMode(decision.Symbol, at.config.IsCrossMargin); err != nil {
		log.Printf("  ⚠️ 设置仓位模式失败: %v", err)
		// 继续执行，不影响交易
	}

	// 开仓
	order, err := at.trader.OpenLong(decision.Symbol, quantity, decision.Leverage)
	if err != nil {
		return err
	}

	// 记录订单ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	log.Printf("  ✓ 开仓成功，订单ID: %v, 数量: %.4f", order["orderId"], quantity)

	// 记录开仓时间
	posKey := decision.Symbol + "_long"
	at.positionFirstSeenTime[posKey] = time.Now().UnixMilli()

	// 设置止损止盈
	if err := at.trader.SetStopLoss(decision.Symbol, "LONG", quantity, decision.StopLoss); err != nil {
		log.Printf("  ⚠ 设置止损失败: %v", err)
	}
	if err := at.trader.SetTakeProfit(decision.Symbol, "LONG", quantity, decision.TakeProfit); err != nil {
		log.Printf("  ⚠ 设置止盈失败: %v", err)
	}

	return nil
}

// executeOpenLongPaperTrading Paper Trading开多仓
func (at *AutoTrader) executeOpenLongPaperTrading(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	// 检查是否已有同币种持仓
	if _, exists := at.virtualPositionManager.GetPositionBySymbol(decision.Symbol); exists {
		return fmt.Errorf("❌ %s 已有虚拟持仓", decision.Symbol)
	}

	// 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}

	// 获取screener数据
	screenerData := make(map[string]interface{})
	listener := pool.GetGlobalScreenerListener()
	if listener != nil {
		if stats, ok := listener.GetStatistics(decision.Symbol); ok {
			screenerData["total_signals"] = stats.TotalSignals
			screenerData["sc_color"] = stats.SCLastColor
			screenerData["btc_corr"] = stats.BTCCorrAvg
		}
	}

	// 开虚拟仓位
	virtualPos, err := at.virtualPositionManager.OpenVirtualPosition(
		decision.Symbol,
		"long",
		marketData.CurrentPrice,
		decision.PositionSizeUSD,
		decision.Leverage,
		decision.StopLoss,
		decision.TakeProfit,
		decision.Reasoning,
		decision.Confidence,
		screenerData,
	)

	if err != nil {
		return err
	}

	// 记录到actionRecord
	actionRecord.Quantity = virtualPos.Quantity
	actionRecord.Price = virtualPos.EntryPrice

	log.Printf("  ✓ [Paper Trading] 开多仓成功: %s, 价格%.4f, 数量%.4f, 杠杆%dx",
		decision.Symbol, virtualPos.EntryPrice, virtualPos.Quantity, virtualPos.Leverage)

	return nil
}

// executeOpenShortWithRecord 执行开空仓并记录详细信息
func (at *AutoTrader) executeOpenShortWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  📉 开空仓: %s", decision.Symbol)

	// Paper Trading模式
	if at.paperTradingMode && at.virtualPositionManager != nil {
		return at.executeOpenShortPaperTrading(decision, actionRecord)
	}

	// 真实交易模式
	// ⚠️ 关键：检查是否已有同币种同方向持仓，如果有则拒绝开仓（防止仓位叠加超限）
	positions, err := at.trader.GetPositions()
	if err == nil {
		for _, pos := range positions {
			if pos["symbol"] == decision.Symbol && pos["side"] == "short" {
				return fmt.Errorf("❌ %s 已有空仓，拒绝开仓以防止仓位叠加超限。如需换仓，请先给出 close_short 决策", decision.Symbol)
			}
		}
	}

	// 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}

	// 计算数量
	quantity := decision.PositionSizeUSD / marketData.CurrentPrice
	actionRecord.Quantity = quantity
	actionRecord.Price = marketData.CurrentPrice

	// ⚠️ 保证金验证：防止保证金不足错误（code=-2019）
	requiredMargin := decision.PositionSizeUSD / float64(decision.Leverage)

	balance, err := at.trader.GetBalance()
	if err != nil {
		return fmt.Errorf("获取账户余额失败: %w", err)
	}
	availableBalance := 0.0
	if avail, ok := balance["availableBalance"].(float64); ok {
		availableBalance = avail
	}

	// 手续费估算（Taker费率 0.04%）
	estimatedFee := decision.PositionSizeUSD * 0.0004
	totalRequired := requiredMargin + estimatedFee

	if totalRequired > availableBalance {
		return fmt.Errorf("❌ 保证金不足: 需要 %.2f USDT（保证金 %.2f + 手续费 %.2f），可用 %.2f USDT",
			totalRequired, requiredMargin, estimatedFee, availableBalance)
	}

	// 设置仓位模式
	if err := at.trader.SetMarginMode(decision.Symbol, at.config.IsCrossMargin); err != nil {
		log.Printf("  ⚠️ 设置仓位模式失败: %v", err)
		// 继续执行，不影响交易
	}

	// 开仓
	order, err := at.trader.OpenShort(decision.Symbol, quantity, decision.Leverage)
	if err != nil {
		return err
	}

	// 记录订单ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	log.Printf("  ✓ 开仓成功，订单ID: %v, 数量: %.4f", order["orderId"], quantity)

	// 记录开仓时间
	posKey := decision.Symbol + "_short"
	at.positionFirstSeenTime[posKey] = time.Now().UnixMilli()

	// 设置止损止盈
	if err := at.trader.SetStopLoss(decision.Symbol, "SHORT", quantity, decision.StopLoss); err != nil {
		log.Printf("  ⚠ 设置止损失败: %v", err)
	}
	if err := at.trader.SetTakeProfit(decision.Symbol, "SHORT", quantity, decision.TakeProfit); err != nil {
		log.Printf("  ⚠ 设置止盈失败: %v", err)
	}

	return nil
}

// executeOpenShortPaperTrading Paper Trading开空仓
func (at *AutoTrader) executeOpenShortPaperTrading(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	// 检查是否已有同币种持仓
	if _, exists := at.virtualPositionManager.GetPositionBySymbol(decision.Symbol); exists {
		return fmt.Errorf("❌ %s 已有虚拟持仓", decision.Symbol)
	}

	// 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}

	// 获取screener数据
	screenerData := make(map[string]interface{})
	listener := pool.GetGlobalScreenerListener()
	if listener != nil {
		if stats, ok := listener.GetStatistics(decision.Symbol); ok {
			screenerData["total_signals"] = stats.TotalSignals
			screenerData["sc_color"] = stats.SCLastColor
			screenerData["btc_corr"] = stats.BTCCorrAvg
		}
	}

	// 开虚拟仓位
	virtualPos, err := at.virtualPositionManager.OpenVirtualPosition(
		decision.Symbol,
		"short",
		marketData.CurrentPrice,
		decision.PositionSizeUSD,
		decision.Leverage,
		decision.StopLoss,
		decision.TakeProfit,
		decision.Reasoning,
		decision.Confidence,
		screenerData,
	)

	if err != nil {
		return err
	}

	// 记录到actionRecord
	actionRecord.Quantity = virtualPos.Quantity
	actionRecord.Price = virtualPos.EntryPrice

	log.Printf("  ✓ [Paper Trading] 开空仓成功: %s, 价格%.4f, 数量%.4f, 杠杆%dx",
		decision.Symbol, virtualPos.EntryPrice, virtualPos.Quantity, virtualPos.Leverage)

	return nil
}

// executeCloseLongWithRecord 执行平多仓并记录详细信息
func (at *AutoTrader) executeCloseLongWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  🔄 平多仓: %s", decision.Symbol)

	// Paper Trading模式
	if at.paperTradingMode && at.virtualPositionManager != nil {
		return at.executeClosePaperTrading(decision.Symbol, "AI决定平仓", actionRecord)
	}

	// 真实交易模式
	// 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}
	actionRecord.Price = marketData.CurrentPrice

	// 平仓
	order, err := at.trader.CloseLong(decision.Symbol, 0) // 0 = 全部平仓
	if err != nil {
		return err
	}

	// 记录订单ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	log.Printf("  ✓ 平仓成功")
	return nil
}

// executeCloseShortWithRecord 执行平空仓并记录详细信息
func (at *AutoTrader) executeCloseShortWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  🔄 平空仓: %s", decision.Symbol)

	// Paper Trading模式
	if at.paperTradingMode && at.virtualPositionManager != nil {
		return at.executeClosePaperTrading(decision.Symbol, "AI决定平仓", actionRecord)
	}

	// 真实交易模式
	// 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}
	actionRecord.Price = marketData.CurrentPrice

	// 平仓
	order, err := at.trader.CloseShort(decision.Symbol, 0) // 0 = 全部平仓
	if err != nil {
		return err
	}

	// 记录订单ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	log.Printf("  ✓ 平仓成功")
	return nil
}

// executeClosePaperTrading Paper Trading平仓（统一方法）
func (at *AutoTrader) executeClosePaperTrading(symbol, closeReason string, actionRecord *logger.DecisionAction) error {
	// 获取虚拟持仓
	virtualPos, exists := at.virtualPositionManager.GetPositionBySymbol(symbol)
	if !exists {
		return fmt.Errorf("❌ 虚拟持仓不存在: %s", symbol)
	}

	// 获取当前价格
	marketData, err := market.Get(symbol)
	if err != nil {
		return err
	}

	// 平仓
	err = at.virtualPositionManager.CloseVirtualPosition(virtualPos.ID, marketData.CurrentPrice, closeReason)
	if err != nil {
		return err
	}

	actionRecord.Price = marketData.CurrentPrice

	log.Printf("  ✓ [Paper Trading] 平仓成功")
	return nil
}

// executeUpdateStopLossPaperTrading Paper Trading调整止损
func (at *AutoTrader) executeUpdateStopLossPaperTrading(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	// 获取虚拟持仓
	virtualPos, exists := at.virtualPositionManager.GetPositionBySymbol(decision.Symbol)
	if !exists {
		return fmt.Errorf("❌ 虚拟持仓不存在: %s", decision.Symbol)
	}

	// 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}
	actionRecord.Price = marketData.CurrentPrice

	// 验证新止损价格合理性
	if virtualPos.Side == "long" && decision.NewStopLoss >= marketData.CurrentPrice {
		return fmt.Errorf("多单止损必须低于当前价格 (当前: %.2f, 新止损: %.2f)", marketData.CurrentPrice, decision.NewStopLoss)
	}
	if virtualPos.Side == "short" && decision.NewStopLoss <= marketData.CurrentPrice {
		return fmt.Errorf("空单止损必须高于当前价格 (当前: %.2f, 新止损: %.2f)", marketData.CurrentPrice, decision.NewStopLoss)
	}

	// 更新止损价格
	oldStopLoss := virtualPos.StopLoss
	virtualPos.StopLoss = decision.NewStopLoss

	log.Printf("  ✓ [Paper Trading] 止损已调整: %.2f → %.2f (当前价格: %.2f)",
		oldStopLoss, decision.NewStopLoss, marketData.CurrentPrice)
	return nil
}

// executeUpdateTakeProfitPaperTrading Paper Trading调整止盈
func (at *AutoTrader) executeUpdateTakeProfitPaperTrading(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	// 获取虚拟持仓
	virtualPos, exists := at.virtualPositionManager.GetPositionBySymbol(decision.Symbol)
	if !exists {
		return fmt.Errorf("❌ 虚拟持仓不存在: %s", decision.Symbol)
	}

	// 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}
	actionRecord.Price = marketData.CurrentPrice

	// 验证新止盈价格合理性
	if virtualPos.Side == "long" && decision.NewTakeProfit <= marketData.CurrentPrice {
		return fmt.Errorf("多单止盈必须高于当前价格 (当前: %.2f, 新止盈: %.2f)", marketData.CurrentPrice, decision.NewTakeProfit)
	}
	if virtualPos.Side == "short" && decision.NewTakeProfit >= marketData.CurrentPrice {
		return fmt.Errorf("空单止盈必须低于当前价格 (当前: %.2f, 新止盈: %.2f)", marketData.CurrentPrice, decision.NewTakeProfit)
	}

	// 更新止盈价格
	oldTakeProfit := virtualPos.TakeProfit
	virtualPos.TakeProfit = decision.NewTakeProfit

	log.Printf("  ✓ [Paper Trading] 止盈已调整: %.2f → %.2f (当前价格: %.2f)",
		oldTakeProfit, decision.NewTakeProfit, marketData.CurrentPrice)
	return nil
}

// executeUpdateStopLossWithRecord 执行调整止损并记录详细信息
func (at *AutoTrader) executeUpdateStopLossWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  🎯 调整止损: %s → %.2f", decision.Symbol, decision.NewStopLoss)

	// 🔄 Paper Trading Mode
	if at.paperTradingMode && at.virtualPositionManager != nil {
		return at.executeUpdateStopLossPaperTrading(decision, actionRecord)
	}

	// 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}
	actionRecord.Price = marketData.CurrentPrice

	// 获取当前持仓
	positions, err := at.trader.GetPositions()
	if err != nil {
		return fmt.Errorf("获取持仓失败: %w", err)
	}

	// 查找目标持仓
	var targetPosition map[string]interface{}
	for _, pos := range positions {
		symbol, _ := pos["symbol"].(string)
		posAmt, _ := pos["positionAmt"].(float64)
		if symbol == decision.Symbol && posAmt != 0 {
			targetPosition = pos
			break
		}
	}

	if targetPosition == nil {
		return fmt.Errorf("持仓不存在: %s", decision.Symbol)
	}

	// 获取持仓方向和数量
	side, _ := targetPosition["side"].(string)
	positionSide := strings.ToUpper(side)
	positionAmt, _ := targetPosition["positionAmt"].(float64)

	// 验证新止损价格合理性
	if positionSide == "LONG" && decision.NewStopLoss >= marketData.CurrentPrice {
		return fmt.Errorf("多单止损必须低于当前价格 (当前: %.2f, 新止损: %.2f)", marketData.CurrentPrice, decision.NewStopLoss)
	}
	if positionSide == "SHORT" && decision.NewStopLoss <= marketData.CurrentPrice {
		return fmt.Errorf("空单止损必须高于当前价格 (当前: %.2f, 新止损: %.2f)", marketData.CurrentPrice, decision.NewStopLoss)
	}

	// ⚠️ 防御性检查：检测是否存在双向持仓（不应该出现，但提供保护）
	var hasOppositePosition bool
	oppositeSide := ""
	for _, pos := range positions {
		symbol, _ := pos["symbol"].(string)
		posSide, _ := pos["side"].(string)
		posAmt, _ := pos["positionAmt"].(float64)
		if symbol == decision.Symbol && posAmt != 0 && strings.ToUpper(posSide) != positionSide {
			hasOppositePosition = true
			oppositeSide = strings.ToUpper(posSide)
			break
		}
	}

	if hasOppositePosition {
		log.Printf("  🚨 警告：检测到 %s 存在双向持仓（%s + %s），这违反了策略规则",
			decision.Symbol, positionSide, oppositeSide)
		log.Printf("  🚨 取消止损单将影响两个方向的订单，请检查是否为用户手动操作导致")
		log.Printf("  🚨 建议：手动平掉其中一个方向的持仓，或检查系统是否有BUG")
	}

	// 取消旧的止损单（只删除止损单，不影响止盈单）
	// 注意：如果存在双向持仓，这会删除两个方向的止损单
	if err := at.trader.CancelStopLossOrders(decision.Symbol); err != nil {
		log.Printf("  ⚠ 取消旧止损单失败: %v", err)
		// 不中断执行，继续设置新止损
	}

	// 调用交易所 API 修改止损
	quantity := math.Abs(positionAmt)
	err = at.trader.SetStopLoss(decision.Symbol, positionSide, quantity, decision.NewStopLoss)
	if err != nil {
		return fmt.Errorf("修改止损失败: %w", err)
	}

	log.Printf("  ✓ 止损已调整: %.2f (当前价格: %.2f)", decision.NewStopLoss, marketData.CurrentPrice)
	return nil
}

// executeUpdateTakeProfitWithRecord 执行调整止盈并记录详细信息
func (at *AutoTrader) executeUpdateTakeProfitWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  🎯 调整止盈: %s → %.2f", decision.Symbol, decision.NewTakeProfit)

	// 🔄 Paper Trading Mode
	if at.paperTradingMode && at.virtualPositionManager != nil {
		return at.executeUpdateTakeProfitPaperTrading(decision, actionRecord)
	}

	// 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}
	actionRecord.Price = marketData.CurrentPrice

	// 获取当前持仓
	positions, err := at.trader.GetPositions()
	if err != nil {
		return fmt.Errorf("获取持仓失败: %w", err)
	}

	// 查找目标持仓
	var targetPosition map[string]interface{}
	for _, pos := range positions {
		symbol, _ := pos["symbol"].(string)
		posAmt, _ := pos["positionAmt"].(float64)
		if symbol == decision.Symbol && posAmt != 0 {
			targetPosition = pos
			break
		}
	}

	if targetPosition == nil {
		return fmt.Errorf("持仓不存在: %s", decision.Symbol)
	}

	// 获取持仓方向和数量
	side, _ := targetPosition["side"].(string)
	positionSide := strings.ToUpper(side)
	positionAmt, _ := targetPosition["positionAmt"].(float64)

	// 验证新止盈价格合理性
	if positionSide == "LONG" && decision.NewTakeProfit <= marketData.CurrentPrice {
		return fmt.Errorf("多单止盈必须高于当前价格 (当前: %.2f, 新止盈: %.2f)", marketData.CurrentPrice, decision.NewTakeProfit)
	}
	if positionSide == "SHORT" && decision.NewTakeProfit >= marketData.CurrentPrice {
		return fmt.Errorf("空单止盈必须低于当前价格 (当前: %.2f, 新止盈: %.2f)", marketData.CurrentPrice, decision.NewTakeProfit)
	}

	// ⚠️ 防御性检查：检测是否存在双向持仓（不应该出现，但提供保护）
	var hasOppositePosition bool
	oppositeSide := ""
	for _, pos := range positions {
		symbol, _ := pos["symbol"].(string)
		posSide, _ := pos["side"].(string)
		posAmt, _ := pos["positionAmt"].(float64)
		if symbol == decision.Symbol && posAmt != 0 && strings.ToUpper(posSide) != positionSide {
			hasOppositePosition = true
			oppositeSide = strings.ToUpper(posSide)
			break
		}
	}

	if hasOppositePosition {
		log.Printf("  🚨 警告：检测到 %s 存在双向持仓（%s + %s），这违反了策略规则",
			decision.Symbol, positionSide, oppositeSide)
		log.Printf("  🚨 取消止盈单将影响两个方向的订单，请检查是否为用户手动操作导致")
		log.Printf("  🚨 建议：手动平掉其中一个方向的持仓，或检查系统是否有BUG")
	}

	// 取消旧的止盈单（只删除止盈单，不影响止损单）
	// 注意：如果存在双向持仓，这会删除两个方向的止盈单
	if err := at.trader.CancelTakeProfitOrders(decision.Symbol); err != nil {
		log.Printf("  ⚠ 取消旧止盈单失败: %v", err)
		// 不中断执行，继续设置新止盈
	}

	// 调用交易所 API 修改止盈
	quantity := math.Abs(positionAmt)
	err = at.trader.SetTakeProfit(decision.Symbol, positionSide, quantity, decision.NewTakeProfit)
	if err != nil {
		return fmt.Errorf("修改止盈失败: %w", err)
	}

	log.Printf("  ✓ 止盈已调整: %.2f (当前价格: %.2f)", decision.NewTakeProfit, marketData.CurrentPrice)
	return nil
}

// executePartialCloseWithRecord 执行部分平仓并记录详细信息
func (at *AutoTrader) executePartialCloseWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  📊 部分平仓: %s %.1f%%", decision.Symbol, decision.ClosePercentage)

	// 🔄 Paper Trading Mode - 不支持部分平仓
	if at.paperTradingMode && at.virtualPositionManager != nil {
		return fmt.Errorf("❌ Paper Trading模式不支持部分平仓，请使用完全平仓（close_long/close_short）")
	}

	// 验证百分比范围
	if decision.ClosePercentage <= 0 || decision.ClosePercentage > 100 {
		return fmt.Errorf("平仓百分比必须在 0-100 之间，当前: %.1f", decision.ClosePercentage)
	}

	// 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return err
	}
	actionRecord.Price = marketData.CurrentPrice

	// 获取当前持仓
	positions, err := at.trader.GetPositions()
	if err != nil {
		return fmt.Errorf("获取持仓失败: %w", err)
	}

	// 查找目标持仓
	var targetPosition map[string]interface{}
	for _, pos := range positions {
		symbol, _ := pos["symbol"].(string)
		posAmt, _ := pos["positionAmt"].(float64)
		if symbol == decision.Symbol && posAmt != 0 {
			targetPosition = pos
			break
		}
	}

	if targetPosition == nil {
		return fmt.Errorf("持仓不存在: %s", decision.Symbol)
	}

	// 获取持仓方向和数量
	side, _ := targetPosition["side"].(string)
	positionSide := strings.ToUpper(side)
	positionAmt, _ := targetPosition["positionAmt"].(float64)

	// 计算平仓数量
	totalQuantity := math.Abs(positionAmt)
	closeQuantity := totalQuantity * (decision.ClosePercentage / 100.0)
	actionRecord.Quantity = closeQuantity

	// 执行平仓
	var order map[string]interface{}
	if positionSide == "LONG" {
		order, err = at.trader.CloseLong(decision.Symbol, closeQuantity)
	} else {
		order, err = at.trader.CloseShort(decision.Symbol, closeQuantity)
	}

	if err != nil {
		return fmt.Errorf("部分平仓失败: %w", err)
	}

	// 记录订单ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	remainingQuantity := totalQuantity - closeQuantity
	log.Printf("  ✓ 部分平仓成功: 平仓 %.4f (%.1f%%), 剩余 %.4f",
		closeQuantity, decision.ClosePercentage, remainingQuantity)

	return nil
}

// executeAddToWatchlistWithRecord 添加到监控列表并记录
func (at *AutoTrader) executeAddToWatchlistWithRecord(d *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  📝 添加到Watchlist: %s (优先级:%d)", d.Symbol, d.Priority)

	// 默认优先级为5
	priority := d.Priority
	if priority <= 0 {
		priority = 5
	}
	if priority > 10 {
		priority = 10
	}

	// 获取币种来源
	sources := []string{"ai_decision"}

	// 获取screener数据（如果有）
	var screenerData *decision.ScreenerData
	listener := pool.GetGlobalScreenerListener()
	if listener != nil {
		if stats, ok := listener.GetStatistics(d.Symbol); ok {
			lastSignalAge := int(time.Since(stats.LastSignalDatetime).Minutes())
			screenerData = &decision.ScreenerData{
				TotalSignals:  stats.TotalSignals,
				SCCount:       stats.SCCount,
				TSBinance:     stats.TSBinanceCount,
				TSBybit:       stats.TSBybitCount,
				BTCCorr:       stats.BTCCorrAvg,
				SCColor:       stats.SCLastColor,
				LastSignalAge: lastSignalAge,
			}
			sources = append(sources, "screener")
		}
	}

	// 添加到watchlist
	err := at.addToWatchlist(d.Symbol, d.Reasoning, priority, sources, screenerData)
	if err != nil {
		return fmt.Errorf("添加到Watchlist失败: %w", err)
	}

	return nil
}

// executeRemoveFromWatchlistWithRecord 从监控列表移除并记录
func (at *AutoTrader) executeRemoveFromWatchlistWithRecord(d *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  📝 从Watchlist移除: %s", d.Symbol)

	// 从watchlist移除
	at.removeFromWatchlist(d.Symbol, d.Reasoning)

	return nil
}

// GetID 获取trader ID
func (at *AutoTrader) GetID() string {
	return at.id
}

// GetName 获取trader名称
func (at *AutoTrader) GetName() string {
	return at.name
}

// GetAIModel 获取AI模型
func (at *AutoTrader) GetAIModel() string {
	return at.aiModel
}

// GetExchange 获取交易所
func (at *AutoTrader) GetExchange() string {
	return at.exchange
}

// SetCustomPrompt 设置自定义交易策略prompt
func (at *AutoTrader) SetCustomPrompt(prompt string) {
	at.customPrompt = prompt
}

// SetOverrideBasePrompt 设置是否覆盖基础prompt
func (at *AutoTrader) SetOverrideBasePrompt(override bool) {
	at.overrideBasePrompt = override
}

// SetSystemPromptTemplate 设置系统提示词模板
func (at *AutoTrader) SetSystemPromptTemplate(templateName string) {
	at.systemPromptTemplate = templateName
}

// GetSystemPromptTemplate 获取当前系统提示词模板名称
func (at *AutoTrader) GetSystemPromptTemplate() string {
	return at.systemPromptTemplate
}

// GetDecisionLogger 获取决策日志记录器
func (at *AutoTrader) GetDecisionLogger() *logger.DecisionLogger {
	return at.decisionLogger
}

// GetStatus 获取系统状态（用于API）
func (at *AutoTrader) GetStatus() map[string]interface{} {
	aiProvider := "DeepSeek"
	if at.config.UseQwen {
		aiProvider = "Qwen"
	}

	return map[string]interface{}{
		"trader_id":       at.id,
		"trader_name":     at.name,
		"ai_model":        at.aiModel,
		"exchange":        at.exchange,
		"is_running":      at.isRunning,
		"start_time":      at.startTime.Format(time.RFC3339),
		"runtime_minutes": int(time.Since(at.startTime).Minutes()),
		"call_count":      at.callCount,
		"initial_balance": at.initialBalance,
		"scan_interval":   at.config.ScanInterval.String(),
		"stop_until":      at.stopUntil.Format(time.RFC3339),
		"last_reset_time": at.lastResetTime.Format(time.RFC3339),
		"ai_provider":     aiProvider,
	}
}

// GetAccountInfo 获取账户信息（用于API）
func (at *AutoTrader) GetAccountInfo() (map[string]interface{}, error) {
	balance, err := at.trader.GetBalance()
	if err != nil {
		return nil, fmt.Errorf("获取余额失败: %w", err)
	}

	// 获取账户字段
	totalWalletBalance := 0.0
	totalUnrealizedProfit := 0.0
	availableBalance := 0.0

	if wallet, ok := balance["totalWalletBalance"].(float64); ok {
		totalWalletBalance = wallet
	}
	if unrealized, ok := balance["totalUnrealizedProfit"].(float64); ok {
		totalUnrealizedProfit = unrealized
	}
	if avail, ok := balance["availableBalance"].(float64); ok {
		availableBalance = avail
	}

	// Total Equity = 钱包余额 + 未实现盈亏
	totalEquity := totalWalletBalance + totalUnrealizedProfit

	// 获取持仓计算总保证金
	positions, err := at.trader.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("获取持仓失败: %w", err)
	}

	totalMarginUsed := 0.0
	totalUnrealizedPnL := 0.0
	for _, pos := range positions {
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity
		}
		unrealizedPnl := pos["unRealizedProfit"].(float64)
		totalUnrealizedPnL += unrealizedPnl

		leverage := 10
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}
		marginUsed := (quantity * markPrice) / float64(leverage)
		totalMarginUsed += marginUsed
	}

	totalPnL := totalEquity - at.initialBalance
	totalPnLPct := 0.0
	if at.initialBalance > 0 {
		totalPnLPct = (totalPnL / at.initialBalance) * 100
	}

	marginUsedPct := 0.0
	if totalEquity > 0 {
		marginUsedPct = (totalMarginUsed / totalEquity) * 100
	}

	return map[string]interface{}{
		// 核心字段
		"total_equity":      totalEquity,           // 账户净值 = wallet + unrealized
		"wallet_balance":    totalWalletBalance,    // 钱包余额（不含未实现盈亏）
		"unrealized_profit": totalUnrealizedProfit, // 未实现盈亏（从API）
		"available_balance": availableBalance,      // 可用余额

		// 盈亏统计
		"total_pnl":            totalPnL,           // 总盈亏 = equity - initial
		"total_pnl_pct":        totalPnLPct,        // 总盈亏百分比
		"total_unrealized_pnl": totalUnrealizedPnL, // 未实现盈亏（从持仓计算）
		"initial_balance":      at.initialBalance,  // 初始余额
		"daily_pnl":            at.dailyPnL,        // 日盈亏

		// 持仓信息
		"position_count":  len(positions),  // 持仓数量
		"margin_used":     totalMarginUsed, // 保证金占用
		"margin_used_pct": marginUsedPct,   // 保证金使用率
	}, nil
}

// GetPositions 获取持仓列表（用于API）
func (at *AutoTrader) GetPositions() ([]map[string]interface{}, error) {
	positions, err := at.trader.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("获取持仓失败: %w", err)
	}

	var result []map[string]interface{}
	for _, pos := range positions {
		symbol := pos["symbol"].(string)
		side := pos["side"].(string)
		entryPrice := pos["entryPrice"].(float64)
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity
		}
		unrealizedPnl := pos["unRealizedProfit"].(float64)
		liquidationPrice := pos["liquidationPrice"].(float64)

		leverage := 10
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}

		// 计算占用保证金
		marginUsed := (quantity * markPrice) / float64(leverage)

		// 计算盈亏百分比（基于保证金）
		// 收益率 = 未实现盈亏 / 保证金 × 100%
		pnlPct := 0.0
		if marginUsed > 0 {
			pnlPct = (unrealizedPnl / marginUsed) * 100
		}

		result = append(result, map[string]interface{}{
			"symbol":             symbol,
			"side":               side,
			"entry_price":        entryPrice,
			"mark_price":         markPrice,
			"quantity":           quantity,
			"leverage":           leverage,
			"unrealized_pnl":     unrealizedPnl,
			"unrealized_pnl_pct": pnlPct,
			"liquidation_price":  liquidationPrice,
			"margin_used":        marginUsed,
		})
	}

	return result, nil
}

// sortDecisionsByPriority 对决策排序：先平仓，再开仓，最后hold/wait
// 这样可以避免换仓时仓位叠加超限
func sortDecisionsByPriority(decisions []decision.Decision) []decision.Decision {
	if len(decisions) <= 1 {
		return decisions
	}

	// 定义优先级
	getActionPriority := func(action string) int {
		switch action {
		case "close_long", "close_short", "partial_close":
			return 1 // 最高优先级：先平仓（包括部分平仓）
		case "update_stop_loss", "update_take_profit":
			return 2 // 调整持仓止盈止损
		case "open_long", "open_short":
			return 3 // 次优先级：后开仓
		case "hold", "wait":
			return 4 // 最低优先级：观望
		default:
			return 999 // 未知动作放最后
		}
	}

	// 复制决策列表
	sorted := make([]decision.Decision, len(decisions))
	copy(sorted, decisions)

	// 按优先级排序
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if getActionPriority(sorted[i].Action) > getActionPriority(sorted[j].Action) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	return sorted
}

// getCandidateCoins 获取交易员的候选币种列表
func (at *AutoTrader) getCandidateCoins() ([]decision.CandidateCoin, error) {
	var candidateCoins []decision.CandidateCoin

	if len(at.tradingCoins) == 0 {
		// 使用数据库配置的默认币种列表
		if len(at.defaultCoins) > 0 {
			// 使用数据库中配置的默认币种
			for _, coin := range at.defaultCoins {
				symbol := normalizeSymbol(coin)
				candidateCoins = append(candidateCoins, decision.CandidateCoin{
					Symbol:  symbol,
					Sources: []string{"default"}, // 标记为数据库默认币种
				})
			}
			log.Printf("📋 [%s] 使用数据库默认币种: %d个币种 %v",
				at.name, len(candidateCoins), at.defaultCoins)
		} else {
			// 如果数据库中没有配置默认币种，则使用AI500+OI Top作为fallback
			const ai500Limit = 20 // AI500取前20个评分最高的币种

			mergedPool, err := pool.GetMergedCoinPool(ai500Limit)
			if err != nil {
				return nil, fmt.Errorf("获取合并币种池失败: %w", err)
			}

			// 构建候选币种列表（包含来源信息）
			for _, symbol := range mergedPool.AllSymbols {
				sources := mergedPool.SymbolSources[symbol]
				candidateCoins = append(candidateCoins, decision.CandidateCoin{
					Symbol:  symbol,
					Sources: sources, // "ai500" 和/或 "oi_top"
				})
			}

			log.Printf("📋 [%s] 数据库无默认币种配置，使用AI500+OI Top: AI500前%d + OI_Top20 = 总计%d个候选币种",
				at.name, ai500Limit, len(candidateCoins))
		}
	} else {
		// 使用自定义币种列表
		for _, coin := range at.tradingCoins {
			// 确保币种格式正确（转为大写USDT交易对）
			symbol := normalizeSymbol(coin)
			candidateCoins = append(candidateCoins, decision.CandidateCoin{
				Symbol:  symbol,
				Sources: []string{"custom"}, // 标记为自定义来源
			})
		}

		log.Printf("📋 [%s] 使用自定义币种: %d个币种 %v",
			at.name, len(candidateCoins), at.tradingCoins)
	}

	// 如果启用了Screener信号，将Screener候选币种合并到列表中
	if at.useScreenerSignals {
		at.screenerCandidatesMux.RLock()
		screenerCount := len(at.screenerCandidates)
		screenerPairs := make([]string, 0, screenerCount)
		for pair := range at.screenerCandidates {
			screenerPairs = append(screenerPairs, pair)
		}
		at.screenerCandidatesMux.RUnlock()

		if screenerCount > 0 {
			// 创建现有币种的映射（用于去重）
			existingSymbols := make(map[string]int) // symbol -> index in candidateCoins
			for i, coin := range candidateCoins {
				existingSymbols[coin.Symbol] = i
			}

			// 合并Screener候选币种
			addedCount := 0
			mergedCount := 0
			for _, pair := range screenerPairs {
				symbol := normalizeSymbol(pair)
				if idx, exists := existingSymbols[symbol]; exists {
					// 币种已存在，添加"screener"到来源列表
					candidateCoins[idx].Sources = append(candidateCoins[idx].Sources, "screener")
					mergedCount++
				} else {
					// 新币种，添加到候选列表
					candidateCoins = append(candidateCoins, decision.CandidateCoin{
						Symbol:  symbol,
						Sources: []string{"screener"},
					})
					addedCount++
				}
			}

			log.Printf("🎯 [%s] Screener信号: 新增%d个币种，合并%d个币种 (总计%d个Screener候选)",
				at.name, addedCount, mergedCount, screenerCount)
		}
	}

	return candidateCoins, nil
}

// normalizeSymbol 标准化币种符号（确保以USDT结尾）
func normalizeSymbol(symbol string) string {
	// 转为大写
	symbol = strings.ToUpper(strings.TrimSpace(symbol))

	// 确保以USDT结尾
	if !strings.HasSuffix(symbol, "USDT") {
		symbol = symbol + "USDT"
	}

	return symbol
}

// 启动回撤监控
func (at *AutoTrader) startDrawdownMonitor() {
	at.monitorWg.Add(1)
	go func() {
		defer at.monitorWg.Done()

		ticker := time.NewTicker(1 * time.Minute) // 每分钟检查一次
		defer ticker.Stop()

		log.Println("📊 启动持仓回撤监控（每分钟检查一次）")

		for {
			select {
			case <-ticker.C:
				at.checkPositionDrawdown()
			case <-at.stopMonitorCh:
				log.Println("⏹ 停止持仓回撤监控")
				return
			}
		}
	}()
}

// 检查持仓回撤情况
func (at *AutoTrader) checkPositionDrawdown() {
	// 获取当前持仓
	positions, err := at.trader.GetPositions()
	if err != nil {
		log.Printf("❌ 回撤监控：获取持仓失败: %v", err)
		return
	}

	for _, pos := range positions {
		symbol := pos["symbol"].(string)
		side := pos["side"].(string)
		entryPrice := pos["entryPrice"].(float64)
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity // 空仓数量为负，转为正数
		}

		// 计算当前盈亏百分比
		leverage := 10 // 默认值
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}

		var currentPnLPct float64
		if side == "long" {
			currentPnLPct = ((markPrice - entryPrice) / entryPrice) * float64(leverage) * 100
		} else {
			currentPnLPct = ((entryPrice - markPrice) / entryPrice) * float64(leverage) * 100
		}

		// 获取该持仓的历史最高收益
		at.peakPnLCacheMutex.RLock()
		peakPnLPct, exists := at.peakPnLCache[symbol]
		at.peakPnLCacheMutex.RUnlock()

		if !exists {
			// 如果没有历史最高记录，使用当前盈亏作为初始值
			peakPnLPct = currentPnLPct
			at.UpdatePeakPnL(symbol, currentPnLPct)
		} else {
			// 更新峰值缓存
			at.UpdatePeakPnL(symbol, currentPnLPct)
		}

		// 计算回撤（从最高点下跌的幅度）
		var drawdownPct float64
		if peakPnLPct > 0 && currentPnLPct < peakPnLPct {
			drawdownPct = ((peakPnLPct - currentPnLPct) / peakPnLPct) * 100
		}

		// 检查平仓条件：收益大于5%且回撤超过40%
		if currentPnLPct > 5.0 && drawdownPct >= 40.0 {
			log.Printf("🚨 触发回撤平仓条件: %s %s | 当前收益: %.2f%% | 最高收益: %.2f%% | 回撤: %.2f%%",
				symbol, side, currentPnLPct, peakPnLPct, drawdownPct)

			// 执行平仓
			if err := at.emergencyClosePosition(symbol, side); err != nil {
				log.Printf("❌ 回撤平仓失败 (%s %s): %v", symbol, side, err)
			} else {
				log.Printf("✅ 回撤平仓成功: %s %s", symbol, side)
				// 平仓后清理该symbol的缓存
				at.ClearPeakPnLCache(symbol)
			}
		} else if currentPnLPct > 5.0 {
			// 记录接近平仓条件的情况（用于调试）
			log.Printf("📊 回撤监控: %s %s | 收益: %.2f%% | 最高: %.2f%% | 回撤: %.2f%%",
				symbol, side, currentPnLPct, peakPnLPct, drawdownPct)
		}
	}
}

// 紧急平仓函数
func (at *AutoTrader) emergencyClosePosition(symbol, side string) error {
	switch side {
	case "long":
		order, err := at.trader.CloseLong(symbol, 0) // 0 = 全部平仓
		if err != nil {
			return err
		}
		log.Printf("✅ 紧急平多仓成功，订单ID: %v", order["orderId"])
	case "short":
		order, err := at.trader.CloseShort(symbol, 0) // 0 = 全部平仓
		if err != nil {
			return err
		}
		log.Printf("✅ 紧急平空仓成功，订单ID: %v", order["orderId"])
	default:
		return fmt.Errorf("未知的持仓方向: %s", side)
	}

	return nil
}

// GetPeakPnLCache 获取最高收益缓存
func (at *AutoTrader) GetPeakPnLCache() map[string]float64 {
	at.peakPnLCacheMutex.RLock()
	defer at.peakPnLCacheMutex.RUnlock()

	// 返回缓存的副本
	cache := make(map[string]float64)
	for k, v := range at.peakPnLCache {
		cache[k] = v
	}
	return cache
}

// UpdatePeakPnL 更新最高收益缓存
func (at *AutoTrader) UpdatePeakPnL(symbol string, currentPnLPct float64) {
	at.peakPnLCacheMutex.Lock()
	defer at.peakPnLCacheMutex.Unlock()

	if peak, exists := at.peakPnLCache[symbol]; exists {
		// 更新峰值（如果是多头，取较大值；如果是空头，currentPnLPct为负，也要比较）
		if currentPnLPct > peak {
			at.peakPnLCache[symbol] = currentPnLPct
		}
	} else {
		// 首次记录
		at.peakPnLCache[symbol] = currentPnLPct
	}
}

// ClearPeakPnLCache 清除指定symbol的峰值缓存
func (at *AutoTrader) ClearPeakPnLCache(symbol string) {
	at.peakPnLCacheMutex.Lock()
	defer at.peakPnLCacheMutex.Unlock()

	delete(at.peakPnLCache, symbol)
}

// ============= Watchlist管理方法 =============

// addToWatchlist 添加币种到监控列表
func (at *AutoTrader) addToWatchlist(symbol string, reason string, priority int, sources []string, screenerData *decision.ScreenerData) error {
	at.watchlistMux.Lock()
	defer at.watchlistMux.Unlock()

	// 检查是否已在watchlist中
	if _, exists := at.watchlist[symbol]; exists {
		log.Printf("📝 [%s] %s 已在Watchlist中，更新优先级 %d -> %d", at.name, symbol, at.watchlist[symbol].Priority, priority)
		// 更新现有条目
		at.watchlist[symbol].Priority = priority
		at.watchlist[symbol].Reason = reason
		at.watchlist[symbol].LastAnalyzedAt = time.Now()
		return nil
	}

	// 检查watchlist大小限制
	if len(at.watchlist) >= at.maxWatchlistSize {
		// 移除最低优先级的条目
		var lowestPrioritySymbol string
		lowestPriority := 11 // 最高优先级是10
		for sym, entry := range at.watchlist {
			if entry.Priority < lowestPriority {
				lowestPriority = entry.Priority
				lowestPrioritySymbol = sym
			}
		}
		if lowestPrioritySymbol != "" && priority > lowestPriority {
			log.Printf("⚠️  [%s] Watchlist已满(%d)，移除低优先级币种: %s (优先级%d)",
				at.name, at.maxWatchlistSize, lowestPrioritySymbol, lowestPriority)
			delete(at.watchlist, lowestPrioritySymbol)
		} else {
			return fmt.Errorf("watchlist已满且新币种优先级不足")
		}
	}

	// 创建新条目
	entry := &WatchlistEntry{
		Symbol:         symbol,
		AddedAt:        time.Now(),
		Reason:         reason,
		LastAnalyzedAt: time.Now(),
		Priority:       priority,
		Sources:        sources,
	}

	// 如果有screener数据，填充
	if screenerData != nil {
		entry.ScreenerTotalSignals = screenerData.TotalSignals
		entry.ScreenerSCCount = screenerData.SCCount
		entry.ScreenerBTCCorr = screenerData.BTCCorr
		entry.ScreenerSCColor = screenerData.SCColor
	}

	at.watchlist[symbol] = entry
	log.Printf("➕ [%s] 添加到Watchlist: %s (优先级:%d) - %s", at.name, symbol, priority, reason)

	return nil
}

// removeFromWatchlist 从监控列表移除币种
func (at *AutoTrader) removeFromWatchlist(symbol string, reason string) {
	at.watchlistMux.Lock()
	defer at.watchlistMux.Unlock()

	if _, exists := at.watchlist[symbol]; exists {
		delete(at.watchlist, symbol)
		log.Printf("➖ [%s] 从Watchlist移除: %s - %s", at.name, symbol, reason)
	}
}

// isInWatchlist 检查币种是否在监控列表中
func (at *AutoTrader) isInWatchlist(symbol string) bool {
	at.watchlistMux.RLock()
	defer at.watchlistMux.RUnlock()
	_, exists := at.watchlist[symbol]
	return exists
}

// getWatchlistCount 获取监控列表大小
func (at *AutoTrader) getWatchlistCount() int {
	at.watchlistMux.RLock()
	defer at.watchlistMux.RUnlock()
	return len(at.watchlist)
}

// getWatchlistEntries 获取所有监控列表条目（按优先级排序）
func (at *AutoTrader) getWatchlistEntries() []*WatchlistEntry {
	at.watchlistMux.RLock()
	defer at.watchlistMux.RUnlock()

	entries := make([]*WatchlistEntry, 0, len(at.watchlist))
	for _, entry := range at.watchlist {
		// 创建副本避免并发问题
		entryCopy := *entry
		entries = append(entries, &entryCopy)
	}

	// 按优先级排序（从高到低）
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Priority > entries[j].Priority
	})

	return entries
}

// updateWatchlistLastAnalyzed 更新watchlist条目的最后分析时间
func (at *AutoTrader) updateWatchlistLastAnalyzed(symbol string) {
	at.watchlistMux.Lock()
	defer at.watchlistMux.Unlock()

	if entry, exists := at.watchlist[symbol]; exists {
		entry.LastAnalyzedAt = time.Now()
	}
}

// watchlistMonitor 监控watchlist中的币种（每分钟检查一次）
func (at *AutoTrader) watchlistMonitor() {
	defer at.monitorWg.Done()

	ticker := time.NewTicker(60 * time.Second) // 每分钟检查一次
	defer ticker.Stop()

	log.Printf("👀 [%s] Watchlist监控已启动（每分钟检查）", at.name)

	for {
		select {
		case <-at.stopMonitorCh:
			log.Printf("⏹  [%s] Watchlist监控已停止", at.name)
			return

		case <-ticker.C:
			at.analyzeWatchlist()
		}
	}
}

// paperTradingMonitor 监控Paper Trading虚拟持仓（检查止损止盈触发）
func (at *AutoTrader) paperTradingMonitor() {
	defer at.monitorWg.Done()

	ticker := time.NewTicker(30 * time.Second) // 每30秒检查一次（与持仓监控频率一致）
	defer ticker.Stop()

	log.Printf("📝 [%s] Paper Trading监控已启动（每30秒检查止损止盈）", at.name)

	for {
		select {
		case <-at.stopMonitorCh:
			log.Printf("⏹  [%s] Paper Trading监控已停止", at.name)
			return

		case <-ticker.C:
			// 更新所有开仓的未实现盈亏，并检查止损止盈触发
			if err := at.virtualPositionManager.UpdateUnrealizedPnL(); err != nil {
				log.Printf("❌ [Paper Trading] 更新未实现盈亏失败: %v", err)
			}

			// 定期输出统计信息（每5分钟）
			openPositions := at.virtualPositionManager.GetOpenPositions()
			if len(openPositions) > 0 {
				stats := at.virtualPositionManager.GetStatistics()
				log.Printf("📊 [Paper Trading] 开仓: %d | 余额: %.2f USDT | 权益: %.2f USDT | 总收益: %+.2f%%",
					len(openPositions),
					stats["current_balance"].(float64),
					stats["total_equity"].(float64),
					stats["total_return"].(float64))
			}
		}
	}
}

// analyzeWatchlist 分析watchlist中的所有币种
func (at *AutoTrader) analyzeWatchlist() {
	entries := at.getWatchlistEntries()
	if len(entries) == 0 {
		return
	}

	log.Printf("\n" + strings.Repeat("=", 70))
	log.Printf("👀 [%s] 分析Watchlist币种 (%d个)", at.name, len(entries))
	log.Println(strings.Repeat("=", 70))

	// 获取当前持仓数量
	positions, err := at.trader.GetPositions()
	if err != nil {
		log.Printf("❌ 获取持仓失败: %v", err)
		return
	}

	activePositionCount := 0
	for _, pos := range positions {
		if quantity, ok := pos["positionAmt"].(float64); ok && quantity != 0 {
			activePositionCount++
		}
	}

	// 检查是否还能开新仓
	canOpenNew := activePositionCount < at.maxPositions
	if !canOpenNew {
		log.Printf("⚠️  [%s] 已达最大持仓数(%d/%d)，暂不分析Watchlist",
			at.name, activePositionCount, at.maxPositions)
		return
	}

	availableSlots := at.maxPositions - activePositionCount
	log.Printf("📊 当前持仓: %d/%d | 可开新仓: %d个", activePositionCount, at.maxPositions, availableSlots)

	// 按优先级分析watchlist（从高到低）
	analyzedCount := 0
	for _, entry := range entries {
		// 更新最后分析时间
		at.updateWatchlistLastAnalyzed(entry.Symbol)

		log.Printf("\n🔍 分析 %s (优先级:%d, 来源:%v)",
			entry.Symbol, entry.Priority, entry.Sources)
		log.Printf("   添加原因: %s", entry.Reason)
		log.Printf("   添加时间: %s (%.0f分钟前)",
			entry.AddedAt.Format("15:04:05"),
			time.Since(entry.AddedAt).Minutes())

		// 构建分析上下文（只包含这个币种）
		ctx, err := at.buildWatchlistAnalysisContext(entry)
		if err != nil {
			log.Printf("   ❌ 构建分析上下文失败: %v", err)
			continue
		}

		// 调用AI分析
		decision, err := decision.GetFullDecisionWithCustomPrompt(ctx, at.mcpClient, at.customPrompt, at.overrideBasePrompt, at.systemPromptTemplate)
		if err != nil {
			log.Printf("   ❌ AI分析失败: %v", err)
			continue
		}

		// 处理AI决策
		at.processWatchlistDecision(entry, decision)

		analyzedCount++

		// 检查是否还有可用槽位
		if analyzedCount >= availableSlots {
			log.Printf("\n⚠️  已分析%d个币种（可用槽位已满），剩余%d个待下次分析",
				analyzedCount, len(entries)-analyzedCount)
			break
		}
	}

	log.Printf(strings.Repeat("=", 70) + "\n")
}

// buildWatchlistAnalysisContext 为watchlist币种构建分析上下文
func (at *AutoTrader) buildWatchlistAnalysisContext(entry *WatchlistEntry) (*decision.Context, error) {
	// 获取账户信息
	balance, err := at.trader.GetBalance()
	if err != nil {
		return nil, fmt.Errorf("获取账户余额失败: %w", err)
	}

	totalWalletBalance := 0.0
	totalUnrealizedProfit := 0.0
	availableBalance := 0.0

	if wallet, ok := balance["totalWalletBalance"].(float64); ok {
		totalWalletBalance = wallet
	}
	if unrealized, ok := balance["totalUnrealizedProfit"].(float64); ok {
		totalUnrealizedProfit = unrealized
	}
	if avail, ok := balance["availableBalance"].(float64); ok {
		availableBalance = avail
	}

	totalEquity := totalWalletBalance + totalUnrealizedProfit

	// 创建候选币种列表（只包含这一个币种）
	candidateCoins := []decision.CandidateCoin{
		{
			Symbol:  entry.Symbol,
			Sources: entry.Sources,
		},
	}

	// 获取Screener数据（如果有）
	var screenerDataMap map[string]*decision.ScreenerData
	if entry.ScreenerTotalSignals > 0 {
		screenerDataMap = make(map[string]*decision.ScreenerData)
		screenerDataMap[entry.Symbol] = &decision.ScreenerData{
			TotalSignals:  entry.ScreenerTotalSignals,
			SCCount:       entry.ScreenerSCCount,
			TSBinance:     entry.ScreenerTotalSignals - entry.ScreenerSCCount, // 简化
			BTCCorr:       entry.ScreenerBTCCorr,
			SCColor:       entry.ScreenerSCColor,
			LastSignalAge: int(time.Since(entry.LastAnalyzedAt).Minutes()),
		}
	}

	// 构建上下文
	ctx := &decision.Context{
		CurrentTime:      time.Now().Format("2006-01-02 15:04:05"),
		RuntimeMinutes:   int(time.Since(at.startTime).Minutes()),
		CallCount:        at.callCount,
		BTCETHLeverage:   at.config.BTCETHLeverage,
		AltcoinLeverage:  at.config.AltcoinLeverage,
		Account: decision.AccountInfo{
			TotalEquity:      totalEquity,
			AvailableBalance: availableBalance,
			PositionCount:    0, // Watchlist分析时不关心持仓数
		},
		Positions:       []decision.PositionInfo{}, // 空持仓列表
		CandidateCoins:  candidateCoins,
		ScreenerDataMap: screenerDataMap,
	}

	// 获取市场数据
	if err := decision.FetchMarketDataForContext(ctx); err != nil {
		return nil, fmt.Errorf("获取市场数据失败: %w", err)
	}

	// 添加Knowledge Base学习摘要（Paper Trading模式）
	if at.knowledgeBase != nil {
		summary, err := at.knowledgeBase.GetKnowledgeBaseSummary()
		if err == nil {
			ctx.KnowledgeBaseSummary = summary
		}
	}

	return ctx, nil
}

// processWatchlistDecision 处理watchlist分析决策
func (at *AutoTrader) processWatchlistDecision(entry *WatchlistEntry, fullDecision *decision.FullDecision) {
	if fullDecision == nil || len(fullDecision.Decisions) == 0 {
		log.Printf("   ⏸  AI决定: 继续观察")
		return
	}

	for _, d := range fullDecision.Decisions {
		if d.Symbol != entry.Symbol {
			continue
		}

		log.Printf("   🤖 AI决策: %s - %s", d.Action, d.Reasoning)

		switch d.Action {
		case "open_long", "open_short":
			// AI决定开仓 - 执行开仓并从watchlist移除
			log.Printf("   ✅ 条件成熟，准备开仓")

			// 创建action record用于记录
			actionRecord := &logger.DecisionAction{
				Symbol:    d.Symbol,
				Action:    d.Action,
			}

			// 执行开仓
			err := at.executeDecisionWithRecord(&d, actionRecord)
			if err != nil {
				log.Printf("   ❌ 开仓失败: %v", err)
			} else {
				// 开仓成功，从watchlist移除
				at.removeFromWatchlist(entry.Symbol, "已开仓")
			}

		case "remove_from_watchlist":
			// AI决定移除 - 从watchlist移除
			at.removeFromWatchlist(entry.Symbol, d.Reasoning)

		case "wait", "hold":
			// AI决定继续观察 - 不做任何操作
			log.Printf("   ⏸  继续观察")

		default:
			log.Printf("   ⚠️  未知操作: %s", d.Action)
		}
	}
}
