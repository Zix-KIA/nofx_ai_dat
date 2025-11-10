package trader

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// KnowledgeBase AI学习知识库
type KnowledgeBase struct {
	db       *sql.DB
	traderID string
}

// TradeRecord 交易记录（用于分析）
type TradeRecord struct {
	ID          string    `json:"id"`
	TraderID    string    `json:"trader_id"`
	Symbol      string    `json:"symbol"`
	Side        string    `json:"side"`
	EntryPrice  float64   `json:"entry_price"`
	ClosePrice  float64   `json:"close_price"`
	Quantity    float64   `json:"quantity"`
	Leverage    int       `json:"leverage"`
	OpenTime    time.Time `json:"open_time"`
	CloseTime   time.Time `json:"close_time"`
	RealizedPnL float64   `json:"realized_pnl"`
	PnLPercent  float64   `json:"pnl_percent"`
	CloseReason string    `json:"close_reason"`

	// Screener数据
	ScreenerTotalSignals int     `json:"screener_total_signals"`
	ScreenerSCColor      string  `json:"screener_sc_color"`
	ScreenerBTCCorr      float64 `json:"screener_btc_corr"`

	// AI决策
	AIReasoning  string `json:"ai_reasoning"`
	AIConfidence int    `json:"ai_confidence"`

	// 分析标签
	IsWinning    bool   `json:"is_winning"`
	HoldDuration int    `json:"hold_duration_minutes"`
	Pattern      string `json:"pattern"` // 自动识别的模式
}

// NewKnowledgeBase 创建知识库
func NewKnowledgeBase(db *sql.DB, traderID string) (*KnowledgeBase, error) {
	kb := &KnowledgeBase{
		db:       db,
		traderID: traderID,
	}

	// 初始化表
	if err := kb.initTables(); err != nil {
		return nil, fmt.Errorf("初始化知识库表失败: %w", err)
	}

	return kb, nil
}

// initTables 初始化知识库表
func (kb *KnowledgeBase) initTables() error {
	// 交易历史表
	createTradesTable := `
	CREATE TABLE IF NOT EXISTS kb_trades (
		id TEXT PRIMARY KEY,
		trader_id TEXT NOT NULL,
		symbol TEXT NOT NULL,
		side TEXT NOT NULL,
		entry_price REAL NOT NULL,
		close_price REAL NOT NULL,
		quantity REAL NOT NULL,
		leverage INTEGER NOT NULL,
		open_time DATETIME NOT NULL,
		close_time DATETIME NOT NULL,
		realized_pnl REAL NOT NULL,
		pnl_percent REAL NOT NULL,
		close_reason TEXT NOT NULL,
		screener_total_signals INTEGER DEFAULT 0,
		screener_sc_color TEXT DEFAULT '',
		screener_btc_corr REAL DEFAULT 0,
		ai_reasoning TEXT DEFAULT '',
		ai_confidence INTEGER DEFAULT 0,
		is_winning BOOLEAN NOT NULL,
		hold_duration_minutes INTEGER NOT NULL,
		pattern TEXT DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`

	// 模式统计表
	createPatternsTable := `
	CREATE TABLE IF NOT EXISTS kb_patterns (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		trader_id TEXT NOT NULL,
		pattern_name TEXT NOT NULL,
		pattern_type TEXT NOT NULL,
		total_occurrences INTEGER DEFAULT 0,
		winning_occurrences INTEGER DEFAULT 0,
		win_rate REAL DEFAULT 0,
		avg_pnl_percent REAL DEFAULT 0,
		description TEXT DEFAULT '',
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(trader_id, pattern_name)
	)`

	// 执行创建表
	_, err := kb.db.Exec(createTradesTable)
	if err != nil {
		return fmt.Errorf("创建kb_trades表失败: %w", err)
	}

	_, err = kb.db.Exec(createPatternsTable)
	if err != nil {
		return fmt.Errorf("创建kb_patterns表失败: %w", err)
	}

	log.Printf("✓ Knowledge Base表初始化完成")
	return nil
}

// RecordTrade 记录交易到知识库
func (kb *KnowledgeBase) RecordTrade(virtualPos *VirtualPosition) error {
	if virtualPos.CloseTime == nil {
		return fmt.Errorf("持仓未平仓，无法记录")
	}

	// 计算盈亏百分比
	pnlPercent := (virtualPos.RealizedPnL / (virtualPos.EntryPrice * virtualPos.Quantity)) * 100

	// 持续时间（分钟）
	holdDuration := int(virtualPos.CloseTime.Sub(virtualPos.OpenTime).Minutes())

	// 判断是否盈利
	isWinning := virtualPos.RealizedPnL > 0

	// 自动识别模式
	pattern := kb.identifyPattern(virtualPos)

	// 插入记录
	query := `
		INSERT INTO kb_trades (
			id, trader_id, symbol, side, entry_price, close_price, quantity, leverage,
			open_time, close_time, realized_pnl, pnl_percent, close_reason,
			screener_total_signals, screener_sc_color, screener_btc_corr,
			ai_reasoning, ai_confidence, is_winning, hold_duration_minutes, pattern
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := kb.db.Exec(query,
		virtualPos.ID,
		kb.traderID,
		virtualPos.Symbol,
		virtualPos.Side,
		virtualPos.EntryPrice,
		virtualPos.ClosePrice,
		virtualPos.Quantity,
		virtualPos.Leverage,
		virtualPos.OpenTime,
		*virtualPos.CloseTime,
		virtualPos.RealizedPnL,
		pnlPercent,
		virtualPos.CloseReason,
		virtualPos.ScreenerTotalSignals,
		virtualPos.ScreenerSCColor,
		virtualPos.ScreenerBTCCorr,
		virtualPos.AIReasoning,
		virtualPos.AIConfidence,
		isWinning,
		holdDuration,
		pattern,
	)

	if err != nil {
		return fmt.Errorf("记录交易失败: %w", err)
	}

	// 更新模式统计
	kb.updatePatternStats(pattern, isWinning, pnlPercent)

	log.Printf("📚 [Knowledge Base] 记录交易: %s | 盈亏%+.2f%% | 模式: %s",
		virtualPos.Symbol, pnlPercent, pattern)

	return nil
}

// identifyPattern 自动识别交易模式
func (kb *KnowledgeBase) identifyPattern(pos *VirtualPosition) string {
	patterns := []string{}

	// 基于Screener SC颜色
	if pos.ScreenerSCColor == "🟦" {
		patterns = append(patterns, "SC_BLUE")
	} else if pos.ScreenerSCColor == "🟢" {
		patterns = append(patterns, "SC_GREEN")
	}

	// 基于BTC相关性
	if pos.ScreenerBTCCorr < 0.3 {
		patterns = append(patterns, "LOW_BTC_CORR")
	} else if pos.ScreenerBTCCorr < 0.5 {
		patterns = append(patterns, "MED_BTC_CORR")
	} else {
		patterns = append(patterns, "HIGH_BTC_CORR")
	}

	// 基于信号数量
	if pos.ScreenerTotalSignals >= 20 {
		patterns = append(patterns, "HIGH_SIGNALS")
	} else if pos.ScreenerTotalSignals >= 10 {
		patterns = append(patterns, "MED_SIGNALS")
	}

	// 基于方向
	if pos.Side == "long" {
		patterns = append(patterns, "LONG")
	} else {
		patterns = append(patterns, "SHORT")
	}

	// 基于平仓原因
	if pos.CloseReason == "触发止盈" {
		patterns = append(patterns, "TP_HIT")
	} else if pos.CloseReason == "触发止损" {
		patterns = append(patterns, "SL_HIT")
	} else if pos.CloseReason == "AI决定平仓" {
		patterns = append(patterns, "AI_CLOSE")
	}

	// 组合成模式字符串
	pattern := ""
	for i, p := range patterns {
		if i > 0 {
			pattern += ","
		}
		pattern += p
	}

	return pattern
}

// updatePatternStats 更新模式统计
func (kb *KnowledgeBase) updatePatternStats(pattern string, isWinning bool, pnlPercent float64) error {
	if pattern == "" {
		return nil
	}

	// 检查是否已存在
	var exists bool
	err := kb.db.QueryRow(`
		SELECT EXISTS(SELECT 1 FROM kb_patterns WHERE trader_id = ? AND pattern_name = ?)
	`, kb.traderID, pattern).Scan(&exists)

	if err != nil {
		return err
	}

	if exists {
		// 更新现有记录
		winIncrement := 0
		if isWinning {
			winIncrement = 1
		}

		_, err = kb.db.Exec(`
			UPDATE kb_patterns
			SET total_occurrences = total_occurrences + 1,
			    winning_occurrences = winning_occurrences + ?,
			    win_rate = CAST(winning_occurrences + ? AS REAL) / (total_occurrences + 1) * 100,
			    avg_pnl_percent = (avg_pnl_percent * total_occurrences + ?) / (total_occurrences + 1),
			    updated_at = CURRENT_TIMESTAMP
			WHERE trader_id = ? AND pattern_name = ?
		`, winIncrement, winIncrement, pnlPercent, kb.traderID, pattern)
	} else {
		// 创建新记录
		winRate := 0.0
		if isWinning {
			winRate = 100.0
		}

		_, err = kb.db.Exec(`
			INSERT INTO kb_patterns (
				trader_id, pattern_name, pattern_type, total_occurrences,
				winning_occurrences, win_rate, avg_pnl_percent
			) VALUES (?, ?, ?, 1, ?, ?, ?)
		`, kb.traderID, pattern, "AUTO", func() int { if isWinning { return 1 } else { return 0 } }(), winRate, pnlPercent)
	}

	return err
}

// GetBestPatterns 获取最佳模式（胜率最高）
func (kb *KnowledgeBase) GetBestPatterns(minOccurrences int, limit int) ([]map[string]interface{}, error) {
	query := `
		SELECT pattern_name, total_occurrences, winning_occurrences, win_rate, avg_pnl_percent
		FROM kb_patterns
		WHERE trader_id = ? AND total_occurrences >= ?
		ORDER BY win_rate DESC, avg_pnl_percent DESC
		LIMIT ?
	`

	rows, err := kb.db.Query(query, kb.traderID, minOccurrences, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	patterns := []map[string]interface{}{}
	for rows.Next() {
		var patternName string
		var totalOccurrences, winningOccurrences int
		var winRate, avgPnL float64

		err := rows.Scan(&patternName, &totalOccurrences, &winningOccurrences, &winRate, &avgPnL)
		if err != nil {
			continue
		}

		patterns = append(patterns, map[string]interface{}{
			"pattern":            patternName,
			"total_occurrences":  totalOccurrences,
			"winning_occurrences": winningOccurrences,
			"win_rate":           winRate,
			"avg_pnl_percent":    avgPnL,
		})
	}

	return patterns, nil
}

// GetWorstPatterns 获取最差模式（胜率最低）
func (kb *KnowledgeBase) GetWorstPatterns(minOccurrences int, limit int) ([]map[string]interface{}, error) {
	query := `
		SELECT pattern_name, total_occurrences, winning_occurrences, win_rate, avg_pnl_percent
		FROM kb_patterns
		WHERE trader_id = ? AND total_occurrences >= ?
		ORDER BY win_rate ASC, avg_pnl_percent ASC
		LIMIT ?
	`

	rows, err := kb.db.Query(query, kb.traderID, minOccurrences, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	patterns := []map[string]interface{}{}
	for rows.Next() {
		var patternName string
		var totalOccurrences, winningOccurrences int
		var winRate, avgPnL float64

		err := rows.Scan(&patternName, &totalOccurrences, &winningOccurrences, &winRate, &avgPnL)
		if err != nil {
			continue
		}

		patterns = append(patterns, map[string]interface{}{
			"pattern":             patternName,
			"total_occurrences":   totalOccurrences,
			"winning_occurrences": winningOccurrences,
			"win_rate":            winRate,
			"avg_pnl_percent":     avgPnL,
		})
	}

	return patterns, nil
}

// GetOverallStatistics 获取总体统计
func (kb *KnowledgeBase) GetOverallStatistics() (map[string]interface{}, error) {
	query := `
		SELECT
			COUNT(*) as total_trades,
			SUM(CASE WHEN is_winning = 1 THEN 1 ELSE 0 END) as winning_trades,
			AVG(CASE WHEN is_winning = 1 THEN 1.0 ELSE 0.0 END) * 100 as win_rate,
			SUM(realized_pnl) as total_pnl,
			AVG(pnl_percent) as avg_pnl_percent,
			AVG(hold_duration_minutes) as avg_hold_duration
		FROM kb_trades
		WHERE trader_id = ?
	`

	var totalTrades, winningTrades int
	var winRate, totalPnL, avgPnLPercent, avgHoldDuration float64

	err := kb.db.QueryRow(query, kb.traderID).Scan(
		&totalTrades, &winningTrades, &winRate, &totalPnL, &avgPnLPercent, &avgHoldDuration,
	)

	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"total_trades":       totalTrades,
		"winning_trades":     winningTrades,
		"win_rate":           winRate,
		"total_pnl":          totalPnL,
		"avg_pnl_percent":    avgPnLPercent,
		"avg_hold_duration":  avgHoldDuration,
	}, nil
}

// GetKnowledgeBaseSummary 获取知识库摘要（用于AI prompt）
func (kb *KnowledgeBase) GetKnowledgeBaseSummary() (string, error) {
	// 获取总体统计
	stats, err := kb.GetOverallStatistics()
	if err != nil {
		return "", err
	}

	// 获取最佳模式
	bestPatterns, err := kb.GetBestPatterns(3, 5)
	if err != nil {
		return "", err
	}

	// 获取最差模式
	worstPatterns, err := kb.GetWorstPatterns(3, 5)
	if err != nil {
		return "", err
	}

	// 构建摘要
	summary := fmt.Sprintf(`
## 📚 Knowledge Base Summary

**Overall Performance:**
- Total Trades: %d
- Winning Trades: %d
- Win Rate: %.2f%%
- Total PnL: %+.2f USDT
- Avg PnL per Trade: %+.2f%%
- Avg Hold Duration: %.0f minutes

**Best Patterns (High Success Rate):**
`,
		stats["total_trades"].(int),
		stats["winning_trades"].(int),
		stats["win_rate"].(float64),
		stats["total_pnl"].(float64),
		stats["avg_pnl_percent"].(float64),
		stats["avg_hold_duration"].(float64),
	)

	for i, pattern := range bestPatterns {
		summary += fmt.Sprintf("  %d. %s: %.1f%% win rate (%d trades, avg PnL %+.2f%%)\n",
			i+1,
			pattern["pattern"].(string),
			pattern["win_rate"].(float64),
			pattern["total_occurrences"].(int),
			pattern["avg_pnl_percent"].(float64),
		)
	}

	summary += "\n**Worst Patterns (Avoid These):**\n"
	for i, pattern := range worstPatterns {
		summary += fmt.Sprintf("  %d. %s: %.1f%% win rate (%d trades, avg PnL %+.2f%%)\n",
			i+1,
			pattern["pattern"].(string),
			pattern["win_rate"].(float64),
			pattern["total_occurrences"].(int),
			pattern["avg_pnl_percent"].(float64),
		)
	}

	return summary, nil
}

// ExportToJSON 导出知识库为JSON（用于分析）
func (kb *KnowledgeBase) ExportToJSON() (string, error) {
	query := `
		SELECT * FROM kb_trades
		WHERE trader_id = ?
		ORDER BY close_time DESC
		LIMIT 100
	`

	rows, err := kb.db.Query(query, kb.traderID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	trades := []TradeRecord{}
	for rows.Next() {
		var trade TradeRecord
		var createdAt string

		err := rows.Scan(
			&trade.ID, &trade.TraderID, &trade.Symbol, &trade.Side,
			&trade.EntryPrice, &trade.ClosePrice, &trade.Quantity, &trade.Leverage,
			&trade.OpenTime, &trade.CloseTime, &trade.RealizedPnL, &trade.PnLPercent,
			&trade.CloseReason, &trade.ScreenerTotalSignals, &trade.ScreenerSCColor,
			&trade.ScreenerBTCCorr, &trade.AIReasoning, &trade.AIConfidence,
			&trade.IsWinning, &trade.HoldDuration, &trade.Pattern, &createdAt,
		)

		if err != nil {
			continue
		}

		trades = append(trades, trade)
	}

	jsonData, err := json.MarshalIndent(trades, "", "  ")
	if err != nil {
		return "", err
	}

	return string(jsonData), nil
}
