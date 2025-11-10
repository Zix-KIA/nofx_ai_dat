# Phase 4 Testing Plan - Paper Trading & Knowledge Base

**创建日期**: 2025-11-10
**测试范围**: Paper Trading + Knowledge Base + Watchlist + Realtime Signal Analysis
**测试类型**: 单元测试、集成测试、端到端测试

---

## 目录

1. [测试环境准备](#1-测试环境准备)
2. [单元测试 (Unit Tests)](#2-单元测试-unit-tests)
3. [集成测试 (Integration Tests)](#3-集成测试-integration-tests)
4. [端到端测试 (End-to-End Tests)](#4-端到端测试-end-to-end-tests)
5. [性能测试 (Performance Tests)](#5-性能测试-performance-tests)
6. [测试检查清单](#6-测试检查清单)

---

## 1. 测试环境准备

### 1.1 数据库准备

```bash
# 确认数据库包含所有必需的表
psql -U user -d nofx -c "\dt"

# 必需的表:
# - kb_trades (Knowledge Base交易记录)
# - kb_patterns (Knowledge Base模式统计)
# - pair_statistics2 (Screener信号数据)
# - trader_records (交易员配置)
```

### 1.2 配置 Paper Trading 模式

在 `trader_records` 表中设置:
```sql
-- 启用 Paper Trading 模式
UPDATE trader_records
SET config = jsonb_set(config, '{paper_trading_mode}', 'true')
WHERE id = 'your_trader_id';

-- 设置初始余额
UPDATE trader_records
SET initial_balance = 10000.0
WHERE id = 'your_trader_id';
```

### 1.3 Screener Listener 准备

确认 Screener Listener 正在运行并接收信号:
```bash
# 检查 pair_statistics2 表有数据
psql -U user -d nofx -c "SELECT COUNT(*) FROM pair_statistics2;"

# 确认最近有信号更新
psql -U user -d nofx -c "SELECT pair, total_signals, sc_last_color, last_signal_datetime FROM pair_statistics2 WHERE total_signals >= 10 ORDER BY last_signal_datetime DESC LIMIT 5;"
```

---

## 2. 单元测试 (Unit Tests)

### 2.1 Paper Trading - VirtualPositionManager

#### Test 2.1.1: 开虚拟多仓
**目标**: 验证 `OpenVirtualPosition()` 多仓逻辑

**步骤**:
1. 创建 `VirtualPositionManager` (余额 10000 USDT)
2. 调用 `OpenVirtualPosition("BTCUSDT", "long", 50000, 5000, 10, 49000, 52000, ...)`
3. 验证:
   - ✅ 虚拟持仓创建成功
   - ✅ `currentBalance` 减少保证金 (5000/10 = 500 USDT)
   - ✅ `positionsBySymbol["BTCUSDT"]` 存在
   - ✅ `StopLoss = 49000`, `TakeProfit = 52000`

**预期结果**: 持仓创建成功，余额正确扣除

---

#### Test 2.1.2: 开虚拟空仓
**目标**: 验证 `OpenVirtualPosition()` 空仓逻辑

**步骤**:
1. 调用 `OpenVirtualPosition("ETHUSDT", "short", 3000, 3000, 5, 3100, 2900, ...)`
2. 验证:
   - ✅ 空仓创建成功
   - ✅ `Side = "short"`
   - ✅ `currentBalance` 减少 600 USDT (3000/5)

**预期结果**: 空仓创建成功

---

#### Test 2.1.3: 平仓盈利计算
**目标**: 验证 `CloseVirtualPosition()` 盈利计算

**步骤**:
1. 开多仓: BTCUSDT @ 50000, 数量 0.1, 杠杆 10x
2. 平仓: 价格 52000 (上涨 4%)
3. 验证:
   - ✅ `RealizedPnL = (52000 - 50000) * 0.1 * 10 = 200 USDT`
   - ✅ `currentBalance` 增加 (保证金 500 + 盈亏 200 = 700)
   - ✅ `CloseTime` 不为 nil
   - ✅ `CloseReason` 正确记录

**预期结果**: 盈利 200 USDT，余额更新正确

---

#### Test 2.1.4: 平仓亏损计算
**目标**: 验证亏损计算

**步骤**:
1. 开空仓: ETHUSDT @ 3000, 数量 1, 杠杆 5x
2. 平仓: 价格 3100 (上涨 3.33%)
3. 验证:
   - ✅ `RealizedPnL = (3000 - 3100) * 1 * 5 = -500 USDT`
   - ✅ `currentBalance` 减少亏损

**预期结果**: 亏损 500 USDT

---

#### Test 2.1.5: 止损自动触发
**目标**: 验证 `UpdateUnrealizedPnL()` 止损逻辑

**步骤**:
1. 开多仓: BTCUSDT @ 50000, StopLoss 49000
2. 模拟价格下跌到 48990
3. 调用 `UpdateUnrealizedPnL()`
4. 验证:
   - ✅ 自动平仓
   - ✅ `CloseReason = "触发止损"`
   - ✅ 盈亏正确计算

**预期结果**: 止损自动执行

---

#### Test 2.1.6: 止盈自动触发
**目标**: 验证止盈逻辑

**步骤**:
1. 开多仓: BTCUSDT @ 50000, TakeProfit 52000
2. 模拟价格上涨到 52010
3. 调用 `UpdateUnrealizedPnL()`
4. 验证:
   - ✅ 自动平仓
   - ✅ `CloseReason = "触发止盈"`

**预期结果**: 止盈自动执行

---

#### Test 2.1.7: 余额不足拒绝开仓
**目标**: 验证余额保护

**步骤**:
1. 余额 1000 USDT
2. 尝试开仓: 仓位 10000 USDT, 杠杆 5x (需要 2000 保证金)
3. 验证:
   - ✅ 返回错误 "余额不足"
   - ✅ 持仓未创建

**预期结果**: 拒绝开仓

---

#### Test 2.1.8: 重复开仓保护
**目标**: 验证单向持仓限制

**步骤**:
1. 开仓 BTCUSDT 多仓
2. 尝试再次开仓 BTCUSDT (任何方向)
3. 验证:
   - ✅ 返回错误 "该币种已有持仓"

**预期结果**: 拒绝重复开仓

---

#### Test 2.1.9: GetStatistics 统计
**目标**: 验证统计数据

**步骤**:
1. 开仓并平仓 3 笔交易 (2 盈利, 1 亏损)
2. 调用 `GetStatistics()`
3. 验证:
   - ✅ `total_trades = 3`
   - ✅ `winning_trades = 2`
   - ✅ `win_rate = 66.67%`
   - ✅ `total_pnl` 正确
   - ✅ `total_return` 正确

**预期结果**: 统计数据准确

---

### 2.2 Knowledge Base - Pattern Recognition

#### Test 2.2.1: RecordTrade 记录交易
**目标**: 验证 `RecordTrade()` 保存到数据库

**步骤**:
1. 创建 `VirtualPosition` (已平仓)
2. 调用 `knowledgeBase.RecordTrade(position)`
3. 查询数据库:
```sql
SELECT * FROM kb_trades WHERE symbol = 'BTCUSDT' ORDER BY close_time DESC LIMIT 1;
```
4. 验证:
   - ✅ 记录存在
   - ✅ `realized_pnl` 正确
   - ✅ `pattern` 字段不为空
   - ✅ `is_winning` 正确

**预期结果**: 交易记录保存成功

---

#### Test 2.2.2: Pattern 识别 - SC_BLUE
**目标**: 验证 SC 颜色模式识别

**步骤**:
1. 创建持仓，设置 `ScreenerSCColor = "🟦"`
2. 调用 `identifyPattern()`
3. 验证:
   - ✅ Pattern 包含 "SC_BLUE"

**预期结果**: 模式正确识别

---

#### Test 2.2.3: Pattern 识别 - LOW_BTC_CORR
**目标**: 验证 BTC 相关性模式

**步骤**:
1. 创建持仓，设置 `ScreenerBTCCorr = 0.25`
2. 调用 `identifyPattern()`
3. 验证:
   - ✅ Pattern 包含 "LOW_BTC_CORR"

**预期结果**: 低相关性模式识别

---

#### Test 2.2.4: Pattern 识别 - HIGH_SIGNALS
**目标**: 验证信号数量模式

**步骤**:
1. 创建持仓，设置 `ScreenerTotalSignals = 25`
2. 调用 `identifyPattern()`
3. 验证:
   - ✅ Pattern 包含 "HIGH_SIGNALS"

**预期结果**: 高信号模式识别

---

#### Test 2.2.5: GetBestPatterns 最佳模式
**目标**: 验证模式统计

**步骤**:
1. 记录多笔交易 (包含不同模式，不同胜率)
2. 调用 `GetBestPatterns(5, 3)` (至少 3 次出现)
3. 验证:
   - ✅ 返回胜率最高的 5 个模式
   - ✅ 按胜率降序排列
   - ✅ 每个模式至少 3 次出现

**预期结果**: 返回高胜率模式

---

#### Test 2.2.6: GetWorstPatterns 最差模式
**目标**: 验证失败模式识别

**步骤**:
1. 调用 `GetWorstPatterns(5, 3)`
2. 验证:
   - ✅ 返回胜率最低的 5 个模式
   - ✅ 按胜率升序排列

**预期结果**: 返回低胜率模式

---

#### Test 2.2.7: GetKnowledgeBaseSummary 摘要
**目标**: 验证 KB 摘要生成

**步骤**:
1. 记录至少 10 笔交易
2. 调用 `GetKnowledgeBaseSummary()`
3. 验证:
   - ✅ 包含总交易数
   - ✅ 包含胜率
   - ✅ 包含最佳模式（如果有）
   - ✅ 包含最差模式（如果有）
   - ✅ 格式为清晰的文本

**预期结果**: 摘要清晰可读

---

### 2.3 Watchlist Management

#### Test 2.3.1: addToWatchlist 添加币种
**目标**: 验证添加到监控列表

**步骤**:
1. 调用 `addToWatchlist("SOLUSDT", "测试", 8, []string{"screener"}, screenerData)`
2. 验证:
   - ✅ `watchlist["SOLUSDT"]` 存在
   - ✅ `Priority = 8`
   - ✅ `AddedAt` 时间正确

**预期结果**: 币种添加成功

---

#### Test 2.3.2: removeFromWatchlist 移除币种
**目标**: 验证移除逻辑

**步骤**:
1. 添加 SOLUSDT 到 watchlist
2. 调用 `removeFromWatchlist("SOLUSDT", "测试移除")`
3. 验证:
   - ✅ `watchlist["SOLUSDT"]` 不存在

**预期结果**: 币种移除成功

---

#### Test 2.3.3: Watchlist 容量限制
**目标**: 验证 50 个上限

**步骤**:
1. 添加 50 个币种到 watchlist (优先级 1-10 随机)
2. 尝试添加第 51 个币种 (优先级 10)
3. 验证:
   - ✅ 第 51 个币种成功添加
   - ✅ 优先级最低的币种被自动移除
   - ✅ Watchlist 总数 = 50

**预期结果**: 自动移除低优先级

---

#### Test 2.3.4: Priority 提升
**目标**: 验证 `updateWatchlistWithNewSignal()` 优先级提升

**步骤**:
1. 添加 SOLUSDT (Priority 6)
2. 调用 `updateWatchlistWithNewSignal(event)` (新信号到来)
3. 验证:
   - ✅ Priority 增加到 7
   - ✅ Screener 数据更新

**预期结果**: 优先级自动提升

---

### 2.4 Realtime Signal Analysis

#### Test 2.4.1: analyzeNewSignalRealtime 异步分析
**目标**: 验证实时信号分析

**步骤**:
1. 创建 `ScreenerSignalEvent` (BTCUSDT, 总信号 15)
2. 调用 `analyzeNewSignalRealtime(event)`
3. 等待 10 秒
4. 验证:
   - ✅ AI 分析已触发
   - ✅ 如果持仓未满，AI 决定 open/add_to_watchlist/wait
   - ✅ 如果持仓已满，自动添加到 watchlist (Priority 7)

**预期结果**: 异步分析完成

---

#### Test 2.4.2: 持仓已满自动添加 Watchlist
**目标**: 验证持仓满时的处理

**步骤**:
1. 创建 10 个虚拟持仓 (达到上限)
2. 触发新信号 SOLUSDT
3. 验证:
   - ✅ SOLUSDT 自动添加到 watchlist
   - ✅ Priority = 7
   - ✅ 未尝试开仓

**预期结果**: 自动添加到 watchlist

---

## 3. 集成测试 (Integration Tests)

### 3.1 Paper Trading + Knowledge Base 集成

#### Test 3.1.1: 完整交易流程记录
**目标**: 验证从开仓到平仓再到 KB 记录的完整流程

**步骤**:
1. 开虚拟多仓 BTCUSDT
2. 等待或手动触发止盈
3. 验证:
   - ✅ 持仓已平仓
   - ✅ KB 自动记录交易 (`kb_trades` 表)
   - ✅ Pattern 识别正确
   - ✅ `kb_patterns` 表更新统计

**预期结果**: 完整流程无错误

---

#### Test 3.1.2: KB Summary 集成到 AI Prompts
**目标**: 验证 AI 能看到 KB 学习摘要

**步骤**:
1. 记录 10 笔交易到 KB
2. 触发一次 AI 决策 (`runCycle()`)
3. 检查 AI 输入 prompt (`record.InputPrompt`)
4. 验证:
   - ✅ Prompt 包含 "## 📚 Knowledge Base学习摘要"
   - ✅ 摘要显示总交易数、胜率、最佳/最差模式

**预期结果**: AI 可见 KB 摘要

---

### 3.2 Screener Listener + Realtime Analysis 集成

#### Test 3.2.1: Screener 信号触发实时分析
**目标**: 验证 Screener → AI 的完整链路

**步骤**:
1. 启动 Screener Listener
2. 插入新信号到 `pair_statistics2`:
```sql
UPDATE pair_statistics2
SET total_signals = 15, sc_last_color = '🟦', btc_corr_avg = 0.35
WHERE pair = 'SOLUSDT';
```
3. 等待 5-10 秒
4. 验证:
   - ✅ `watchScreenerSignals()` 接收到信号
   - ✅ `analyzeNewSignalRealtime()` 被触发
   - ✅ AI 分析完成，输出决策
   - ✅ 根据决策执行 open/add_to_watchlist/wait

**预期结果**: 信号触发 AI 分析

---

#### Test 3.2.2: Binance 过滤生效
**目标**: 验证 Bybit-only 信号被过滤

**步骤**:
1. 插入 Bybit-only 信号:
```sql
UPDATE pair_statistics2
SET total_signals = 15, ts_binance_count = 0, has_binance_count = 0, ts_bybit_count = 15
WHERE pair = 'ADAUSDT';
```
2. 等待 10 秒
3. 验证:
   - ✅ ADAUSDT 信号被跳过
   - ✅ 日志显示 "跳过"

**预期结果**: Bybit 信号被忽略

---

### 3.3 Watchlist + AI 集成

#### Test 3.3.1: Watchlist 定时分析
**目标**: 验证 watchlist 每 60 秒分析

**步骤**:
1. 添加 ETHUSDT 到 watchlist (Priority 9)
2. 等待 60-70 秒
3. 验证:
   - ✅ `watchlistMonitor()` 触发
   - ✅ `analyzeWatchlist()` 被调用
   - ✅ AI 重新分析 ETHUSDT
   - ✅ 根据分析结果执行 open/remove/hold

**预期结果**: 定时分析正常

---

#### Test 3.3.2: Watchlist 开仓后自动移除
**目标**: 验证开仓后从 watchlist 删除

**步骤**:
1. 添加 SOLUSDT 到 watchlist
2. 手动或等待 AI 决定开仓 SOLUSDT
3. 验证:
   - ✅ 开仓成功
   - ✅ SOLUSDT 从 watchlist 移除

**预期结果**: 自动移除

---

## 4. 端到端测试 (End-to-End Tests)

### 4.1 完整系统测试

#### Test 4.1.1: 系统启动到首次交易
**目标**: 验证完整启动流程

**步骤**:
1. 清空虚拟持仓和 KB 数据
2. 启动 AutoTrader (Paper Trading 模式)
3. 等待首次 `runCycle()` 执行
4. 验证:
   - ✅ Screener Listener 启动
   - ✅ Watchlist Monitor 启动
   - ✅ Paper Trading Monitor 启动
   - ✅ AI 分析完成
   - ✅ 如果有信号，AI 决定 open/add_to_watchlist/wait

**预期结果**: 系统正常启动

---

#### Test 4.1.2: 多币种并发管理
**目标**: 验证同时管理多个虚拟持仓和 watchlist

**步骤**:
1. 开 5 个虚拟持仓 (BTC, ETH, SOL, BNB, ADA)
2. 添加 10 个币种到 watchlist
3. 运行系统 30 分钟
4. 验证:
   - ✅ 所有持仓正确监控
   - ✅ 止损止盈自动触发
   - ✅ Watchlist 定时分析
   - ✅ 无并发冲突或死锁

**预期结果**: 多币种并发正常

---

#### Test 4.1.3: 止损触发 → KB 记录 → AI 学习
**目标**: 验证从止损到 AI 学习的完整流程

**步骤**:
1. 开仓 BTCUSDT (设置较近的止损)
2. 模拟价格下跌触发止损
3. 等待下一个 `runCycle()`
4. 验证:
   - ✅ 止损自动执行
   - ✅ KB 记录亏损交易
   - ✅ Pattern 识别为失败模式
   - ✅ 下次 AI prompt 包含此模式的负面反馈

**预期结果**: AI 学习到失败经验

---

#### Test 4.1.4: 高频信号处理
**目标**: 验证 1-5 信号/分钟的处理能力

**步骤**:
1. 模拟 Screener 发送 5 个信号/分钟 (BTC, ETH, SOL, BNB, ADA)
2. 运行系统 10 分钟
3. 验证:
   - ✅ 所有信号被接收
   - ✅ AI 分析不阻塞信号通道
   - ✅ 决策执行不延迟
   - ✅ 无信号丢失

**预期结果**: 高频信号处理正常

---

### 4.2 异常场景测试

#### Test 4.2.1: 网络中断恢复
**目标**: 验证市场数据获取失败的处理

**步骤**:
1. 开仓 BTCUSDT
2. 模拟 `market.Get()` 返回错误
3. 验证:
   - ✅ 错误被正确记录
   - ✅ 系统不崩溃
   - ✅ 下次 cycle 继续运行

**预期结果**: 错误处理正常

---

#### Test 4.2.2: 数据库连接失败
**目标**: 验证 KB 写入失败的处理

**步骤**:
1. 平仓一个虚拟持仓
2. 模拟数据库连接失败
3. 验证:
   - ✅ 平仓成功（内存中）
   - ✅ KB 记录失败被日志记录
   - ✅ 系统继续运行

**预期结果**: 容错处理正常

---

## 5. 性能测试 (Performance Tests)

### 5.1 响应时间测试

#### Test 5.1.1: Realtime Signal 分析时间
**目标**: 验证 <5 秒分析

**步骤**:
1. 触发新信号
2. 记录从信号接收到 AI 分析完成的时间
3. 验证:
   - ✅ 时间 < 5 秒

**预期结果**: 满足实时要求

---

#### Test 5.1.2: Watchlist 分析时间
**目标**: 验证 60 秒内完成所有 watchlist 分析

**步骤**:
1. Watchlist 有 20 个币种
2. 记录 `analyzeWatchlist()` 执行时间
3. 验证:
   - ✅ 时间 < 60 秒

**预期结果**: 不超时

---

### 5.2 并发测试

#### Test 5.2.1: Paper Trading Monitor 并发安全
**目标**: 验证 `UpdateUnrealizedPnL()` 线程安全

**步骤**:
1. 10 个虚拟持仓
2. 同时调用 `UpdateUnrealizedPnL()` 和 `OpenVirtualPosition()`
3. 验证:
   - ✅ 无数据竞争
   - ✅ 无死锁
   - ✅ 数据一致性

**预期结果**: 并发安全

---

## 6. 测试检查清单

### 6.1 单元测试完成度

- [ ] Test 2.1.1 - 开虚拟多仓
- [ ] Test 2.1.2 - 开虚拟空仓
- [ ] Test 2.1.3 - 平仓盈利计算
- [ ] Test 2.1.4 - 平仓亏损计算
- [ ] Test 2.1.5 - 止损自动触发
- [ ] Test 2.1.6 - 止盈自动触发
- [ ] Test 2.1.7 - 余额不足拒绝
- [ ] Test 2.1.8 - 重复开仓保护
- [ ] Test 2.1.9 - GetStatistics 统计
- [ ] Test 2.2.1 - RecordTrade 保存
- [ ] Test 2.2.2 - Pattern SC_BLUE
- [ ] Test 2.2.3 - Pattern LOW_BTC_CORR
- [ ] Test 2.2.4 - Pattern HIGH_SIGNALS
- [ ] Test 2.2.5 - GetBestPatterns
- [ ] Test 2.2.6 - GetWorstPatterns
- [ ] Test 2.2.7 - GetKnowledgeBaseSummary
- [ ] Test 2.3.1 - addToWatchlist
- [ ] Test 2.3.2 - removeFromWatchlist
- [ ] Test 2.3.3 - Watchlist 容量限制
- [ ] Test 2.3.4 - Priority 提升
- [ ] Test 2.4.1 - analyzeNewSignalRealtime
- [ ] Test 2.4.2 - 持仓满自动 Watchlist

### 6.2 集成测试完成度

- [ ] Test 3.1.1 - Paper Trading + KB 完整流程
- [ ] Test 3.1.2 - KB Summary 集成到 AI
- [ ] Test 3.2.1 - Screener 触发实时分析
- [ ] Test 3.2.2 - Binance 过滤
- [ ] Test 3.3.1 - Watchlist 定时分析
- [ ] Test 3.3.2 - Watchlist 开仓后移除

### 6.3 端到端测试完成度

- [ ] Test 4.1.1 - 系统启动到首次交易
- [ ] Test 4.1.2 - 多币种并发管理
- [ ] Test 4.1.3 - 止损 → KB → AI 学习
- [ ] Test 4.1.4 - 高频信号处理
- [ ] Test 4.2.1 - 网络中断恢复
- [ ] Test 4.2.2 - 数据库失败处理

### 6.4 性能测试完成度

- [ ] Test 5.1.1 - Realtime 分析 <5s
- [ ] Test 5.1.2 - Watchlist 分析 <60s
- [ ] Test 5.2.1 - 并发安全

---

## 7. 测试执行指南

### 7.1 运行单元测试

```bash
# 创建测试文件 trader/paper_trading_test.go
go test -v ./trader -run TestVirtualPositionManager

# 创建测试文件 trader/knowledge_base_test.go
go test -v ./trader -run TestKnowledgeBase
```

### 7.2 运行集成测试

```bash
# 需要数据库和 Screener 环境
go test -v ./trader -run TestIntegration
```

### 7.3 手动端到端测试

```bash
# 启动系统
go run main.go

# 观察日志输出
tail -f logs/nofx.log
```

---

## 8. 测试结果报告模板

```markdown
# 测试执行报告

**测试日期**: 2025-XX-XX
**测试人员**: XXX
**测试环境**: Production/Staging/Local

## 测试结果汇总

| 测试类型 | 总数 | 通过 | 失败 | 通过率 |
|---------|------|------|------|--------|
| 单元测试 | 22 | XX | XX | XX% |
| 集成测试 | 6 | XX | XX | XX% |
| 端到端测试 | 6 | XX | XX | XX% |
| 性能测试 | 3 | XX | XX | XX% |

## 失败测试详情

### Test X.X.X: 测试名称
**失败原因**: XXXX
**错误日志**: XXXX
**计划修复**: XXXX

## 建议

- [ ] XXXX
- [ ] XXXX
```

---

## 9. 测试优先级

**P0 (阻塞性)**: 必须通过才能上线
- Test 2.1.1 - 2.1.6 (Paper Trading 核心逻辑)
- Test 2.2.1 (KB 记录)
- Test 3.1.1 (完整流程)
- Test 4.1.1 (系统启动)

**P1 (重要)**: 影响核心功能
- Test 2.3.1 - 2.3.4 (Watchlist)
- Test 3.2.1 (Screener 集成)
- Test 5.1.1 (实时性能)

**P2 (一般)**: 改善用户体验
- Test 2.2.5 - 2.2.7 (KB 统计)
- Test 4.2.1 - 4.2.2 (异常处理)

---

**测试完成标准**: 所有 P0 和 P1 测试通过，P2 测试至少 80% 通过
