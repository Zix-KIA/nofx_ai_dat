package trader

import (
	"fmt"
	"log"
	"nofx/market"
	"sync"
	"time"
)

// VirtualPosition 虚拟持仓（Paper Trading）
type VirtualPosition struct {
	ID          string    // 唯一标识
	Symbol      string    // 交易对
	Side        string    // "long" or "short"
	EntryPrice  float64   // 入场价格
	Quantity    float64   // 数量
	Leverage    int       // 杠杆倍数
	StopLoss    float64   // 止损价格
	TakeProfit  float64   // 止盈价格
	OpenTime    time.Time // 开仓时间
	CloseTime   *time.Time // 平仓时间（nil表示未平仓）
	ClosePrice  float64   // 平仓价格
	RealizedPnL float64   // 已实现盈亏（平仓后）
	CloseReason string    // 平仓原因

	// Screener信息（用于分析）
	ScreenerTotalSignals int     // 开仓时的总信号数
	ScreenerSCColor      string  // SC颜色
	ScreenerBTCCorr      float64 // BTC相关性

	// AI决策信息
	AIReasoning  string // AI推理
	AIConfidence int    // AI信心度
}

// VirtualPositionManager Paper Trading虚拟持仓管理器
type VirtualPositionManager struct {
	positions       map[string]*VirtualPosition // ID -> Position
	positionsBySymbol map[string]string         // Symbol -> PositionID (只支持单向持仓)
	mutex           sync.RWMutex
	initialBalance  float64
	currentBalance  float64
	traderID        string

	// 用于Knowledge Base
	knowledgeBase   *KnowledgeBase
}

// NewVirtualPositionManager 创建虚拟持仓管理器
func NewVirtualPositionManager(traderID string, initialBalance float64, kb *KnowledgeBase) *VirtualPositionManager {
	return &VirtualPositionManager{
		positions:         make(map[string]*VirtualPosition),
		positionsBySymbol: make(map[string]string),
		initialBalance:    initialBalance,
		currentBalance:    initialBalance,
		traderID:          traderID,
		knowledgeBase:     kb,
	}
}

// OpenVirtualPosition 开虚拟仓位
func (vpm *VirtualPositionManager) OpenVirtualPosition(
	symbol, side string,
	entryPrice float64,
	positionSizeUSD float64,
	leverage int,
	stopLoss, takeProfit float64,
	aiReasoning string,
	aiConfidence int,
	screenerData map[string]interface{},
) (*VirtualPosition, error) {
	vpm.mutex.Lock()
	defer vpm.mutex.Unlock()

	// 检查是否已有该币种的持仓
	if existingID, exists := vpm.positionsBySymbol[symbol]; exists {
		return nil, fmt.Errorf("该币种已有持仓: %s (ID: %s)", symbol, existingID)
	}

	// 检查余额是否足够
	marginRequired := positionSizeUSD / float64(leverage)
	if marginRequired > vpm.currentBalance {
		return nil, fmt.Errorf("余额不足: 需要%.2f, 可用%.2f", marginRequired, vpm.currentBalance)
	}

	// 计算数量
	quantity := positionSizeUSD / entryPrice

	// 创建虚拟持仓
	positionID := fmt.Sprintf("VP_%s_%d", symbol, time.Now().UnixNano())
	position := &VirtualPosition{
		ID:           positionID,
		Symbol:       symbol,
		Side:         side,
		EntryPrice:   entryPrice,
		Quantity:     quantity,
		Leverage:     leverage,
		StopLoss:     stopLoss,
		TakeProfit:   takeProfit,
		OpenTime:     time.Now(),
		AIReasoning:  aiReasoning,
		AIConfidence: aiConfidence,
	}

	// 填充screener数据（如果有）
	if screenerData != nil {
		if totalSignals, ok := screenerData["total_signals"].(int); ok {
			position.ScreenerTotalSignals = totalSignals
		}
		if scColor, ok := screenerData["sc_color"].(string); ok {
			position.ScreenerSCColor = scColor
		}
		if btcCorr, ok := screenerData["btc_corr"].(float64); ok {
			position.ScreenerBTCCorr = btcCorr
		}
	}

	// 扣除保证金
	vpm.currentBalance -= marginRequired

	// 保存持仓
	vpm.positions[positionID] = position
	vpm.positionsBySymbol[symbol] = positionID

	log.Printf("📝 [Paper Trading] 开%s仓: %s | 价格%.4f | 杠杆%dx | 保证金%.2f",
		side, symbol, entryPrice, leverage, marginRequired)

	return position, nil
}

// CloseVirtualPosition 平虚拟仓位
func (vpm *VirtualPositionManager) CloseVirtualPosition(
	positionID string,
	closePrice float64,
	closeReason string,
) error {
	vpm.mutex.Lock()
	defer vpm.mutex.Unlock()

	position, exists := vpm.positions[positionID]
	if !exists {
		return fmt.Errorf("持仓不存在: %s", positionID)
	}

	if position.CloseTime != nil {
		return fmt.Errorf("持仓已平仓: %s", positionID)
	}

	// 计算盈亏
	var pnl float64
	if position.Side == "long" {
		pnl = (closePrice - position.EntryPrice) * position.Quantity * float64(position.Leverage)
	} else {
		pnl = (position.EntryPrice - closePrice) * position.Quantity * float64(position.Leverage)
	}

	pnlPct := (pnl / (position.EntryPrice * position.Quantity)) * 100

	// 更新持仓信息
	now := time.Now()
	position.CloseTime = &now
	position.ClosePrice = closePrice
	position.RealizedPnL = pnl
	position.CloseReason = closeReason

	// 返还保证金 + 盈亏
	marginUsed := (position.EntryPrice * position.Quantity) / float64(position.Leverage)
	vpm.currentBalance += marginUsed + pnl

	// 从symbol映射中移除
	delete(vpm.positionsBySymbol, position.Symbol)

	// 持续时间
	holdingDuration := position.CloseTime.Sub(position.OpenTime)

	log.Printf("📝 [Paper Trading] 平%s仓: %s | 入场%.4f 平仓%.4f | 盈亏%+.2f USDT (%+.2f%%) | 持仓时长%.0f分钟",
		position.Side, position.Symbol, position.EntryPrice, closePrice, pnl, pnlPct, holdingDuration.Minutes())

	// 记录到Knowledge Base
	if vpm.knowledgeBase != nil {
		vpm.knowledgeBase.RecordTrade(position)
	}

	return nil
}

// GetOpenPositions 获取所有开仓的虚拟持仓
func (vpm *VirtualPositionManager) GetOpenPositions() []*VirtualPosition {
	vpm.mutex.RLock()
	defer vpm.mutex.RUnlock()

	openPositions := make([]*VirtualPosition, 0)
	for _, pos := range vpm.positions {
		if pos.CloseTime == nil {
			openPositions = append(openPositions, pos)
		}
	}

	return openPositions
}

// GetPositionBySymbol 根据交易对获取持仓
func (vpm *VirtualPositionManager) GetPositionBySymbol(symbol string) (*VirtualPosition, bool) {
	vpm.mutex.RLock()
	defer vpm.mutex.RUnlock()

	positionID, exists := vpm.positionsBySymbol[symbol]
	if !exists {
		return nil, false
	}

	position, exists := vpm.positions[positionID]
	return position, exists
}

// UpdateUnrealizedPnL 更新所有开仓的未实现盈亏（用于监控）
func (vpm *VirtualPositionManager) UpdateUnrealizedPnL() error {
	openPositions := vpm.GetOpenPositions()
	if len(openPositions) == 0 {
		return nil
	}

	for _, pos := range openPositions {
		// 获取当前价格
		marketData, err := market.Get(pos.Symbol)
		if err != nil {
			log.Printf("⚠️  获取市场数据失败 %s: %v", pos.Symbol, err)
			continue
		}

		// 计算未实现盈亏
		var unrealizedPnL float64
		if pos.Side == "long" {
			unrealizedPnL = (marketData.CurrentPrice - pos.EntryPrice) * pos.Quantity * float64(pos.Leverage)
		} else {
			unrealizedPnL = (pos.EntryPrice - marketData.CurrentPrice) * pos.Quantity * float64(pos.Leverage)
		}

		unrealizedPnLPct := (unrealizedPnL / (pos.EntryPrice * pos.Quantity)) * 100

		// 检查止损止盈
		shouldClose := false
		closeReason := ""

		if pos.Side == "long" {
			if pos.StopLoss > 0 && marketData.CurrentPrice <= pos.StopLoss {
				shouldClose = true
				closeReason = "触发止损"
			} else if pos.TakeProfit > 0 && marketData.CurrentPrice >= pos.TakeProfit {
				shouldClose = true
				closeReason = "触发止盈"
			}
		} else {
			if pos.StopLoss > 0 && marketData.CurrentPrice >= pos.StopLoss {
				shouldClose = true
				closeReason = "触发止损"
			} else if pos.TakeProfit > 0 && marketData.CurrentPrice <= pos.TakeProfit {
				shouldClose = true
				closeReason = "触发止盈"
			}
		}

		// 如果触发止损止盈，平仓
		if shouldClose {
			log.Printf("🎯 [Paper Trading] %s: %s (%.2f%% PnL)",
				closeReason, pos.Symbol, unrealizedPnLPct)
			err := vpm.CloseVirtualPosition(pos.ID, marketData.CurrentPrice, closeReason)
			if err != nil {
				log.Printf("❌ 平仓失败: %v", err)
			}
		}
	}

	return nil
}

// GetBalance 获取当前余额
func (vpm *VirtualPositionManager) GetBalance() float64 {
	vpm.mutex.RLock()
	defer vpm.mutex.RUnlock()
	return vpm.currentBalance
}

// GetTotalEquity 获取总权益（余额+未实现盈亏）
func (vpm *VirtualPositionManager) GetTotalEquity() float64 {
	vpm.mutex.RLock()
	defer vpm.mutex.RUnlock()

	totalEquity := vpm.currentBalance

	// 加上所有开仓的未实现盈亏
	for _, pos := range vpm.positions {
		if pos.CloseTime == nil {
			// 获取当前价格
			marketData, err := market.Get(pos.Symbol)
			if err != nil {
				continue
			}

			var unrealizedPnL float64
			if pos.Side == "long" {
				unrealizedPnL = (marketData.CurrentPrice - pos.EntryPrice) * pos.Quantity * float64(pos.Leverage)
			} else {
				unrealizedPnL = (pos.EntryPrice - marketData.CurrentPrice) * pos.Quantity * float64(pos.Leverage)
			}

			totalEquity += unrealizedPnL
		}
	}

	return totalEquity
}

// GetStatistics 获取Paper Trading统计数据
func (vpm *VirtualPositionManager) GetStatistics() map[string]interface{} {
	vpm.mutex.RLock()
	defer vpm.mutex.RUnlock()

	totalTrades := 0
	winningTrades := 0
	totalPnL := 0.0

	for _, pos := range vpm.positions {
		if pos.CloseTime != nil {
			totalTrades++
			if pos.RealizedPnL > 0 {
				winningTrades++
			}
			totalPnL += pos.RealizedPnL
		}
	}

	winRate := 0.0
	if totalTrades > 0 {
		winRate = (float64(winningTrades) / float64(totalTrades)) * 100
	}

	currentEquity := vpm.GetTotalEquity()
	totalReturn := ((currentEquity - vpm.initialBalance) / vpm.initialBalance) * 100

	return map[string]interface{}{
		"initial_balance": vpm.initialBalance,
		"current_balance": vpm.currentBalance,
		"total_equity":    currentEquity,
		"total_pnl":       totalPnL,
		"total_return":    totalReturn,
		"total_trades":    totalTrades,
		"winning_trades":  winningTrades,
		"win_rate":        winRate,
		"open_positions":  len(vpm.GetOpenPositions()),
	}
}
